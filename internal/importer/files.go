package importer

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func Open(name string) (files fs.FS, close func() error, err error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, nil, err
	}
	noop := func() error { return nil }
	switch {
	case info.IsDir():
		return os.DirFS(name), noop, nil
	case strings.EqualFold(filepath.Ext(name), ".zip"):
		z, err := zip.OpenReader(name)
		if err != nil {
			return nil, nil, err
		}
		files, err := unwrap(z)
		return files, z.Close, err
	}
	return onlyFile{os.DirFS(filepath.Dir(name)), filepath.Base(name)}, noop, nil
}

func Zip(r io.ReaderAt, size int64) (fs.FS, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	return unwrap(z)
}

// Many exports are zipped inside a single folder.
func unwrap(files fs.FS) (fs.FS, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return files, err
	}
	return fs.Sub(files, entries[0].Name())
}

type onlyFile struct {
	dir  fs.FS
	name string
}

func (f onlyFile) Open(name string) (fs.File, error) {
	if name != f.name {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f.dir.Open(name)
}

func (f onlyFile) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	info, err := fs.Stat(f.dir, f.name)
	if err != nil {
		return nil, err
	}
	return []fs.DirEntry{fs.FileInfoToDirEntry(info)}, nil
}
