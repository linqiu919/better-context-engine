// Package web is not a second frontend: it only exists to hold the console's
// build output so it can be compiled into the server binary. The actual
// frontend source lives in ui/; Vite writes its bundle to web/dist (outDir
// ../web/dist), which go:embed below picks up — go:embed cannot reference
// files outside the package directory, hence this container package.
package web

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed dist/*
var assets embed.FS

// Embedded files carry no modification time, so http.FileServer could never
// negotiate 304s — every visit re-downloaded the full ~1MB bundle, and nothing
// was gzipped. Instead the whole dist tree (~1MB) is held in memory with a
// pre-gzipped variant, served with negotiated Content-Encoding plus long-lived
// immutable caching for the hash-named /assets/* files.
type asset struct {
	raw, gz     []byte
	contentType string
}

func loadAssets() map[string]*asset {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	out := map[string]*asset{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		a := &asset{raw: raw, contentType: mime.TypeByExtension(path.Ext(p))}
		if a.contentType == "" {
			a.contentType = http.DetectContentType(raw)
		}
		switch path.Ext(p) {
		case ".js", ".css", ".html", ".svg", ".json", ".txt", ".map":
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(raw)
			zw.Close()
			if buf.Len() < len(raw) {
				a.gz = buf.Bytes()
			}
		}
		out[p] = a
		return nil
	})
	if err != nil {
		panic(err)
	}
	return out
}

func Handler() http.Handler {
	files := loadAssets()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/batch-upload") || strings.HasPrefix(r.URL.Path, "/agents/") {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		a, ok := files[p]
		if !ok { // SPA fallback: unknown paths render the app shell
			p = "index.html"
			a = files[p]
		}
		h := w.Header()
		h.Set("Content-Type", a.contentType)
		if strings.HasPrefix(p, "assets/") {
			// Vite emits content-hashed names; safe to cache forever.
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		body := a.raw
		if a.gz != nil {
			h.Set("Vary", "Accept-Encoding")
			if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				h.Set("Content-Encoding", "gzip")
				body = a.gz
			}
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodHead {
			return
		}
		w.Write(body)
	})
}
