package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed templates
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

func Templates() *template.Template {
	root := template.New("")
	err := fs.WalkDir(templateFS, "templates", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		content, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		_, err = root.New(strings.TrimPrefix(path, "templates/")).Parse(string(content))
		return err
	})
	if err != nil {
		panic(err)
	}
	return root
}

func Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServerFS(sub)
}
