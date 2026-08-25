package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func Static(webDir string) http.Handler {
	root := http.Dir(webDir)
	files := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(filepath.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")), "/")
		if path == "" || path == "." {
			path = "index.html"
		}
		if info, err := os.Stat(filepath.Join(webDir, path)); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		if strings.Contains(filepath.Base(path), ".") {
			http.NotFound(w, r)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = "/"
		files.ServeHTTP(w, clone)
	})
}
