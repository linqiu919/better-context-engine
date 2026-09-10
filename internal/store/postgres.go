package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Postgres struct {
	db            *sql.DB
	encryptionKey [32]byte
	// legacyVec is true while the pre-pgvector bytea tables (chunk_embeddings,
	// hash_embeddings) still exist; vector reads then include a fallback arm
	// against them. The background migration drops the tables and clears the
	// flag once every row is copied into the vector-typed tables.
	legacyVec atomic.Bool
}

func NewPostgres(ctx context.Context, databaseURL, encryptionSecret string) (*Postgres, error) {
	if len(encryptionSecret) < 20 {
		return nil, errors.New("BCE_ENCRYPTION_KEY must contain at least 20 characters when PostgreSQL is enabled")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	p := &Postgres{db: db, encryptionKey: sha256.Sum256([]byte(encryptionSecret))}
	if err := p.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	var legacy bool
	if err := p.db.QueryRowContext(ctx, `SELECT to_regclass('chunk_embeddings') IS NOT NULL OR to_regclass('hash_embeddings') IS NOT NULL`).Scan(&legacy); err != nil {
		_ = db.Close()
		return nil, err
	}
	p.legacyVec.Store(legacy)
	go p.migrateChunkTerms()
	go func() {
		// Sketch backfill runs after the legacy migration: migrateVectors
		// writes sketches for the rows it copies, so ordering leaves no gap.
		p.migrateVectors()
		p.backfillSketches()
	}()
	return p, nil
}

// migrateChunkTerms retires the legacy text-keyed inverted-index table
// (chunk_terms): any remaining rows are copied into the rid-keyed
// chunk_postings table, then the table is dropped for good — the wide text
// key made it the single largest object in the database. Fresh installs never
// create the table, so the probe simply finds nothing. Runs in the background
// so startup is not blocked, copies in rid-range batches to keep transactions
// short, and is idempotent (ON CONFLICT DO NOTHING): a crash mid-way resumes
// on the next boot.
func (p *Postgres) migrateChunkTerms() {
	ctx := context.Background()
	var exists bool
	if err := p.db.QueryRowContext(ctx, `SELECT to_regclass('chunk_terms') IS NOT NULL`).Scan(&exists); err != nil {
		slog.Error("chunk_terms migration: probe failed", "err", err)
		return
	}
	if !exists {
		return
	}
	var hasLegacy bool
	if err := p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chunk_terms)`).Scan(&hasLegacy); err != nil {
		slog.Error("chunk_terms migration: probe failed", "err", err)
		return
	}
	if hasLegacy {
		// Only chunks that existed before this boot need copying; anything
		// newer was already written to chunk_postings directly.
		var maxRid int64
		if err := p.db.QueryRowContext(ctx, `SELECT COALESCE(max(rid),0) FROM chunks`).Scan(&maxRid); err != nil {
			slog.Error("chunk_terms migration: max rid failed", "err", err)
			return
		}
		slog.Info("chunk_terms migration: starting", "max_rid", maxRid)
		const batch = 20000 // rids per statement ≈ 1-2M posting rows
		for lo := int64(1); lo <= maxRid; lo += batch {
			hi := lo + batch
			if _, err := p.db.ExecContext(ctx, `
				INSERT INTO chunk_postings(chunk_rid,term,tf)
				SELECT c.rid, t.term, t.tf FROM chunk_terms t JOIN chunks c ON c.id = t.chunk_id
				WHERE c.rid >= $1 AND c.rid < $2
				ON CONFLICT(chunk_rid,term) DO NOTHING`, lo, hi); err != nil {
				slog.Error("chunk_terms migration: batch failed, will retry next boot", "from_rid", lo, "err", err)
				return
			}
			slog.Info("chunk_terms migration: batch done", "through_rid", min(hi-1, maxRid), "max_rid", maxRid)
		}
	}
	if _, err := p.db.ExecContext(ctx, `DROP TABLE chunk_terms`); err != nil {
		slog.Error("chunk_terms migration: drop failed", "err", err)
		return
	}
	slog.Info("chunk_terms migration: complete, legacy table dropped")
}

// migrateVectors retires the pre-pgvector bytea tables: every row is copied
// into the vector-typed tables (decoding float32-LE in Go, re-encoding as a
// pgvector literal), then the old tables are dropped and the legacy read arms
// switch off. Background, keyset-paginated, and idempotent (ON CONFLICT DO
// NOTHING — concurrent fresh writes go to the new tables directly and win).
func (p *Postgres) migrateVectors() {
	if !p.legacyVec.Load() {
		return
	}
	ctx := context.Background()
	type spec struct{ oldTable, keyCol, newTable, newKeyCol string }
	specs := []spec{
		{"hash_embeddings", "content_hash", "hash_vectors", "content_hash"},
		{"chunk_embeddings", "chunk_id", "chunk_vectors", "chunk_id"},
	}
	for _, sp := range specs {
		var exists bool
		if err := p.db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, sp.oldTable).Scan(&exists); err != nil {
			slog.Error("vector migration: probe failed", "table", sp.oldTable, "err", err)
			return
		}
		if !exists {
			continue
		}
		slog.Info("vector migration: starting", "table", sp.oldTable)
		lastKey, lastModel, copied := "", "", 0
		for {
			rows, err := p.db.QueryContext(ctx, fmt.Sprintf(
				`SELECT %s, model_id, dims, vector FROM %s WHERE (%s, model_id) > ($1, $2) ORDER BY %s, model_id LIMIT 200`,
				sp.keyCol, sp.oldTable, sp.keyCol, sp.keyCol), lastKey, lastModel)
			if err != nil {
				slog.Error("vector migration: read failed, will retry next boot", "table", sp.oldTable, "err", err)
				return
			}
			type row struct {
				key, model string
				dims       int
				raw        []byte
			}
			batch := []row{}
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.key, &r.model, &r.dims, &r.raw); err != nil {
					rows.Close()
					slog.Error("vector migration: scan failed", "table", sp.oldTable, "err", err)
					return
				}
				batch = append(batch, r)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				slog.Error("vector migration: read failed, will retry next boot", "table", sp.oldTable, "err", err)
				return
			}
			if len(batch) == 0 {
				break
			}
			placeholders := make([]string, 0, len(batch))
			args := make([]any, 0, len(batch)*4)
			for i, r := range batch {
				base := i * 4
				placeholders = append(placeholders, fmt.Sprintf("($%d,$%d,$%d,$%d::vector,binary_quantize($%d::vector))", base+1, base+2, base+3, base+4, base+4))
				args = append(args, r.key, r.model, r.dims, formatVector(decodeVector(r.raw)))
			}
			if _, err := p.db.ExecContext(ctx, fmt.Sprintf(
				`INSERT INTO %s(%s,model_id,dims,vec,sketch) VALUES %s ON CONFLICT(%s,model_id) DO NOTHING`,
				sp.newTable, sp.newKeyCol, strings.Join(placeholders, ","), sp.newKeyCol), args...); err != nil {
				slog.Error("vector migration: insert failed, will retry next boot", "table", sp.newTable, "err", err)
				return
			}
			last := batch[len(batch)-1]
			lastKey, lastModel = last.key, last.model
			copied += len(batch)
			if copied%20000 == 0 {
				slog.Info("vector migration: progress", "table", sp.oldTable, "rows", copied)
			}
		}
		if _, err := p.db.ExecContext(ctx, `DROP TABLE `+sp.oldTable); err != nil {
			slog.Error("vector migration: drop failed", "table", sp.oldTable, "err", err)
			return
		}
		slog.Info("vector migration: table done and dropped", "table", sp.oldTable, "rows", copied)
	}
	p.legacyVec.Store(false)
	slog.Info("vector migration: complete, legacy fallback disabled")
}

// backfillSketches fills the binary-quantized sketch column for vectors that
// predate it. The sketch (128 bytes at 1024 dims, stored inline) is what the
// search prefilter's semantic file selection scans: full-precision vectors
// are ~5.5KB each and mostly TOASTed, so selecting files over a big project
// read ~200MB and took 6-16s cold — the sketch scan reads ~40x less. Batches
// are small and spaced out so production queries keep priority; a crash
// resumes on the next boot (sketch IS NULL is the work queue).
func (p *Postgres) backfillSketches() {
	ctx := context.Background()
	for _, table := range []string{"hash_vectors", "chunk_vectors"} {
		total := 0
		for {
			res, err := p.db.ExecContext(ctx, fmt.Sprintf(
				`UPDATE %s SET sketch = binary_quantize(vec) WHERE ctid IN (SELECT ctid FROM %s WHERE sketch IS NULL LIMIT 2000)`, table, table))
			if err != nil {
				slog.Error("sketch backfill: update failed, will resume next boot", "table", table, "err", err)
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				break
			}
			total += int(n)
			if total%50000 < 2000 {
				slog.Info("sketch backfill: progress", "table", table, "rows", total)
			}
			time.Sleep(300 * time.Millisecond)
		}
		if total > 0 {
			slog.Info("sketch backfill: table complete", "table", table, "rows", total)
		}
	}
}

func (p *Postgres) Close() error { return p.db.Close() }

func (p *Postgres) migrate(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, schema)
	return err
}

func mapSQLError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// sealToken encrypts a personal token for at-rest storage; empty stays empty
// so users without a token do not produce a ciphertext.
func (p *Postgres) sealToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	return p.encrypt(token)
}

// userColumns is the single source of truth for the users column list; keep
// it in sync with scanUser, CreateUser and UpdateUser.
const userColumns = "id,username,role,disabled,password_hash,ace_token_hash,ace_token_cipher,email,avatar_url,linuxdo_id,linuxdo_username,trust_level,created_at,last_active_at,max_output_tokens"

func (p *Postgres) CreateUser(ctx context.Context, u domain.User) error {
	cipherToken, err := p.sealToken(u.ACEToken)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `INSERT INTO users(`+userColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, u.ID, u.Username, u.Role, u.Disabled, u.PasswordHash, u.ACETokenHash, cipherToken, u.Email, u.AvatarURL, u.LinuxDoID, u.LinuxDoUsername, u.TrustLevel, u.CreatedAt, u.LastActiveAt, u.MaxOutputTokens)
	return err
}
func (p *Postgres) UpdateUser(ctx context.Context, u domain.User) error {
	cipherToken, err := p.sealToken(u.ACEToken)
	if err != nil {
		return err
	}
	r, err := p.db.ExecContext(ctx, `UPDATE users SET username=$2,role=$3,disabled=$4,password_hash=$5,ace_token_hash=$6,ace_token_cipher=$7,email=$8,avatar_url=$9,linuxdo_id=$10,linuxdo_username=$11,trust_level=$12,last_active_at=$13,max_output_tokens=$14 WHERE id=$1`, u.ID, u.Username, u.Role, u.Disabled, u.PasswordHash, u.ACETokenHash, cipherToken, u.Email, u.AvatarURL, u.LinuxDoID, u.LinuxDoUsername, u.TrustLevel, u.LastActiveAt, u.MaxOutputTokens)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (p *Postgres) scanUser(row interface{ Scan(...any) error }) (domain.User, error) {
	var u domain.User
	var cipherToken string
	if err := row.Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &u.PasswordHash, &u.ACETokenHash, &cipherToken, &u.Email, &u.AvatarURL, &u.LinuxDoID, &u.LinuxDoUsername, &u.TrustLevel, &u.CreatedAt, &u.LastActiveAt, &u.MaxOutputTokens); err != nil {
		return u, mapSQLError(err)
	}
	if cipherToken != "" {
		var err error
		if u.ACEToken, err = p.decrypt(cipherToken); err != nil {
			return u, err
		}
	}
	return u, nil
}
func (p *Postgres) UserByUsername(ctx context.Context, name string) (domain.User, error) {
	return p.scanUser(p.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username=$1`, name))
}
func (p *Postgres) UserByID(ctx context.Context, id string) (domain.User, error) {
	return p.scanUser(p.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
}
func (p *Postgres) UserByACETokenHash(ctx context.Context, hash string) (domain.User, error) {
	if hash == "" {
		return domain.User{}, ErrNotFound
	}
	return p.scanUser(p.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE ace_token_hash=$1`, hash))
}
func (p *Postgres) UserByEmail(ctx context.Context, email string) (domain.User, error) {
	if email == "" {
		return domain.User{}, ErrNotFound
	}
	return p.scanUser(p.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email=$1`, email))
}
func (p *Postgres) UserByLinuxDoID(ctx context.Context, id int64) (domain.User, error) {
	if id == 0 {
		return domain.User{}, ErrNotFound
	}
	return p.scanUser(p.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE linuxdo_id=$1`, id))
}
func (p *Postgres) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.User{}
	for rows.Next() {
		u, e := p.scanUser(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (p *Postgres) CreateSession(ctx context.Context, s domain.Session) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,csrf_token,user_id,expires_at) VALUES($1,$2,$3,$4)`, s.TokenHash, s.CSRFToken, s.UserID, s.ExpiresAt)
	return err
}
func (p *Postgres) SessionByTokenHash(ctx context.Context, h string) (domain.Session, error) {
	var s domain.Session
	err := p.db.QueryRowContext(ctx, `SELECT token_hash,csrf_token,user_id,expires_at FROM sessions WHERE token_hash=$1 AND expires_at>now()`, h).Scan(&s.TokenHash, &s.CSRFToken, &s.UserID, &s.ExpiresAt)
	return s, mapSQLError(err)
}
func (p *Postgres) DeleteSession(ctx context.Context, h string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=$1`, h)
	return err
}
func (p *Postgres) DeleteUserSessions(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=$1`, id)
	return err
}
func (p *Postgres) DeleteExpiredSessions(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=now()`)
	return err
}

func (p *Postgres) CreateRepository(ctx context.Context, r domain.Repository) error {
	query, args := repoInsertArgs(r)
	_, err := p.db.ExecContext(ctx, query, args...)
	return err
}
func repoInsertArgs(r domain.Repository) (string, []any) {
	return `INSERT INTO repositories(id,owner_id,name,root_path,branch,base_snapshot,snapshot_id,status,file_count,chunk_count,storage_bytes,current_revision,indexed_revision,index_lag_ms,search_p95_ms,embedding_model,last_synced_at,created_at,updated_at,paused) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, []any{r.ID, r.OwnerID, r.Name, r.RootPath, r.Branch, r.BaseSnapshot, r.SnapshotID, r.Status, r.FileCount, r.ChunkCount, r.StorageBytes, r.CurrentRevision, r.IndexedRevision, r.IndexLagMS, r.SearchP95MS, r.EmbeddingModel, r.LastSyncedAt, r.CreatedAt, r.UpdatedAt, r.Paused}
}
func (p *Postgres) UpdateRepository(ctx context.Context, r domain.Repository) error {
	res, err := p.db.ExecContext(ctx, `UPDATE repositories SET name=$2,root_path=$3,branch=$4,base_snapshot=$5,snapshot_id=$6,status=$7,file_count=$8,chunk_count=$9,storage_bytes=$10,current_revision=$11,indexed_revision=$12,index_lag_ms=$13,search_p95_ms=$14,embedding_model=$15,last_synced_at=$16,updated_at=$17,paused=$18 WHERE id=$1`, r.ID, r.Name, r.RootPath, r.Branch, r.BaseSnapshot, r.SnapshotID, r.Status, r.FileCount, r.ChunkCount, r.StorageBytes, r.CurrentRevision, r.IndexedRevision, r.IndexLagMS, r.SearchP95MS, r.EmbeddingModel, r.LastSyncedAt, r.UpdatedAt, r.Paused)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const repoSelect = `SELECT r.id,r.owner_id,u.username,r.name,r.root_path,r.branch,r.base_snapshot,r.snapshot_id,r.status,r.file_count,r.chunk_count,r.storage_bytes,r.current_revision,r.indexed_revision,r.index_lag_ms,r.search_p95_ms,r.embedding_model,r.last_synced_at,r.created_at,r.updated_at,r.paused FROM repositories r JOIN users u ON u.id=r.owner_id`

func scanRepo(row interface{ Scan(...any) error }) (domain.Repository, error) {
	var r domain.Repository
	err := row.Scan(&r.ID, &r.OwnerID, &r.OwnerUsername, &r.Name, &r.RootPath, &r.Branch, &r.BaseSnapshot, &r.SnapshotID, &r.Status, &r.FileCount, &r.ChunkCount, &r.StorageBytes, &r.CurrentRevision, &r.IndexedRevision, &r.IndexLagMS, &r.SearchP95MS, &r.EmbeddingModel, &r.LastSyncedAt, &r.CreatedAt, &r.UpdatedAt, &r.Paused)
	return r, mapSQLError(err)
}
func (p *Postgres) RepositoryByID(ctx context.Context, id string) (domain.Repository, error) {
	return scanRepo(p.db.QueryRowContext(ctx, repoSelect+` WHERE r.id=$1`, id))
}
func (p *Postgres) ListRepositories(ctx context.Context, owner string, all bool) ([]domain.Repository, error) {
	q := repoSelect
	args := []any{}
	if !all {
		q += ` WHERE r.owner_id=$1`
		args = append(args, owner)
	}
	q += ` ORDER BY r.updated_at DESC`
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Repository{}
	for rows.Next() {
		r, e := scanRepo(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (p *Postgres) PutBlob(ctx context.Context, b domain.Blob) error {
	encrypted, err := p.encrypt(b.Content)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `INSERT INTO blobs(name,path,content,created_at) VALUES($1,$2,$3,$4) ON CONFLICT(name) DO NOTHING`, b.Name, b.Path, encrypted, b.CreatedAt)
	return err
}
func (p *Postgres) ExistingBlobNames(ctx context.Context, names []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := p.db.QueryContext(ctx, `SELECT name FROM blobs WHERE name = ANY($1)`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}
func (p *Postgres) BlobsByNames(ctx context.Context, names []string) ([]domain.Blob, error) {
	if len(names) == 0 {
		return []domain.Blob{}, nil
	}
	rows, err := p.db.QueryContext(ctx, `SELECT name,path,content,created_at FROM blobs WHERE name = ANY($1)`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Blob, 0, len(names))
	for rows.Next() {
		var b domain.Blob
		var encrypted string
		if err := rows.Scan(&b.Name, &b.Path, &encrypted, &b.CreatedAt); err != nil {
			return nil, err
		}
		if b.Content, err = p.decrypt(encrypted); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (p *Postgres) ReplaceRepositoryFiles(ctx context.Context, repo string, upserts []domain.RepositoryFile, deleted []string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, path := range deleted {
		if _, err = tx.ExecContext(ctx, `DELETE FROM repository_files WHERE repository_id=$1 AND path=$2`, repo, path); err != nil {
			return err
		}
	}
	for _, f := range upserts {
		_, err = tx.ExecContext(ctx, `INSERT INTO repository_files(repository_id,blob_name,path,content_hash,line_count,language,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(repository_id,path) DO UPDATE SET blob_name=excluded.blob_name,content_hash=excluded.content_hash,line_count=excluded.line_count,language=excluded.language,updated_at=excluded.updated_at`, f.RepositoryID, f.BlobName, f.Path, f.ContentHash, f.LineCount, f.Language, f.UpdatedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (p *Postgres) RepositoryFiles(ctx context.Context, repo string) ([]domain.RepositoryFile, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT rf.repository_id,rf.blob_name,rf.path,b.content,rf.content_hash,rf.line_count,rf.language,rf.updated_at FROM repository_files rf JOIN blobs b ON b.name=rf.blob_name WHERE rf.repository_id=$1 ORDER BY rf.path`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.RepositoryFile{}
	for rows.Next() {
		var f domain.RepositoryFile
		var encrypted string
		if err := rows.Scan(&f.RepositoryID, &f.BlobName, &f.Path, &encrypted, &f.ContentHash, &f.LineCount, &f.Language, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.Content, err = p.decrypt(encrypted)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (p *Postgres) encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(p.encryptionKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return "enc:v1:" + base64.RawStdEncoding.EncodeToString(ciphertext), nil
}

// EstimatedStoredSize is the byte length encrypt produces for a plaintext of
// the given size: "enc:v1:" (7) plus unpadded base64 of nonce(12)||plaintext||
// tag(16). Quota accounting uses it so incoming uploads are measured in the
// same ciphertext units the database reports back. Keep it in lockstep with
// encrypt above.
func EstimatedStoredSize(plain int64) int64 {
	return (4*(plain+28)+2)/3 + 7
}

func (p *Postgres) decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, "enc:v1:") {
		return value, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "enc:v1:"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(p.encryptionKey[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("encrypted blob is truncated")
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt blob: %w", err)
	}
	return string(plaintext), nil
}

// PutChunks writes a blob's chunks and inverted-index postings in one
// transaction. It is idempotent per blob: if the blob already has chunks the
// whole call is a no-op, so postings are only ever written by the first
// indexing of a blob and re-uploads of identical content cost one SELECT.
func (p *Postgres) PutChunks(ctx context.Context, blobName string, chunks []domain.Chunk, postings []domain.ChunkPosting) error {
	var exists bool
	if err := p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chunks WHERE blob_name=$1)`, blobName).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Multi-row inserts: one statement per group instead of one per row. A
	// large blob carries thousands of posting rows; row-at-a-time execution
	// made the write phase dominate first-upload latency.
	const chunkCols, chunkRowsPerStmt = 11, 500
	for start := 0; start < len(chunks); start += chunkRowsPerStmt {
		group := chunks[start:min(start+chunkRowsPerStmt, len(chunks))]
		placeholders := make([]string, 0, len(group))
		args := make([]any, 0, len(group)*chunkCols)
		for i, c := range group {
			base := i * chunkCols
			placeholders = append(placeholders, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11))
			args = append(args, c.ID, c.BlobName, c.Seq, c.Symbol, c.SymbolKind, c.StartLine, c.EndLine, c.Language, c.TokenCount, c.Summary, c.ContentHash)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chunks(id,blob_name,seq,symbol,symbol_kind,start_line,end_line,language,token_count,summary,content_hash) VALUES `+strings.Join(placeholders, ",")+` ON CONFLICT(id) DO NOTHING`, args...); err != nil {
			return err
		}
	}
	// Postings are keyed by the chunk's int8 surrogate rid, not its ~70-char
	// text ID: at tens of millions of rows the wide text key (stored again in
	// the primary key) dominated the whole database's disk footprint.
	rids := map[string]int64{}
	ridRows, err := tx.QueryContext(ctx, `SELECT id, rid FROM chunks WHERE blob_name=$1`, blobName)
	if err != nil {
		return err
	}
	for ridRows.Next() {
		var id string
		var rid int64
		if err := ridRows.Scan(&id, &rid); err != nil {
			ridRows.Close()
			return err
		}
		rids[id] = rid
	}
	ridRows.Close()
	if err := ridRows.Err(); err != nil {
		return err
	}
	const postingCols, postingRowsPerStmt = 3, 5000
	for start := 0; start < len(postings); start += postingRowsPerStmt {
		group := postings[start:min(start+postingRowsPerStmt, len(postings))]
		placeholders := make([]string, 0, len(group))
		args := make([]any, 0, len(group)*postingCols)
		for i, posting := range group {
			base := i * postingCols
			placeholders = append(placeholders, fmt.Sprintf("($%d,$%d,$%d)", base+1, base+2, base+3))
			args = append(args, rids[posting.ChunkID], posting.Term, posting.TF)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chunk_postings(chunk_rid,term,tf) VALUES `+strings.Join(placeholders, ",")+` ON CONFLICT(chunk_rid,term) DO NOTHING`, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (p *Postgres) ChunksByBlobNames(ctx context.Context, names []string) ([]domain.Chunk, error) {
	if len(names) == 0 {
		return []domain.Chunk{}, nil
	}
	rows, err := p.db.QueryContext(ctx, `SELECT id,blob_name,seq,symbol,symbol_kind,start_line,end_line,language,token_count,summary,content_hash FROM chunks WHERE blob_name = ANY($1) ORDER BY blob_name,seq`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Chunk{}
	for rows.Next() {
		var c domain.Chunk
		if err := rows.Scan(&c.ID, &c.BlobName, &c.Seq, &c.Symbol, &c.SymbolKind, &c.StartLine, &c.EndLine, &c.Language, &c.TokenCount, &c.Summary, &c.ContentHash); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateChunkSummaries backfills LLM summaries after indexing; one
// transaction keeps a partially-summarized batch from surfacing mid-write.
func (p *Postgres) UpdateChunkSummaries(ctx context.Context, summaries map[string]string) error {
	if len(summaries) == 0 {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for id, summary := range summaries {
		if _, err := tx.ExecContext(ctx, `UPDATE chunks SET summary=$2 WHERE id=$1`, id, summary); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ChunkPostings returns term -> chunkID -> tf restricted to both the query
// terms and the visible candidate set, so common terms never pull postings
// from other repositories or invisible snapshots.
func (p *Postgres) ChunkPostings(ctx context.Context, terms []string, chunkIDs []string) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	if len(terms) == 0 || len(chunkIDs) == 0 {
		return out, nil
	}
	// Postings live in the rid-keyed chunk_postings table; callers hold text
	// chunk IDs, so the query maps them through chunks. The legacy chunk_terms
	// table was migrated into this one and dropped (v0.1.8/v0.1.9).
	rows, err := p.db.QueryContext(ctx, `SELECT p.term, c.id, p.tf FROM chunks c JOIN chunk_postings p ON p.chunk_rid = c.rid AND p.term = ANY($1) WHERE c.id = ANY($2)`, terms, chunkIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var term, chunkID string
		var tf int
		if err := rows.Scan(&term, &chunkID, &tf); err != nil {
			return nil, err
		}
		if out[term] == nil {
			out[term] = map[string]int{}
		}
		out[term][chunkID] = tf
	}
	return out, rows.Err()
}
// PutEmbeddings routes each vector by key generation: chunks carrying a
// content hash store under (content_hash, model) — shared across every chunk
// with identical (path, text), which is what makes re-uploads of edited files
// reuse vectors — while hash-less chunks (pre-migration data, on-the-fly
// candidates) keep the legacy per-chunk-ID row. The two keyspaces are
// disjoint by construction: a hashed chunk never writes a legacy row.
func (p *Postgres) PutEmbeddings(ctx context.Context, embeddings []domain.ChunkEmbedding) error {
	if len(embeddings) == 0 {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, e := range embeddings {
		if e.ContentHash != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO hash_vectors(content_hash,model_id,dims,vec,sketch) VALUES($1,$2,$3,$4::vector,binary_quantize($4::vector)) ON CONFLICT(content_hash,model_id) DO UPDATE SET dims=excluded.dims,vec=excluded.vec,sketch=excluded.sketch`, e.ContentHash, e.ModelID, e.Dims, formatVector(e.Vector)); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chunk_vectors(chunk_id,model_id,dims,vec,sketch) VALUES($1,$2,$3,$4::vector,binary_quantize($4::vector)) ON CONFLICT(chunk_id,model_id) DO UPDATE SET dims=excluded.dims,vec=excluded.vec,sketch=excluded.sketch`, e.ChunkID, e.ModelID, e.Dims, formatVector(e.Vector)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EmbeddingsByChunkIDs resolves raw vectors dual-track: hash-keyed rows
// joined through the chunks table for hashed chunks, per-chunk-ID rows for
// everything else. While the pgvector migration is still copying, a legacy
// arm covers rows that only exist in the old bytea tables. Scoring should
// use VectorScores; this is for existence checks and the migration-window
// fallback.
func (p *Postgres) EmbeddingsByChunkIDs(ctx context.Context, chunkIDs []string, modelID string) (map[string][]float32, error) {
	out := map[string][]float32{}
	if len(chunkIDs) == 0 {
		return out, nil
	}
	query := `
		SELECT c.id, e.vec::text FROM chunks c JOIN hash_vectors e ON e.content_hash=c.content_hash AND e.model_id=$1 WHERE c.content_hash <> '' AND c.id = ANY($2)
		UNION ALL
		SELECT chunk_id, vec::text FROM chunk_vectors WHERE model_id=$1 AND chunk_id = ANY($2)`
	rows, err := p.db.QueryContext(ctx, query, modelID, chunkIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		out[id] = parseVector(raw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !p.legacyVec.Load() {
		return out, nil
	}
	legacy, err := p.db.QueryContext(ctx, `
		SELECT c.id, e.vector FROM chunks c JOIN hash_embeddings e ON e.content_hash=c.content_hash AND e.model_id=$1 WHERE c.content_hash <> '' AND c.id = ANY($2)
		UNION ALL
		SELECT chunk_id, vector FROM chunk_embeddings WHERE model_id=$1 AND chunk_id = ANY($2)`, modelID, chunkIDs)
	if err != nil {
		return nil, err
	}
	defer legacy.Close()
	for legacy.Next() {
		var id string
		var raw []byte
		if err := legacy.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if _, ok := out[id]; !ok {
			out[id] = decodeVector(raw)
		}
	}
	return out, legacy.Err()
}

// VectorScores computes inner-product similarity inside the database: one
// query vector goes in, scalars come out. Dims filtering keeps the operator
// from erroring on rows embedded under a different dimensionality.
func (p *Postgres) VectorScores(ctx context.Context, chunkIDs []string, modelID string, query []float32) (map[string]float64, error) {
	out := map[string]float64{}
	if len(chunkIDs) == 0 || len(query) == 0 {
		return out, nil
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT c.id, (e.vec <#> $1::vector) * -1 FROM chunks c JOIN hash_vectors e ON e.content_hash=c.content_hash AND e.model_id=$2 AND e.dims=$3 WHERE c.content_hash <> '' AND c.id = ANY($4)
		UNION ALL
		SELECT v.chunk_id, (v.vec <#> $1::vector) * -1 FROM chunk_vectors v WHERE v.model_id=$2 AND v.dims=$3 AND v.chunk_id = ANY($4)`,
		formatVector(query), modelID, len(query), chunkIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var score float64
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		out[id] = score
	}
	return out, rows.Err()
}

func (p *Postgres) BlobPaths(ctx context.Context, names []string) (map[string]string, error) {
	out := make(map[string]string, len(names))
	if len(names) == 0 {
		return out, nil
	}
	rows, err := p.db.QueryContext(ctx, `SELECT name,path FROM blobs WHERE name = ANY($1)`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, path string
		if err := rows.Scan(&name, &path); err != nil {
			return nil, err
		}
		out[name] = path
	}
	return out, rows.Err()
}

func (p *Postgres) BlobChunkCounts(ctx context.Context, names []string) (map[string]int, error) {
	out := map[string]int{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := p.db.QueryContext(ctx, `SELECT blob_name, count(*) FROM chunks WHERE blob_name = ANY($1) GROUP BY blob_name`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

func (p *Postgres) BlobSymbols(ctx context.Context, names []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT blob_name, symbol FROM chunks WHERE blob_name = ANY($1) AND symbol <> ''`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, symbol string
		if err := rows.Scan(&name, &symbol); err != nil {
			return nil, err
		}
		out[name] = append(out[name], symbol)
	}
	return out, rows.Err()
}

// TopVectorBlobs ranks files by their best head chunk's similarity (seq < 4),
// dual-track over the hash-keyed and legacy per-chunk vector tables like
// VectorScores. Selection runs on the binary-quantized sketch (hamming
// distance, ascending = closer): full-precision vectors are TOASTed at ~5.5KB
// each and scanning a big project's worth read ~200MB (6-16s cold, which
// chronically tripped the prefilter's deadline); the inline 128-byte sketch
// reads ~40x less and stays sub-second even cold. Quantization noise is fine
// here — this stage only nominates files, and the pipeline re-scores the
// surviving chunks with full-precision vectors. Rows whose sketch has not
// been backfilled yet are simply not nominated during the backfill window.
func (p *Postgres) TopVectorBlobs(ctx context.Context, names []string, modelID string, query []float32, k int) ([]string, error) {
	if len(names) == 0 || len(query) == 0 || k <= 0 {
		return []string{}, nil
	}
	rows, err := p.db.QueryContext(ctx, `
		WITH q AS (SELECT binary_quantize($1::vector) AS s)
		SELECT blob_name FROM (
			SELECT c.blob_name, MIN(e.sketch <~> (SELECT s FROM q)) AS d
			  FROM chunks c JOIN hash_vectors e ON e.content_hash=c.content_hash AND e.model_id=$2 AND e.dims=$3
			 WHERE e.sketch IS NOT NULL AND c.content_hash <> '' AND c.seq < 4 AND c.blob_name = ANY($4) GROUP BY c.blob_name
			UNION ALL
			SELECT v_c.blob_name, MIN(v.sketch <~> (SELECT s FROM q)) AS d
			  FROM chunks v_c JOIN chunk_vectors v ON v.chunk_id=v_c.id AND v.model_id=$2 AND v.dims=$3
			 WHERE v.sketch IS NOT NULL AND v_c.seq < 4 AND v_c.blob_name = ANY($4) GROUP BY v_c.blob_name
		) t GROUP BY blob_name ORDER BY MIN(d) ASC LIMIT $5`,
		formatVector(query), modelID, len(query), names, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, k)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (p *Postgres) BlobsMissingEmbeddings(ctx context.Context, modelID string, limit int) ([]string, error) {
	query := `SELECT DISTINCT c.blob_name FROM chunks c WHERE
		(c.content_hash='' AND NOT EXISTS (SELECT 1 FROM chunk_vectors cv WHERE cv.chunk_id=c.id AND cv.model_id=$1))
		OR (c.content_hash<>'' AND NOT EXISTS (SELECT 1 FROM hash_vectors hv WHERE hv.content_hash=c.content_hash AND hv.model_id=$1))
		LIMIT $2`
	if p.legacyVec.Load() {
		// Rows still waiting in the old bytea tables must not look missing, or
		// the backfill would pay the embedding API again for every one.
		query = `SELECT DISTINCT c.blob_name FROM chunks c WHERE
		(c.content_hash='' AND NOT EXISTS (SELECT 1 FROM chunk_vectors cv WHERE cv.chunk_id=c.id AND cv.model_id=$1) AND NOT EXISTS (SELECT 1 FROM chunk_embeddings ce WHERE ce.chunk_id=c.id AND ce.model_id=$1))
		OR (c.content_hash<>'' AND NOT EXISTS (SELECT 1 FROM hash_vectors hv WHERE hv.content_hash=c.content_hash AND hv.model_id=$1) AND NOT EXISTS (SELECT 1 FROM hash_embeddings he WHERE he.content_hash=c.content_hash AND he.model_id=$1))
		LIMIT $2`
	}
	rows, err := p.db.QueryContext(ctx, query, modelID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
func (p *Postgres) CreateEvalCase(ctx context.Context, c domain.EvalCase) error {
	paths, _ := json.Marshal(c.ExpectedPaths)
	keywords, _ := json.Marshal(c.ExpectedKeywords)
	_, err := p.db.ExecContext(ctx, `INSERT INTO eval_cases(id,repository_id,query,expected_paths,expected_keywords,created_at) VALUES($1,$2,$3,$4,$5,$6)`, c.ID, c.RepositoryID, c.Query, paths, keywords, c.CreatedAt)
	return err
}
func scanEvalCase(row interface{ Scan(...any) error }) (domain.EvalCase, error) {
	var c domain.EvalCase
	var paths, keywords []byte
	err := row.Scan(&c.ID, &c.RepositoryID, &c.Query, &paths, &keywords, &c.CreatedAt)
	if err != nil {
		return c, mapSQLError(err)
	}
	_ = json.Unmarshal(paths, &c.ExpectedPaths)
	_ = json.Unmarshal(keywords, &c.ExpectedKeywords)
	return c, nil
}
func (p *Postgres) ListEvalCases(ctx context.Context, repoID string) ([]domain.EvalCase, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id,repository_id,query,expected_paths,expected_keywords,created_at FROM eval_cases WHERE repository_id=$1 ORDER BY created_at`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.EvalCase{}
	for rows.Next() {
		c, e := scanEvalCase(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (p *Postgres) DeleteEvalCase(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM eval_cases WHERE id=$1`, id)
	return err
}
func (p *Postgres) EvalCaseByID(ctx context.Context, id string) (domain.EvalCase, error) {
	return scanEvalCase(p.db.QueryRowContext(ctx, `SELECT id,repository_id,query,expected_paths,expected_keywords,created_at FROM eval_cases WHERE id=$1`, id))
}
func (p *Postgres) CreateSnapshot(ctx context.Context, s domain.Snapshot) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Deterministic checkpoint IDs make identical blob sets race on creation;
	// the content is identical by construction, so first writer wins.
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(id,repository_id,parent_id,created_at) VALUES($1,NULLIF($2,''),NULLIF($3,''),$4) ON CONFLICT(id) DO NOTHING`, s.ID, s.RepositoryID, s.ParentID, s.CreatedAt); err != nil {
		return err
	}
	for _, name := range s.BlobNames {
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshot_blobs(snapshot_id,blob_name) VALUES($1,$2) ON CONFLICT DO NOTHING`, s.ID, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (p *Postgres) SnapshotByID(ctx context.Context, id string) (domain.Snapshot, error) {
	var s domain.Snapshot
	var repoID, parentID sql.NullString
	err := p.db.QueryRowContext(ctx, `SELECT id,repository_id,parent_id,created_at FROM snapshots WHERE id=$1`, id).Scan(&s.ID, &repoID, &parentID, &s.CreatedAt)
	if err != nil {
		return s, mapSQLError(err)
	}
	s.RepositoryID, s.ParentID = repoID.String, parentID.String
	rows, err := p.db.QueryContext(ctx, `SELECT blob_name FROM snapshot_blobs WHERE snapshot_id=$1 ORDER BY blob_name`, id)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return s, err
		}
		s.BlobNames = append(s.BlobNames, name)
	}
	return s, rows.Err()
}
func (p *Postgres) LatestSnapshot(ctx context.Context) (domain.Snapshot, error) {
	var id string
	if err := p.db.QueryRowContext(ctx, `SELECT id FROM snapshots ORDER BY created_at DESC LIMIT 1`).Scan(&id); err != nil {
		return domain.Snapshot{}, mapSQLError(err)
	}
	return p.SnapshotByID(ctx, id)
}
func (p *Postgres) SetUserCheckpoint(ctx context.Context, userID, snapshotID string) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO user_checkpoints(user_id,snapshot_id,updated_at) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id, updated_at=EXCLUDED.updated_at`, userID, snapshotID, time.Now())
	return err
}
func (p *Postgres) UserCheckpoint(ctx context.Context, userID string) (string, error) {
	var id string
	if err := p.db.QueryRowContext(ctx, `SELECT snapshot_id FROM user_checkpoints WHERE user_id=$1`, userID).Scan(&id); err != nil {
		return "", mapSQLError(err)
	}
	return id, nil
}
func (p *Postgres) UserOwnsSnapshot(ctx context.Context, userID, snapshotID string) (bool, error) {
	var owned bool
	err := p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_ace_projects WHERE user_id=$1 AND snapshot_id=$2)
		OR EXISTS(SELECT 1 FROM user_checkpoints WHERE user_id=$1 AND snapshot_id=$2)`, userID, snapshotID).Scan(&owned)
	return owned, err
}
func (p *Postgres) SaveACEProject(ctx context.Context, userID, name, snapshotID string) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO user_ace_projects(user_id,project_name,snapshot_id,updated_at) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,project_name) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id, updated_at=EXCLUDED.updated_at`, userID, name, snapshotID, time.Now())
	return err
}
func (p *Postgres) EnsureACEProject(ctx context.Context, userID, name string) (bool, error) {
	r, err := p.db.ExecContext(ctx, `INSERT INTO user_ace_projects(user_id,project_name,snapshot_id,updated_at) VALUES($1,$2,'',$3) ON CONFLICT(user_id,project_name) DO NOTHING`, userID, name, time.Now())
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}
func (p *Postgres) ACEProjectRefs(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT project_name, snapshot_id FROM user_ace_projects WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[string]string{}
	for rows.Next() {
		var name, snapshotID string
		if err := rows.Scan(&name, &snapshotID); err != nil {
			return nil, err
		}
		refs[name] = snapshotID
	}
	return refs, rows.Err()
}
func (p *Postgres) DeleteACEProject(ctx context.Context, userID, name string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM user_ace_projects WHERE user_id=$1 AND project_name=$2`, userID, name)
	return err
}
func (p *Postgres) PurgeACEProject(ctx context.Context, userID, name string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var snapID string
	if err := tx.QueryRowContext(ctx, `SELECT snapshot_id FROM user_ace_projects WHERE user_id=$1 AND project_name=$2`, userID, name).Scan(&snapID); err != nil {
		return mapSQLError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_ace_projects WHERE user_id=$1 AND project_name=$2`, userID, name); err != nil {
		return err
	}
	// ACE activity jobs carry no repository row; the empty repository_id
	// guard keeps legacy repo jobs with a same-named repository alive.
	if _, err := tx.ExecContext(ctx, `DELETE FROM index_jobs WHERE owner_id=$1 AND repository_name=$2 AND repository_id=''`, userID, name); err != nil {
		return err
	}
	// One statement resolves the deletable set and removes it. "doomed" =
	// checkpoint snapshots of this workspace (>=50% blob overlap with the
	// project's snapshot, same coefficient as archival dedupe) minus anything
	// a surviving project row or another user's checkpoint still needs; the
	// caller's own dangling checkpoint pointer is deleted instead (their
	// client then gets 410 and re-uploads from scratch). Blobs are dropped
	// only when no snapshot outside the doomed set and no repository file
	// references them; chunks, postings and embeddings follow via FK cascade.
	// History snapshots that drifted below 50% overlap survive as orphans —
	// they share almost all blobs with newer checkpoints anyway.
	_, err = tx.ExecContext(ctx, `
WITH target AS (
	SELECT blob_name FROM snapshot_blobs WHERE snapshot_id=$1
), tsize AS (
	SELECT count(*) AS n FROM target
), ovl AS (
	-- "ovl" 不能叫 overlaps：OVERLAPS 是 PostgreSQL 保留字，做 CTE 名直接语法错误
	SELECT sb.snapshot_id, count(t.blob_name) AS shared, count(*) AS total
	FROM snapshot_blobs sb LEFT JOIN target t ON t.blob_name=sb.blob_name
	GROUP BY sb.snapshot_id
), doomed AS (
	SELECT s.id FROM snapshots s
	LEFT JOIN ovl o ON o.snapshot_id=s.id
	WHERE coalesce(s.repository_id,'')=''
	  AND (s.id=$1 OR (coalesce(o.shared,0) > 0
	       AND o.shared::float8 / NULLIF(LEAST(o.total,(SELECT n FROM tsize)),0) >= 0.5))
	  AND NOT EXISTS (SELECT 1 FROM user_ace_projects up WHERE up.snapshot_id=s.id)
	  AND NOT EXISTS (SELECT 1 FROM user_checkpoints uc WHERE uc.snapshot_id=s.id AND uc.user_id<>$2)
), dead_blobs AS (
	SELECT DISTINCT sb.blob_name FROM snapshot_blobs sb
	WHERE sb.snapshot_id IN (SELECT id FROM doomed)
	  AND NOT EXISTS (SELECT 1 FROM snapshot_blobs sb2
	       WHERE sb2.blob_name=sb.blob_name AND sb2.snapshot_id NOT IN (SELECT id FROM doomed))
	  AND NOT EXISTS (SELECT 1 FROM repository_files rf WHERE rf.blob_name=sb.blob_name)
), del_ckpt AS (
	DELETE FROM user_checkpoints WHERE user_id=$2 AND snapshot_id IN (SELECT id FROM doomed)
), del_snaps AS (
	DELETE FROM snapshots WHERE id IN (SELECT id FROM doomed)
)
DELETE FROM blobs WHERE name IN (SELECT blob_name FROM dead_blobs)`, snapID, userID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (p *Postgres) ListACEProjects(ctx context.Context, userID, modelID string) ([]domain.ACEProject, error) {
	return p.listACEProjects(ctx, userID, modelID)
}
func (p *Postgres) ListAllACEProjects(ctx context.Context, modelID string) ([]domain.ACEProject, error) {
	return p.listACEProjects(ctx, "", modelID)
}
// snapshotStatsTTL bounds how stale the cached stats of a still-indexing
// snapshot may get; completed snapshots never recompute (they are immutable).
const snapshotStatsTTL = 15 * time.Second

func (p *Postgres) listACEProjects(ctx context.Context, userID, modelID string) ([]domain.ACEProject, error) {
	// Per-snapshot aggregates (file/byte/chunk counts and embedding coverage)
	// are served from the snapshot_stats cache. Snapshots are immutable, so a
	// snapshot whose coverage reached 100% is final and its row never needs
	// recomputing; only snapshots still being embedded refresh, at most once
	// per snapshotStatsTTL. Recomputing everything inline made the project
	// list (and the overview built on it) a 2-second full aggregate per view.
	filter := ``
	args := []any{}
	if userID != "" {
		filter = ` WHERE p.user_id=$1`
		args = append(args, userID)
	}
	rows, err := p.db.QueryContext(ctx, `SELECT p.user_id, u.username, p.project_name, p.snapshot_id, p.updated_at
		FROM user_ace_projects p JOIN users u ON u.id=p.user_id`+filter+` ORDER BY p.updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ACEProject{}
	snapIDs := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var pr domain.ACEProject
		if err := rows.Scan(&pr.OwnerID, &pr.OwnerUsername, &pr.Name, &pr.SnapshotID, &pr.UpdatedAt); err != nil {
			return nil, err
		}
		if pr.SnapshotID != "" && !seen[pr.SnapshotID] {
			seen[pr.SnapshotID] = true
			snapIDs = append(snapIDs, pr.SnapshotID)
		}
		out = append(out, pr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(snapIDs) == 0 {
		return out, nil
	}
	stats, err := p.snapshotStats(ctx, snapIDs, modelID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if st, ok := stats[out[i].SnapshotID]; ok {
			out[i].FileCount, out[i].StorageBytes, out[i].ChunkCount, out[i].EmbeddedCount = int(st.files), st.bytes, int(st.chunks), int(st.embedded)
		}
	}
	return out, nil
}

type snapStat struct {
	files, bytes, chunks, embedded int64
}

// snapshotStats returns cached per-snapshot aggregates, recomputing only the
// snapshots whose cache row is missing or still-incomplete-and-stale.
func (p *Postgres) snapshotStats(ctx context.Context, snapIDs []string, modelID string) (map[string]snapStat, error) {
	out := map[string]snapStat{}
	fresh, err := p.db.QueryContext(ctx, `SELECT snapshot_id, files, bytes, chunks, embedded FROM snapshot_stats
		WHERE model_id=$1 AND snapshot_id = ANY($2) AND (complete OR updated_at > now() - $3::interval)`,
		modelID, snapIDs, fmt.Sprintf("%d seconds", int(snapshotStatsTTL.Seconds())))
	if err != nil {
		return nil, err
	}
	defer fresh.Close()
	for fresh.Next() {
		var id string
		var st snapStat
		if err := fresh.Scan(&id, &st.files, &st.bytes, &st.chunks, &st.embedded); err != nil {
			return nil, err
		}
		out[id] = st
	}
	if err := fresh.Err(); err != nil {
		return nil, err
	}
	stale := make([]string, 0)
	for _, id := range snapIDs {
		if _, ok := out[id]; !ok {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return out, nil
	}
	// storage_bytes measures ciphertext at rest; large files arrive as
	// path#chunkNofM pseudo-path blobs, so the file count collapses that
	// suffix. Aggregated set-based over just the stale snapshots and upserted
	// back into the cache in the same statement.
	recomputed, err := p.db.QueryContext(ctx, `WITH blob_stats AS (
		SELECT sb.snapshot_id,
			count(DISTINCT split_part(b.path,'#',1)) AS files,
			coalesce(sum(octet_length(b.content)),0) AS bytes
		FROM snapshot_blobs sb JOIN blobs b ON b.name=sb.blob_name
		WHERE sb.snapshot_id = ANY($2)
		GROUP BY sb.snapshot_id
	), chunk_stats AS (
		SELECT sb.snapshot_id,
			count(*) AS chunks,
			count(*) FILTER (WHERE cv.chunk_id IS NOT NULL OR hv.content_hash IS NOT NULL) AS embedded
		FROM snapshot_blobs sb
		JOIN chunks c ON c.blob_name=sb.blob_name
		LEFT JOIN chunk_vectors cv ON cv.chunk_id=c.id AND cv.model_id=$1
		LEFT JOIN hash_vectors hv ON c.content_hash<>'' AND hv.content_hash=c.content_hash AND hv.model_id=$1
		WHERE sb.snapshot_id = ANY($2)
		GROUP BY sb.snapshot_id
	)
	INSERT INTO snapshot_stats(snapshot_id, model_id, files, bytes, chunks, embedded, complete, updated_at)
	SELECT s.sid, $1, coalesce(bs.files,0), coalesce(bs.bytes,0), coalesce(cs.chunks,0), coalesce(cs.embedded,0),
		coalesce(cs.embedded,0) >= coalesce(cs.chunks,0), now()
	FROM unnest($2::text[]) AS s(sid)
	LEFT JOIN blob_stats bs ON bs.snapshot_id=s.sid
	LEFT JOIN chunk_stats cs ON cs.snapshot_id=s.sid
	ON CONFLICT (snapshot_id, model_id) DO UPDATE SET files=excluded.files, bytes=excluded.bytes,
		chunks=excluded.chunks, embedded=excluded.embedded, complete=excluded.complete, updated_at=excluded.updated_at
	RETURNING snapshot_id, files, bytes, chunks, embedded`, modelID, stale)
	if err != nil {
		return nil, err
	}
	defer recomputed.Close()
	for recomputed.Next() {
		var id string
		var st snapStat
		if err := recomputed.Scan(&id, &st.files, &st.bytes, &st.chunks, &st.embedded); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, recomputed.Err()
}
func (p *Postgres) RecordACEUsage(ctx context.Context, userID, endpoint string, units int64) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO ace_usage(user_id,day,endpoint,calls,units) VALUES($1,CURRENT_DATE,$2,1,$3) ON CONFLICT(user_id,day,endpoint) DO UPDATE SET calls=ace_usage.calls+1, units=ace_usage.units+EXCLUDED.units`, userID, endpoint, units)
	return err
}
func (p *Postgres) ListACEUsage(ctx context.Context, days int) ([]domain.ACEUsage, error) {
	if days <= 0 {
		days = 30
	}
	rows, err := p.db.QueryContext(ctx, `SELECT user_id,endpoint,SUM(calls),SUM(units),MAX(day) FROM ace_usage WHERE day > CURRENT_DATE - $1::int GROUP BY user_id,endpoint ORDER BY SUM(calls) DESC`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ACEUsage{}
	for rows.Next() {
		var u domain.ACEUsage
		if err := rows.Scan(&u.UserID, &u.Endpoint, &u.Calls, &u.Units, &u.LastDay); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (p *Postgres) ACEUsageDaily(ctx context.Context, userID string, days int) ([]domain.ACEUsageDay, error) {
	if days <= 0 {
		days = 30
	}
	rows, err := p.db.QueryContext(ctx, `SELECT day,endpoint,SUM(calls),SUM(units) FROM ace_usage WHERE day > CURRENT_DATE - $1::int AND ($2::text = '' OR user_id = $2) GROUP BY day,endpoint ORDER BY day,endpoint`, days, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ACEUsageDay{}
	for rows.Next() {
		var u domain.ACEUsageDay
		if err := rows.Scan(&u.Day, &u.Endpoint, &u.Calls, &u.Units); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (p *Postgres) IncQuotaUsage(ctx context.Context, userID, kind string) error {
	return p.AddQuotaUsage(ctx, userID, kind, 1)
}
func (p *Postgres) AddQuotaUsage(ctx context.Context, userID, kind string, n int64) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO user_quota_usage(user_id,day,kind,used) VALUES($1,CURRENT_DATE,$2,$3) ON CONFLICT(user_id,day,kind) DO UPDATE SET used=user_quota_usage.used+$3`, userID, kind, n)
	return err
}
func (p *Postgres) ClearQuotaUsageKind(ctx context.Context, userID, kind string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM user_quota_usage WHERE user_id=$1 AND day=CURRENT_DATE AND kind=$2`, userID, kind)
	return err
}
func (p *Postgres) QuotaUsageToday(ctx context.Context, userID string) (map[string]int64, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT kind,used FROM user_quota_usage WHERE user_id=$1 AND day=CURRENT_DATE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var kind string
		var used int64
		if err := rows.Scan(&kind, &used); err != nil {
			return nil, err
		}
		out[kind] = used
	}
	return out, rows.Err()
}
func (p *Postgres) QuotaUsageTodayAll(ctx context.Context) (map[string]map[string]int64, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT user_id,kind,used FROM user_quota_usage WHERE day=CURRENT_DATE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int64{}
	for rows.Next() {
		var userID, kind string
		var used int64
		if err := rows.Scan(&userID, &kind, &used); err != nil {
			return nil, err
		}
		if out[userID] == nil {
			out[userID] = map[string]int64{}
		}
		out[userID][kind] = used
	}
	return out, rows.Err()
}
func (p *Postgres) ResetQuotaUsage(ctx context.Context, userID string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM user_quota_usage WHERE user_id=$1 AND day=CURRENT_DATE`, userID)
	return err
}
// formatVector renders a float32 slice in pgvector's text input form
// ("[0.1,0.2,...]"); vectors are sent and stored as the pgvector `vector`
// type so similarity is computed inside the database.
func formatVector(v []float32) string {
	var b strings.Builder
	b.Grow(len(v)*10 + 2)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// parseVector reads pgvector's text output form back into a float32 slice.
func parseVector(s string) []float32 {
	s = strings.Trim(strings.TrimSpace(s), "[]")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, _ := strconv.ParseFloat(strings.TrimSpace(p), 32)
		out[i] = float32(f)
	}
	return out
}

// decodeVector reads the legacy bytea float32-LE encoding still present in
// the pre-pgvector tables while the background migration copies them over.
func decodeVector(raw []byte) []float32 {
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}
func (p *Postgres) CreateJob(ctx context.Context, j domain.IndexJob) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO index_jobs(id,repository_id,repository_name,owner_id,revision,status,priority,stage,files_added,files_updated,files_deleted,duration_ms,queue_ms,parse_ms,embedding_ms,write_ms,error,created_at,started_at,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, jobArgs(j)...)
	return err
}
func jobArgs(j domain.IndexJob) []any {
	return []any{j.ID, j.RepositoryID, j.Repository, j.OwnerID, j.Revision, j.Status, j.Priority, j.Stage, j.FilesAdded, j.FilesUpdated, j.FilesDeleted, j.DurationMS, j.QueueMS, j.ParseMS, j.EmbeddingMS, j.WriteMS, j.Error, j.CreatedAt, nullTime(j.StartedAt), nullTime(j.CompletedAt)}
}
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func (p *Postgres) UpdateJob(ctx context.Context, j domain.IndexJob) error {
	_, err := p.db.ExecContext(ctx, `UPDATE index_jobs SET status=$2,stage=$3,files_added=$4,files_updated=$5,files_deleted=$6,duration_ms=$7,queue_ms=$8,parse_ms=$9,embedding_ms=$10,write_ms=$11,error=$12,started_at=$13,completed_at=$14 WHERE id=$1`, j.ID, j.Status, j.Stage, j.FilesAdded, j.FilesUpdated, j.FilesDeleted, j.DurationMS, j.QueueMS, j.ParseMS, j.EmbeddingMS, j.WriteMS, j.Error, nullTime(j.StartedAt), nullTime(j.CompletedAt))
	return err
}
func scanJob(row interface{ Scan(...any) error }) (domain.IndexJob, error) {
	var j domain.IndexJob
	var started, completed sql.NullTime
	err := row.Scan(&j.ID, &j.RepositoryID, &j.Repository, &j.OwnerID, &j.Revision, &j.Status, &j.Priority, &j.Stage, &j.FilesAdded, &j.FilesUpdated, &j.FilesDeleted, &j.DurationMS, &j.QueueMS, &j.ParseMS, &j.EmbeddingMS, &j.WriteMS, &j.Error, &j.CreatedAt, &started, &completed)
	if started.Valid {
		j.StartedAt = started.Time
	}
	if completed.Valid {
		j.CompletedAt = completed.Time
	}
	return j, mapSQLError(err)
}

const jobSelect = `SELECT id,repository_id,repository_name,owner_id,revision,status,priority,stage,files_added,files_updated,files_deleted,duration_ms,queue_ms,parse_ms,embedding_ms,write_ms,error,created_at,started_at,completed_at FROM index_jobs`

func (p *Postgres) JobByID(ctx context.Context, id string) (domain.IndexJob, error) {
	return scanJob(p.db.QueryRowContext(ctx, jobSelect+` WHERE id=$1`, id))
}
func (p *Postgres) ListJobs(ctx context.Context, owner string, all bool, limit int) ([]domain.IndexJob, error) {
	q := jobSelect
	args := []any{}
	if !all {
		q += ` WHERE owner_id=$1`
		args = append(args, owner)
	}
	q += ` ORDER BY created_at DESC`
	if limit > 0 {
		q += ` LIMIT $` + itoa(len(args)+1)
		args = append(args, limit)
	}
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.IndexJob{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func itoa(v int) string {
	const digits = "0123456789"
	if v < 10 {
		return string(digits[v])
	}
	return "10"
}
func (p *Postgres) RecordMetric(ctx context.Context, m domain.MetricPoint) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO metrics(scope,repository_id,name,value,recorded_at) VALUES($1,NULLIF($2,''),$3,$4,$5)`, m.Scope, m.RepositoryID, m.Name, m.Value, m.Timestamp)
	return err
}
func (p *Postgres) Metrics(ctx context.Context, repo, name string, since time.Time, limit int) ([]domain.MetricPoint, error) {
	q := `SELECT scope,COALESCE(repository_id,''),name,value,recorded_at FROM metrics WHERE recorded_at >= $1`
	args := []any{since}
	if repo != "" {
		q += ` AND repository_id=$2`
		args = append(args, repo)
	}
	if name != "" {
		q += ` AND name=$` + itoa(len(args)+1)
		args = append(args, name)
	}
	q += ` ORDER BY recorded_at`
	if limit > 0 {
		q += ` LIMIT $` + itoa(len(args)+1)
		args = append(args, limit)
	}
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.MetricPoint{}
	for rows.Next() {
		var m domain.MetricPoint
		if err := rows.Scan(&m.Scope, &m.RepositoryID, &m.Name, &m.Value, &m.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (p *Postgres) CreateAudit(ctx context.Context, a domain.AuditEvent) error {
	raw, _ := json.Marshal(a.Metadata)
	_, err := p.db.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,actor,action,target_type,target_id,result,metadata,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, a.ID, a.ActorID, a.Actor, a.Action, a.TargetType, a.TargetID, a.Result, raw, a.CreatedAt)
	return err
}
func (p *Postgres) ListAudit(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id,actor_id,actor,action,target_type,target_id,result,metadata,created_at FROM audit_events ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AuditEvent{}
	for rows.Next() {
		var a domain.AuditEvent
		var raw []byte
		if err := rows.Scan(&a.ID, &a.ActorID, &a.Actor, &a.Action, &a.TargetType, &a.TargetID, &a.Result, &raw, &a.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &a.Metadata)
		out = append(out, a)
	}
	return out, rows.Err()
}
func (p *Postgres) CreateAnnouncement(ctx context.Context, a domain.Announcement) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO announcements(id,title,content,author_id,author,created_at) VALUES($1,$2,$3,$4,$5,$6)`, a.ID, a.Title, a.Content, a.AuthorID, a.Author, a.CreatedAt)
	return err
}
func (p *Postgres) ListAnnouncements(ctx context.Context, limit int) ([]domain.Announcement, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id,title,content,author_id,author,created_at FROM announcements ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Announcement{}
	for rows.Next() {
		var a domain.Announcement
		if err := rows.Scan(&a.ID, &a.Title, &a.Content, &a.AuthorID, &a.Author, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (p *Postgres) LatestAnnouncement(ctx context.Context) (domain.Announcement, error) {
	var a domain.Announcement
	err := p.db.QueryRowContext(ctx, `SELECT id,title,content,author_id,author,created_at FROM announcements ORDER BY created_at DESC LIMIT 1`).Scan(&a.ID, &a.Title, &a.Content, &a.AuthorID, &a.Author, &a.CreatedAt)
	return a, mapSQLError(err)
}
func (p *Postgres) DeleteAnnouncement(ctx context.Context, id string) error {
	result, err := p.db.ExecContext(ctx, `DELETE FROM announcements WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
func (p *Postgres) DismissAnnouncement(ctx context.Context, userID, announcementID string) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO announcement_dismissals(user_id,announcement_id,dismissed_at) VALUES($1,$2,now()) ON CONFLICT(user_id) DO UPDATE SET announcement_id=excluded.announcement_id,dismissed_at=excluded.dismissed_at`, userID, announcementID)
	return err
}
func (p *Postgres) DismissedAnnouncement(ctx context.Context, userID string) (string, error) {
	var id string
	err := p.db.QueryRowContext(ctx, `SELECT announcement_id FROM announcement_dismissals WHERE user_id=$1`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func (p *Postgres) GetSettings(ctx context.Context) (map[string]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT key,value FROM system_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
func (p *Postgres) SetSettings(ctx context.Context, values map[string]string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for k, v := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO system_settings(key,value,updated_at) VALUES($1,$2,now()) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const schema = `
CREATE TABLE IF NOT EXISTS users (id text PRIMARY KEY, username text UNIQUE NOT NULL, role text NOT NULL, disabled boolean NOT NULL DEFAULT false, password_hash text NOT NULL, created_at timestamptz NOT NULL, last_active_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (token_hash text PRIMARY KEY, csrf_token text NOT NULL, user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(user_id);
CREATE TABLE IF NOT EXISTS repositories (id text PRIMARY KEY, owner_id text NOT NULL REFERENCES users(id), name text NOT NULL, root_path text NOT NULL, branch text NOT NULL, base_snapshot text NOT NULL, status text NOT NULL, file_count int NOT NULL, chunk_count int NOT NULL, storage_bytes bigint NOT NULL, current_revision bigint NOT NULL, indexed_revision bigint NOT NULL, index_lag_ms bigint NOT NULL, search_p95_ms double precision NOT NULL, embedding_model text NOT NULL, last_synced_at timestamptz NOT NULL, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, paused boolean NOT NULL DEFAULT false);
CREATE INDEX IF NOT EXISTS repositories_owner_idx ON repositories(owner_id);
CREATE TABLE IF NOT EXISTS blobs (name text PRIMARY KEY, path text NOT NULL, content text NOT NULL, created_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS repository_files (repository_id text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE, blob_name text NOT NULL REFERENCES blobs(name), path text NOT NULL, content_hash text NOT NULL, line_count int NOT NULL, language text NOT NULL, updated_at timestamptz NOT NULL, PRIMARY KEY(repository_id,path));
CREATE INDEX IF NOT EXISTS repository_files_blob_idx ON repository_files(blob_name);
CREATE TABLE IF NOT EXISTS index_jobs (id text PRIMARY KEY, repository_id text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE, repository_name text NOT NULL, owner_id text NOT NULL REFERENCES users(id), revision bigint NOT NULL, status text NOT NULL, priority text NOT NULL, stage text NOT NULL, files_added int NOT NULL, files_updated int NOT NULL, files_deleted int NOT NULL, duration_ms bigint NOT NULL, queue_ms bigint NOT NULL, parse_ms bigint NOT NULL, embedding_ms bigint NOT NULL, write_ms bigint NOT NULL, error text NOT NULL, created_at timestamptz NOT NULL, started_at timestamptz, completed_at timestamptz);
CREATE INDEX IF NOT EXISTS index_jobs_owner_idx ON index_jobs(owner_id,created_at DESC);
CREATE TABLE IF NOT EXISTS metrics (id bigserial PRIMARY KEY, scope text NOT NULL, repository_id text REFERENCES repositories(id) ON DELETE CASCADE, name text NOT NULL, value double precision NOT NULL, recorded_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS metrics_lookup_idx ON metrics(repository_id,name,recorded_at DESC);
CREATE INDEX IF NOT EXISTS metrics_name_time_idx ON metrics(name,recorded_at DESC);
CREATE TABLE IF NOT EXISTS audit_events (id text PRIMARY KEY, actor_id text NOT NULL, actor text NOT NULL, action text NOT NULL, target_type text NOT NULL, target_id text NOT NULL, result text NOT NULL, metadata jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS audit_created_idx ON audit_events(created_at DESC);
CREATE TABLE IF NOT EXISTS system_settings (key text PRIMARY KEY, value text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS snapshot_id text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS chunks (id text PRIMARY KEY, blob_name text NOT NULL REFERENCES blobs(name) ON DELETE CASCADE, seq int NOT NULL, symbol text NOT NULL DEFAULT '', symbol_kind text NOT NULL DEFAULT '', start_line int NOT NULL, end_line int NOT NULL, language text NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS chunks_blob_idx ON chunks(blob_name);
CREATE TABLE IF NOT EXISTS snapshots (id text PRIMARY KEY, repository_id text, parent_id text, created_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS snapshot_blobs (snapshot_id text NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE, blob_name text NOT NULL, PRIMARY KEY(snapshot_id,blob_name));
CREATE TABLE IF NOT EXISTS eval_cases (id text PRIMARY KEY, repository_id text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE, query text NOT NULL, expected_paths jsonb NOT NULL DEFAULT '[]', expected_keywords jsonb NOT NULL DEFAULT '[]', created_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS eval_cases_repo_idx ON eval_cases(repository_id,created_at);
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS token_count int NOT NULL DEFAULT 0;
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS summary text NOT NULL DEFAULT '';
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS content_hash text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS chunks_content_hash_idx ON chunks(content_hash) WHERE content_hash <> '';
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE IF NOT EXISTS chunk_vectors (chunk_id text NOT NULL REFERENCES chunks(id) ON DELETE CASCADE, model_id text NOT NULL, dims int NOT NULL, vec vector NOT NULL, PRIMARY KEY(chunk_id,model_id));
CREATE TABLE IF NOT EXISTS hash_vectors (content_hash text NOT NULL, model_id text NOT NULL, dims int NOT NULL, vec vector NOT NULL, PRIMARY KEY(content_hash,model_id));
ALTER TABLE chunk_vectors ADD COLUMN IF NOT EXISTS sketch bit varying;
ALTER TABLE hash_vectors ADD COLUMN IF NOT EXISTS sketch bit varying;
CREATE TABLE IF NOT EXISTS snapshot_stats (snapshot_id text NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE, model_id text NOT NULL, files bigint NOT NULL, bytes bigint NOT NULL, chunks bigint NOT NULL, embedded bigint NOT NULL, complete boolean NOT NULL, updated_at timestamptz NOT NULL, PRIMARY KEY(snapshot_id,model_id));
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS rid bigint GENERATED BY DEFAULT AS IDENTITY;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'chunks_rid_unique') THEN
    ALTER TABLE chunks ADD CONSTRAINT chunks_rid_unique UNIQUE (rid);
  END IF;
END $$;
CREATE TABLE IF NOT EXISTS chunk_postings (chunk_rid bigint NOT NULL REFERENCES chunks(rid) ON DELETE CASCADE, term text NOT NULL, tf int NOT NULL, PRIMARY KEY(chunk_rid,term));
ALTER TABLE users ADD COLUMN IF NOT EXISTS ace_token_hash text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS ace_token_cipher text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS email text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS linuxdo_id bigint NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS linuxdo_username text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS trust_level integer NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS max_output_tokens integer NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS users_email_unique ON users(email) WHERE email <> '';
CREATE UNIQUE INDEX IF NOT EXISTS users_linuxdo_id_unique ON users(linuxdo_id) WHERE linuxdo_id <> 0;
CREATE UNIQUE INDEX IF NOT EXISTS users_ace_token_idx ON users(ace_token_hash) WHERE ace_token_hash <> '';
CREATE TABLE IF NOT EXISTS user_checkpoints (user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, snapshot_id text NOT NULL, updated_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS user_ace_projects (user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, project_name text NOT NULL, snapshot_id text NOT NULL, updated_at timestamptz NOT NULL, PRIMARY KEY(user_id,project_name));
ALTER TABLE index_jobs DROP CONSTRAINT IF EXISTS index_jobs_repository_id_fkey;
CREATE TABLE IF NOT EXISTS ace_usage (user_id text NOT NULL, day date NOT NULL, endpoint text NOT NULL, calls bigint NOT NULL DEFAULT 0, units bigint NOT NULL DEFAULT 0, PRIMARY KEY(user_id,day,endpoint));
CREATE TABLE IF NOT EXISTS user_quota_usage (user_id text NOT NULL, day date NOT NULL, kind text NOT NULL, used bigint NOT NULL DEFAULT 0, PRIMARY KEY(user_id,day,kind));
CREATE TABLE IF NOT EXISTS announcements (id text PRIMARY KEY, title text NOT NULL, content text NOT NULL, author_id text NOT NULL, author text NOT NULL, created_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS announcements_created_idx ON announcements(created_at DESC);
CREATE TABLE IF NOT EXISTS announcement_dismissals (user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, announcement_id text NOT NULL, dismissed_at timestamptz NOT NULL);
`
