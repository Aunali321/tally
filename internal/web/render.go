package web

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed templates
var templateFiles embed.FS

type pages map[string]*template.Template

func parsePages() (pages, error) {
	files, err := fs.Glob(templateFiles, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	p := pages{}
	for _, file := range files {
		t, err := template.New("").Funcs(funcs).ParseFS(templateFiles, "templates/*.html", file)
		if err != nil {
			return nil, err
		}
		p[strings.TrimSuffix(path.Base(file), ".html")] = t
	}
	return p, nil
}

// Buffered so a template error never sends half a page.
func (s *server) render(w http.ResponseWriter, status int, page, block string, view any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, block, view); err != nil {
		s.log.Error("render", "page", page, "block", block, "err", err)
		http.Error(w, "Something went wrong. Try again.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (s *server) page(w http.ResponseWriter, page string, view any) {
	s.render(w, http.StatusOK, page, "layout", view)
}
