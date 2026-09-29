// Package importer brings watch history from other trackers into tally. Entries without an IMDb or TMDB id are
// counted rather than failing the import, since tally has no catalog to identify them.
package importer

import (
	"cmp"
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

var (
	errNoID        = errors.New("no imdb or tmdb id")
	errUnsupported = errors.New("unsupported entry")
)

type Options struct {
	Starting func(section string)
	Finished func(store.ImportSection)
}

type write func(*store.Batch) error

type run struct {
	ctx     context.Context
	store   *store.Store
	uid     int64
	opts    Options
	section string
	reports []store.ImportSection
}

func newRun(ctx context.Context, st *store.Store, uid int64, opts Options) *run {
	return &run{ctx: ctx, store: st, uid: uid, opts: opts}
}

func (r *run) steps(steps ...func() error) ([]store.ImportSection, error) {
	for _, step := range steps {
		if err := step(); err != nil {
			return r.reports, err
		}
	}
	return r.reports, nil
}

func (r *run) begin(section string) {
	r.section = section
	if r.opts.Starting != nil {
		r.opts.Starting(section)
	}
}

func (r *run) finish(report store.ImportSection) {
	r.reports = append(r.reports, report)
	if r.opts.Finished != nil {
		r.opts.Finished(report)
	}
}

type playKey struct {
	key string
	at  time.Time
}

func plays[R any](r *run, rows []R, convert func(R) (store.Item, time.Time, error)) error {
	counts := map[playKey]int{}
	return importRows(r, rows, func(row R) (write, error) {
		it, at, err := convert(row)
		if err != nil {
			return nil, err
		}
		k := playKey{it.Key(), at}
		counts[k]++
		n := counts[k]
		return func(b *store.Batch) error { return b.AddPlays(it, at, n) }, nil
	})
}

func list[R any](r *run, slug, name string, rows []R, convert func(R) (store.Item, time.Time, error)) error {
	r.begin(name)
	if err := r.store.Write(r.ctx, r.uid, func(b *store.Batch) error { return b.PutList(slug, name) }); err != nil {
		return err
	}
	return importRows(r, rows, func(row R) (write, error) {
		it, at, err := convert(row)
		return func(b *store.Batch) error { return b.AddToList(slug, it, at) }, err
	})
}

func importRows[R any](r *run, rows []R, convert func(R) (write, error)) error {
	report := store.ImportSection{Name: r.section}
	var writes []write
	for _, row := range rows {
		w, err := convert(row)
		switch {
		case errors.Is(err, errNoID):
			report.NoID++
		case err != nil:
			report.Skipped++
		default:
			writes = append(writes, w)
		}
	}
	report.Imported = len(writes)
	if len(writes) > 0 {
		err := r.store.Write(r.ctx, r.uid, func(b *store.Batch) error {
			for _, w := range writes {
				if err := w(b); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	r.finish(report)
	return nil
}

func newItem(typ, imdb string, tmdb int64, meta store.Meta) (store.Item, error) {
	it := store.Item{Type: typ, ID: imdb, Meta: &meta}
	switch {
	case imdb != "":
	case tmdb != 0:
		it.ID = "tmdb:" + strconv.FormatInt(tmdb, 10)
	default:
		return store.Item{}, errNoID
	}
	return it, it.Validate()
}

func episodeItem(show store.Item, season, episode int) (store.Item, error) {
	show.Season, show.Episode = &season, &episode
	return show, show.Validate()
}

func slugify(s, fallback string) string {
	return cmp.Or(store.SlugFor(s), fallback)
}

func minutes(n int64) int64 {
	return (time.Duration(n) * time.Minute).Milliseconds()
}
