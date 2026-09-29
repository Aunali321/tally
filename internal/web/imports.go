package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/importer"
	"github.com/Aunali321/tally/internal/store"
)

const (
	maxUpload     = 512 << 20
	uploadTimeout = 10 * time.Minute
)

type source struct {
	Key, Name, Desc, Accept string
}

var sourceList = []source{
	{"trakt", "Trakt", "Watched history, progress, ratings, watchlist and lists from a Trakt data export (.zip).", ".zip"},
	{"simkl", "Simkl", "Watched history, ratings and lists from a Simkl backup (.zip).", ".zip,.json"},
	{"imdb", "IMDb", "Ratings, watchlist and lists from the .csv files IMDb exports. Add one or several.", ".csv,.zip"},
	{"stremio", "Stremio", "Your library, what you watched and where you stopped.", ""},
	{"mdblist", "MDBList", "Watched history, ratings, watchlist and lists.", ""},
}

type sourceView struct {
	source
	Run   *store.ImportRun
	Error string
}

func (s *server) sources(r *http.Request, uid int64) ([]sourceView, error) {
	latest, err := s.store.LatestImports(r.Context(), uid)
	if err != nil {
		return nil, err
	}
	views := make([]sourceView, len(sourceList))
	for i, src := range sourceList {
		views[i].source = src
		if run, ok := latest[src.Key]; ok {
			views[i].Run = &run
		}
	}
	return views, nil
}

func (s *server) importStatus(w http.ResponseWriter, r *http.Request, uid int64) {
	views, err := s.sources(r, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, v := range views {
		if v.Key == r.PathValue("source") {
			s.render(w, http.StatusOK, "settings", "source", v)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *server) startImport(w http.ResponseWriter, r *http.Request, uid int64) {
	key := r.PathValue("source")
	views, err := s.sources(r, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var view *sourceView
	for i := range views {
		if views[i].Key == key {
			view = &views[i]
		}
	}
	if view == nil {
		http.NotFound(w, r)
		return
	}
	if view.Run != nil && view.Run.Running() {
		s.importResult(w, r, uid, *view, "This import is already running.")
		return
	}
	label, run, err := s.prepareImport(w, r, uid, view.source)
	if bad, ok := errors.AsType[badRequest](err); ok {
		s.importResult(w, r, uid, *view, string(bad))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id, err := s.store.StartImport(r.Context(), uid, key, label)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	go s.runImport(id, run)
	s.importResult(w, r, uid, *view, "")
}

func (s *server) importResult(w http.ResponseWriter, r *http.Request, uid int64, view sourceView, problem string) {
	status := http.StatusOK
	if problem != "" {
		status = http.StatusUnprocessableEntity
	}
	if !isHTMX(r) {
		if problem == "" {
			redirect(w, r, "/settings#"+view.Key)
			return
		}
		page, err := s.settingsView(r, uid)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for i := range page.Sources {
			if page.Sources[i].Key == view.Key {
				page.Sources[i].Error = problem
			}
		}
		s.render(w, status, "settings", "layout", page)
		return
	}
	views, err := s.sources(r, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, v := range views {
		if v.Key == view.Key {
			v.Error = problem
			s.render(w, status, "settings", "source", v)
		}
	}
}

type importJob func(ctx context.Context, opts importer.Options) error

func (s *server) prepareImport(w http.ResponseWriter, r *http.Request, uid int64, src source) (string, importJob, error) {
	switch src.Key {
	case "stremio":
		email, password := strings.TrimSpace(r.PostFormValue("email")), r.PostFormValue("password")
		if email == "" || password == "" {
			return "", nil, badRequest("Enter your Stremio email and password.")
		}
		return email, func(ctx context.Context, opts importer.Options) error {
			_, err := importer.Stremio(ctx, s.store, uid, email, password, opts)
			return err
		}, nil
	case "mdblist":
		key := strings.TrimSpace(r.PostFormValue("key"))
		if key == "" {
			return "", nil, badRequest("Enter your MDBList API key.")
		}
		return "API key", func(ctx context.Context, opts importer.Options) error {
			_, err := importer.MDBList(ctx, s.store, uid, key, opts)
			return err
		}, nil
	}
	dir, names, err := saveUploads(w, r)
	if err != nil {
		return "", nil, err
	}
	fromFiles := map[string]func(context.Context, *store.Store, int64, fs.FS, importer.Options) ([]store.ImportSection, error){
		"trakt": importer.Trakt,
		"simkl": importer.Simkl,
		"imdb":  importer.IMDb,
	}[src.Key]
	return strings.Join(names, ", "), func(ctx context.Context, opts importer.Options) error {
		defer os.RemoveAll(dir)
		path := dir
		if len(names) == 1 && strings.EqualFold(filepath.Ext(names[0]), ".zip") {
			path = filepath.Join(dir, names[0])
		}
		files, closeFiles, err := importer.Open(path)
		if err != nil {
			return err
		}
		defer closeFiles()
		_, err = fromFiles(ctx, s.store, uid, files, opts)
		return err
	}, nil
}

func saveUploads(w http.ResponseWriter, r *http.Request) (string, []string, error) {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(uploadTimeout)
	if err := errors.Join(rc.SetReadDeadline(deadline), rc.SetWriteDeadline(deadline)); err != nil {
		return "", nil, err
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return "", nil, badRequest("Choose the export files to import.")
	}
	uploads := r.MultipartForm.File["files"]
	if len(uploads) == 0 {
		return "", nil, badRequest("Choose the export files to import.")
	}
	dir, err := os.MkdirTemp("", "tally-import-")
	if err != nil {
		return "", nil, err
	}
	names := make([]string, len(uploads))
	for i, u := range uploads {
		names[i] = filepath.Base(u.Filename)
		if err := saveUpload(u, filepath.Join(dir, names[i])); err != nil {
			os.RemoveAll(dir)
			return "", nil, err
		}
	}
	return dir, names, nil
}

func saveUpload(u *multipart.FileHeader, dst string) error {
	src, err := u.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	return errors.Join(err, f.Close())
}

func (s *server) runImport(id int64, job importJob) {
	ctx := s.background
	record := context.WithoutCancel(ctx)
	opts := importer.Options{
		Starting: func(section string) {
			if err := s.store.ImportProgress(record, id, section); err != nil {
				s.log.Error("import progress", "import", id, "err", err)
			}
		},
		Finished: func(sec store.ImportSection) {
			if err := s.store.AddImportSection(record, id, sec); err != nil {
				s.log.Error("import section", "import", id, "err", err)
			}
		},
	}
	failure := ""
	if err := job(ctx, opts); err != nil {
		s.log.Error("import failed", "import", id, "err", err)
		failure = importFailure(err)
	}
	if err := s.store.FinishImport(record, id, failure); err != nil {
		s.log.Error("finish import", "import", id, "err", err)
	}
}

func importFailure(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "Stopped because the server shut down. Run it again."
	case errors.Is(err, fs.ErrNotExist):
		return "The upload does not look like this tracker's export."
	}
	return fmt.Sprintf("The import stopped: %v.", err)
}
