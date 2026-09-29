package nuvio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

const (
	PollInterval  = time.Minute
	refreshMargin = time.Hour
)

type ProfileError struct {
	Profile  int
	Profiles []Profile
}

func (e *ProfileError) Error() string {
	return fmt.Sprintf("profile %d not found, the account has: %s", e.Profile, describe(e.Profiles))
}

type Result struct {
	Applied int
	Skipped int
}

func SignIn(ctx context.Context, st *store.Store, uid int64, backend, email, password string) ([]Profile, error) {
	c, err := Discover(ctx, backend)
	if err != nil {
		return nil, err
	}
	session, err := c.SignIn(ctx, email, password)
	if err != nil {
		return nil, err
	}
	profiles, err := c.Profiles(ctx, session.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	link := store.NuvioLink{UserID: uid, Backend: c.backend, APIKey: c.apiKey, Email: email, Session: session}
	if err := st.SaveNuvioLink(ctx, link); err != nil {
		return nil, err
	}
	if len(profiles) == 1 {
		return profiles, st.ChooseNuvioProfile(ctx, uid, profiles[0].Index, profiles[0].Name)
	}
	return profiles, nil
}

func Profiles(ctx context.Context, st *store.Store, link store.NuvioLink) ([]Profile, error) {
	c := NewClient(link.Backend, link.APIKey)
	token, err := accessToken(ctx, st, c, link)
	if err != nil {
		return nil, err
	}
	return c.Profiles(ctx, token)
}

func ChooseProfile(ctx context.Context, st *store.Store, uid int64, index int) (Profile, error) {
	link, err := st.NuvioLink(ctx, uid)
	if err != nil {
		return Profile{}, err
	}
	profiles, err := Profiles(ctx, st, link)
	if err != nil {
		return Profile{}, err
	}
	i := slices.IndexFunc(profiles, func(p Profile) bool { return p.Index == index })
	if i < 0 {
		return Profile{}, &ProfileError{index, profiles}
	}
	return profiles[i], st.ChooseNuvioProfile(ctx, uid, index, profiles[i].Name)
}

func Sync(ctx context.Context, st *store.Store, link store.NuvioLink) (Result, error) {
	res, err := syncEvents(ctx, st, link)
	status := link.Status
	if err == nil {
		status = store.NuvioStatus{SyncedAt: time.Now()}
	} else if ctx.Err() == nil {
		status.Error, status.SessionExpired = err.Error(), errors.Is(err, ErrSessionExpired)
	}
	return res, errors.Join(err, st.SaveNuvioStatus(context.WithoutCancel(ctx), link.UserID, status))
}

func syncEvents(ctx context.Context, st *store.Store, link store.NuvioLink) (Result, error) {
	c := NewClient(link.Backend, link.APIKey)
	token, err := accessToken(ctx, st, c, link)
	if err != nil {
		return Result{}, err
	}
	progress, err := pull[progressEvent](ctx, c, token, progressFeed, link.Profile, link.Cursors.Progress)
	if err != nil {
		return Result{}, err
	}
	watched, err := pull[watchedEvent](ctx, c, token, watchedFeed, link.Profile, link.Cursors.Watched)
	if err != nil {
		return Result{}, err
	}
	library, err := pull[libraryEvent](ctx, c, token, libraryFeed, link.Profile, link.Cursors.Library)
	if err != nil {
		return Result{}, err
	}
	if len(progress)+len(watched)+len(library) == 0 {
		return Result{}, nil
	}

	next := store.NuvioCursors{
		Progress: cursor(progress, link.Cursors.Progress),
		Watched:  cursor(watched, link.Cursors.Watched),
		Library:  cursor(library, link.Cursors.Library),
	}
	var res Result
	err = st.Write(ctx, link.UserID, func(b *store.Batch) error {
		if err := apply(b, progress, &res); err != nil {
			return err
		}
		if err := apply(b, watched, &res); err != nil {
			return err
		}
		if err := apply(b, library, &res); err != nil {
			return err
		}
		return b.AdvanceNuvioCursors(link.Cursors, next, res.Applied)
	})
	if errors.Is(err, store.ErrConflict) {
		return Result{}, nil
	}
	return res, err
}

func Run(ctx context.Context, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		syncAll(ctx, st, log)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func syncAll(ctx context.Context, st *store.Store, log *slog.Logger) {
	links, err := st.NuvioLinks(ctx)
	if err != nil {
		log.Error("nuvio: load links", "err", err)
		return
	}
	for _, link := range links {
		res, err := Sync(ctx, st, link)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			log.Error("nuvio sync failed", "user", link.UserID, "err", err)
		case res.Applied > 0:
			log.Info("nuvio sync", "user", link.UserID, "applied", res.Applied, "skipped", res.Skipped)
		}
	}
}

func apply[E feedEvent](b *store.Batch, events []E, res *Result) error {
	for _, e := range events {
		ok, err := e.apply(b)
		if err != nil {
			return err
		}
		if ok {
			res.Applied++
		} else {
			res.Skipped++
		}
	}
	return nil
}

func accessToken(ctx context.Context, st *store.Store, c *Client, link store.NuvioLink) (string, error) {
	if time.Until(link.Session.ExpiresAt) > refreshMargin {
		return link.Session.AccessToken, nil
	}
	session, err := c.Refresh(ctx, link.Session.RefreshToken)
	if err != nil {
		return "", err
	}
	return session.AccessToken, st.SaveNuvioSession(ctx, link.UserID, session)
}

func describe(profiles []Profile) string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = fmt.Sprintf("%d (%s)", p.Index, p.Name)
	}
	return strings.Join(names, ", ")
}
