package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}
type blob struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// clientState mirrors the server-side checkpoint: the last acknowledged
// checkpoint ID plus the path→blob mapping it covers, so each search only
// uploads and declares the delta instead of the whole workspace.
type clientState struct {
	CheckpointID string            `json:"checkpoint_id"`
	Blobs        map[string]string `json:"blobs"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req rpcRequest
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		if strings.HasPrefix(req.Method, "notifications/") {
			continue
		}
		response := handle(req)
		_ = encoder.Encode(response)
	}
}
func handle(req rpcRequest) rpcResponse {
	result := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		result.Result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "better-context-engine", "version": "0.1.0"}}
	case "tools/list":
		result.Result = map[string]any{"tools": []any{map[string]any{"name": "search_context", "description": "Search the current codebase using Better Context Engine.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"project_root_path": map[string]string{"type": "string"}, "query": map[string]string{"type": "string"}}, "required": []string{"project_root_path", "query"}}}}}
	case "tools/call":
		var params struct {
			Name      string `json:"name"`
			Arguments struct {
				ProjectRoot string `json:"project_root_path"`
				Query       string `json:"query"`
			} `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &params) != nil || params.Name != "search_context" {
			result.Error = map[string]any{"code": -32602, "message": "invalid tool call"}
			break
		}
		text, err := search(params.Arguments.ProjectRoot, params.Arguments.Query)
		if err != nil {
			text = "Error: " + err.Error()
		}
		result.Result = map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}}
	default:
		result.Error = map[string]any{"code": -32601, "message": "method not found"}
	}
	return result
}
func search(root, query string) (string, error) {
	files, err := collect(root)
	if err != nil {
		return "", err
	}
	base := env("BCE_BASE_URL", "http://localhost:18181")
	token := env("ACE_TOKEN", "development-token")
	state := loadState(root)
	formatted, next, err := searchDelta(base, token, query, files, state)
	if err != nil && strings.Contains(err.Error(), "unknown checkpoint") {
		// Server lost our checkpoint (restart, retention): reset and re-upload.
		formatted, next, err = searchDelta(base, token, query, files, clientState{})
	}
	if err != nil {
		return "", err
	}
	_ = saveState(root, next)
	return formatted, nil
}
func searchDelta(base, token, query string, files []blob, state clientState) (string, clientState, error) {
	if state.Blobs == nil {
		state.Blobs = map[string]string{}
	}
	current := make(map[string]string, len(files))
	added := []blob{}
	addedNames := []string{}
	for _, f := range files {
		name := blobName(f.Path, f.Content)
		current[f.Path] = name
		if state.Blobs[f.Path] != name {
			added = append(added, f)
			addedNames = append(addedNames, name)
		}
	}
	currentNames := make(map[string]bool, len(current))
	for _, name := range current {
		currentNames[name] = true
	}
	deleted := []string{}
	for _, oldName := range state.Blobs {
		if !currentNames[oldName] {
			deleted = append(deleted, oldName)
		}
	}
	sort.Strings(addedNames)
	sort.Strings(deleted)
	if len(added) > 0 {
		var uploaded struct {
			Names []string `json:"blob_names"`
		}
		if err := post(base+"/batch-upload", token, map[string]any{"blobs": added}, &uploaded); err != nil {
			return "", state, err
		}
	}
	var checkpoint any
	if state.CheckpointID != "" {
		checkpoint = state.CheckpointID
	}
	payload := map[string]any{"information_request": query, "blobs": map[string]any{"checkpoint_id": checkpoint, "added_blobs": addedNames, "deleted_blobs": deleted}, "dialog": []any{}, "max_output_length": 0, "disable_codebase_retrieval": false, "enable_commit_retrieval": false}
	var response struct {
		Formatted    string `json:"formatted_retrieval"`
		CheckpointID string `json:"checkpoint_id"`
	}
	if err := post(base+"/agents/codebase-retrieval", token, payload, &response); err != nil {
		return "", state, err
	}
	return response.Formatted, clientState{CheckpointID: response.CheckpointID, Blobs: current}, nil
}
func collect(root string) ([]blob, error) {
	out := []blob{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		first := strings.Split(filepath.ToSlash(relative), "/")[0]
		if d.IsDir() && (first == ".git" || first == ".bce" || first == "node_modules" || first == "vendor" || first == "dist" || first == "target") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil || info.Size() > 512*1024 {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !strings.Contains(".go .ts .tsx .js .jsx .py .java .rs .c .h .cpp .md .json .yaml .yml .toml .sql .sh .css .html", ext) {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e == nil && !bytes.Contains(raw, []byte{0}) {
			out = append(out, blob{Path: filepath.ToSlash(relative), Content: string(raw)})
		}
		return nil
	})
	return out, err
}
func loadState(root string) clientState {
	raw, err := os.ReadFile(filepath.Join(root, ".bce", "mcp.json"))
	if err != nil {
		return clientState{Blobs: map[string]string{}}
	}
	var state clientState
	if json.Unmarshal(raw, &state) != nil || state.Blobs == nil {
		state.Blobs = map[string]string{}
	}
	return state
}
func saveState(root string, state clientState) error {
	dir := filepath.Join(root, ".bce")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(state, "", "  ")
	return os.WriteFile(filepath.Join(dir, "mcp.json"), raw, 0600)
}
func post(url, token string, input, output any) error {
	raw, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("%s", body)
	}
	return json.NewDecoder(res.Body).Decode(output)
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func blobName(path, content string) string {
	sum := sha256.Sum256([]byte(path + content))
	return hex.EncodeToString(sum[:])
}
