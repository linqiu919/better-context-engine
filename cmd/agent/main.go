package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type config struct {
	root, server, repoID, repoName, username, password string
	interval                                           time.Duration
}
type manifest struct {
	Hashes map[string]string `json:"hashes"`
}
type fileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func main() {
	var cfg config
	flag.StringVar(&cfg.root, "root", ".", "repository root")
	flag.StringVar(&cfg.server, "server", "http://localhost:18181", "Better Context Engine URL")
	flag.StringVar(&cfg.repoID, "repo-id", "", "existing repository ID")
	flag.StringVar(&cfg.repoName, "repo-name", "", "repository name when creating a new repository")
	flag.StringVar(&cfg.username, "username", os.Getenv("BCE_USERNAME"), "local account username")
	flag.StringVar(&cfg.password, "password", os.Getenv("BCE_PASSWORD"), "local account password")
	flag.DurationVar(&cfg.interval, "interval", 30*time.Second, "full reconciliation interval")
	flag.Parse()
	root, err := filepath.Abs(cfg.root)
	if err != nil {
		log.Fatal(err)
	}
	cfg.root = root
	if cfg.username == "" || cfg.password == "" {
		log.Fatal("username and password are required (flags or BCE_USERNAME/BCE_PASSWORD)")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	csrf, err := login(client, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.repoID == "" {
		cfg.repoID, err = createRepo(client, cfg, csrf)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("created repository %s", cfg.repoID)
	}
	state := loadManifest(root)
	if err := syncNow(client, cfg, csrf, &state); err != nil {
		log.Printf("initial sync: %v", err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()
	if err := addWatches(watcher, root); err != nil {
		log.Printf("watch setup: %v", err)
	}
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	trigger := make(chan struct{}, 1)
	go debounce(trigger, 250*time.Millisecond, func() {
		if err := syncNow(client, cfg, csrf, &state); err != nil {
			log.Printf("sync: %v", err)
		}
	})
	log.Printf("watching %s", root)
	for {
		select {
		case event := <-watcher.Events:
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() && !ignored(root, event.Name) {
					_ = addWatches(watcher, event.Name)
				}
			}
			select {
			case trigger <- struct{}{}:
			default:
			}
		case err := <-watcher.Errors:
			log.Printf("watcher: %v", err)
		case <-ticker.C:
			select {
			case trigger <- struct{}{}:
			default:
			}
		}
	}
}

func login(client *http.Client, cfg config) (string, error) {
	var response struct {
		CSRF string `json:"csrf_token"`
	}
	err := requestJSON(client, "POST", cfg.server+"/api/v1/auth/login", "", map[string]string{"username": cfg.username, "password": cfg.password}, &response)
	return response.CSRF, err
}
func createRepo(client *http.Client, cfg config, csrf string) (string, error) {
	name := cfg.repoName
	if name == "" {
		name = filepath.Base(cfg.root)
	}
	var repo struct {
		ID string `json:"id"`
	}
	err := requestJSON(client, "POST", cfg.server+"/api/v1/repositories", csrf, map[string]string{"name": name, "root_path": cfg.root, "branch": gitValue(cfg.root, "branch", "--show-current")}, &repo)
	return repo.ID, err
}
func syncNow(client *http.Client, cfg config, csrf string, state *manifest) error {
	files, err := scan(cfg.root)
	if err != nil {
		return err
	}
	changed := []fileInput{}
	next := manifest{Hashes: map[string]string{}}
	for path, content := range files {
		sum := sha256.Sum256([]byte(content))
		hash := hex.EncodeToString(sum[:])
		next.Hashes[path] = hash
		if state.Hashes[path] != hash {
			changed = append(changed, fileInput{Path: path, Content: content})
		}
	}
	deleted := []string{}
	for path := range state.Hashes {
		if _, ok := next.Hashes[path]; !ok {
			deleted = append(deleted, path)
		}
	}
	if len(changed) == 0 && len(deleted) == 0 {
		return nil
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	sort.Strings(deleted)
	payload := map[string]any{"branch": gitValue(cfg.root, "branch", "--show-current"), "base_snapshot": gitValue(cfg.root, "rev-parse", "HEAD"), "files": changed, "deleted_paths": deleted, "priority": "realtime"}
	var result map[string]any
	if err := requestJSON(client, "POST", fmt.Sprintf("%s/api/v1/repositories/%s/sync", cfg.server, cfg.repoID), csrf, payload, &result); err != nil {
		return err
	}
	*state = next
	if err := saveManifest(cfg.root, next); err != nil {
		return err
	}
	log.Printf("synced %d changed and %d deleted files", len(changed), len(deleted))
	return nil
}
func requestJSON(client *http.Client, method, url, csrf string, input, output any) error {
	raw, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("%s: %s", res.Status, string(body))
	}
	return json.NewDecoder(res.Body).Decode(output)
}
func scan(root string) (map[string]string, error) {
	paths := gitFiles(root)
	if len(paths) == 0 {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && ignored(root, path) {
				return filepath.SkipDir
			}
			if !d.IsDir() && supported(path) {
				relative, _ := filepath.Rel(root, path)
				paths = append(paths, filepath.ToSlash(relative))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := map[string]string{}
	for _, relative := range paths {
		if ignored(root, filepath.Join(root, relative)) || !supported(relative) {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil || info.Size() > 1024*1024 {
			continue
		}
		raw, err := os.ReadFile(path)
		if err == nil && !bytes.Contains(raw, []byte{0}) {
			out[filepath.ToSlash(relative)] = string(raw)
		}
	}
	return out, nil
}
func gitFiles(root string) []string {
	cmd := exec.Command("git", "-C", root, "ls-files", "-co", "--exclude-standard")
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(raw))
}
func gitValue(root string, args ...string) string {
	full := append([]string{"-C", root}, args...)
	raw, err := exec.Command("git", full...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
func supported(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	allowed := map[string]bool{".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".py": true, ".java": true, ".rs": true, ".c": true, ".h": true, ".cpp": true, ".md": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".sql": true, ".sh": true, ".css": true, ".html": true}
	return allowed[ext] || filepath.Base(path) == "Dockerfile"
}
func ignored(root, path string) bool {
	relative, _ := filepath.Rel(root, path)
	first := strings.Split(filepath.ToSlash(relative), "/")[0]
	return first == ".git" || first == ".bce" || first == "node_modules" || first == "vendor" || first == "dist" || first == "build" || first == "target"
}
func addWatches(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ignored(root, path) && path != root {
				return filepath.SkipDir
			}
			return w.Add(path)
		}
		return nil
	})
}
func debounce(trigger <-chan struct{}, delay time.Duration, fn func()) {
	var timer *time.Timer
	for range trigger {
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(delay, fn)
	}
}
func loadManifest(root string) manifest {
	raw, err := os.ReadFile(filepath.Join(root, ".bce", "index.json"))
	if err != nil {
		return manifest{Hashes: map[string]string{}}
	}
	var state manifest
	if json.Unmarshal(raw, &state) != nil || state.Hashes == nil {
		state.Hashes = map[string]string{}
	}
	return state
}
func saveManifest(root string, state manifest) error {
	dir := filepath.Join(root, ".bce")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	return os.WriteFile(filepath.Join(dir, "index.json"), raw, 0600)
}
