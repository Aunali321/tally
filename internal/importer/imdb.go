package importer

import (
	"context"
	"encoding/csv"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

// Episode rows carry only the episode's own id, not the show's, so they are left out.
var imdbTypes = map[string]string{
	"Movie":          store.Movie,
	"TV Movie":       store.Movie,
	"TV Special":     store.Movie,
	"Short":          store.Movie,
	"TV Short":       store.Movie,
	"Video":          store.Movie,
	"TV Series":      store.Show,
	"TV Mini Series": store.Show,
}

type imdbRow map[string]string

func IMDb(ctx context.Context, st *store.Store, uid int64, files fs.FS, opts Options) ([]store.ImportSection, error) {
	r := newRun(ctx, st, uid, opts)
	names, err := fs.Glob(files, "*.csv")
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		header, rows, err := imdbLoad(files, name)
		if err != nil {
			return r.reports, err
		}
		switch {
		case header["Position"]:
			err = imdbList(r, name, rows)
		case header["Your Rating"]:
			err = imdbRatings(r, rows)
		default:
			err = fmt.Errorf("%s: not an IMDb ratings or list export", name)
		}
		if err != nil {
			return r.reports, err
		}
	}
	return r.reports, nil
}

func imdbRatings(r *run, rows []imdbRow) error {
	r.begin("Ratings")
	return importRows(r, rows, func(row imdbRow) (write, error) {
		it, err := row.item()
		if err != nil {
			return nil, err
		}
		rating, err := strconv.Atoi(row["Your Rating"])
		if err == nil {
			err = store.ValidateRating(rating)
		}
		at := row.date("Date Rated")
		return func(b *store.Batch) error { return b.Rate(it, rating, at) }, err
	})
}

func imdbList(r *run, file string, rows []imdbRow) error {
	stem := strings.TrimSuffix(path.Base(file), path.Ext(file))
	slug, name := slugify(stem, "imdb-list"), stem
	if strings.Contains(strings.ToLower(stem), "watchlist") {
		slug, name = "watchlist", "Watchlist"
	}
	return list(r, slug, name, rows, func(row imdbRow) (store.Item, time.Time, error) {
		it, err := row.item()
		return it, row.date("Created"), err
	})
}

func (row imdbRow) item() (store.Item, error) {
	typ, ok := imdbTypes[row["Title Type"]]
	if !ok {
		return store.Item{}, errUnsupported
	}
	year, _ := strconv.Atoi(row["Year"])
	return newItem(typ, row["Const"], 0, store.Meta{Title: row["Title"], Year: year})
}

func (row imdbRow) date(column string) time.Time {
	t, err := time.Parse(time.DateOnly, row[column])
	if err != nil {
		return time.Now()
	}
	return t
}

func imdbLoad(files fs.FS, name string) (map[string]bool, []imdbRow, error) {
	f, err := files.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, err)
	}
	if len(records) == 0 {
		return nil, nil, nil
	}
	columns := records[0]
	columns[0] = strings.TrimPrefix(columns[0], "\ufeff")
	header := make(map[string]bool, len(columns))
	for _, c := range columns {
		header[c] = true
	}
	rows := make([]imdbRow, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := make(imdbRow, len(columns))
		for i, c := range columns {
			if i < len(rec) {
				row[c] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}
