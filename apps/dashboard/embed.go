package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/*
var content embed.FS

func Handler() http.Handler {
	sub, _ := fs.Sub(content, "web")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
	})
}
