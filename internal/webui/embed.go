package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var assets embed.FS

func Handler() http.Handler {
	root, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			if _, err := fs.Stat(root, request.URL.Path[1:]); err == nil {
				files.ServeHTTP(response, request)
				return
			}
		}
		request.URL.Path = "/"
		files.ServeHTTP(response, request)
	})
}
