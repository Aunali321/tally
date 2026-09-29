// Package nuvio mirrors a Nuvio account. Nuvio addons and plugins never see playback, but the apps push every
// update to their backend, which exposes per-profile event feeds.
package nuvio

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Aunali321/tally/internal/store"
)

const DefaultBackend = "https://api.nuvio.tv"

var httpClient = &http.Client{Timeout: 30 * time.Second}

var (
	ErrInvalidLogin   = errors.New("wrong Nuvio email or password")
	ErrSessionExpired = errors.New("the Nuvio session expired, link the account again")
)

type responseError struct {
	method, path string
	status       int
	body         []byte
}

func (e *responseError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %s", e.method, e.path, e.status, http.StatusText(e.status), e.body)
}

func rejected(err error) bool {
	re, ok := errors.AsType[*responseError](err)
	return ok && (re.status == http.StatusBadRequest || re.status == http.StatusUnauthorized)
}

type Client struct {
	backend string
	apiKey  string
}

type Profile struct {
	Index int    `json:"profile_index"`
	Name  string `json:"name"`
}

type discovery struct {
	BackendURL     string `json:"backend_url"`
	PublishableKey string `json:"publishable_key"`
}

type session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

type passwordGrant struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshGrant struct {
	RefreshToken string `json:"refresh_token"`
}

func NewClient(backend, apiKey string) *Client {
	return &Client{backend: strings.TrimRight(backend, "/"), apiKey: apiKey}
}

func Discover(ctx context.Context, backend string) (*Client, error) {
	var d discovery
	if err := NewClient(backend, "").do(ctx, http.MethodGet, "/.well-known/nuvio", "", nil, &d); err != nil {
		return nil, fmt.Errorf("discover %s: %w", backend, err)
	}
	if d.PublishableKey == "" {
		return nil, fmt.Errorf("discover %s: no publishable key", backend)
	}
	return NewClient(cmp.Or(d.BackendURL, backend), d.PublishableKey), nil
}

func (c *Client) SignIn(ctx context.Context, email, password string) (store.NuvioSession, error) {
	s, err := c.grant(ctx, "password", passwordGrant{email, password})
	if rejected(err) {
		return s, ErrInvalidLogin
	}
	return s, err
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (store.NuvioSession, error) {
	s, err := c.grant(ctx, "refresh_token", refreshGrant{refreshToken})
	if rejected(err) {
		return s, ErrSessionExpired
	}
	return s, err
}

func (c *Client) Profiles(ctx context.Context, token string) ([]Profile, error) {
	var profiles []Profile
	return profiles, c.rpc(ctx, token, "sync_pull_profiles", struct{}{}, &profiles)
}

func (c *Client) grant(ctx context.Context, grantType string, body any) (store.NuvioSession, error) {
	var s session
	if err := c.do(ctx, http.MethodPost, "/auth/v1/token?grant_type="+grantType, "", body, &s); err != nil {
		return store.NuvioSession{}, err
	}
	if s.AccessToken == "" || s.RefreshToken == "" {
		return store.NuvioSession{}, errors.New("sign-in returned no session")
	}
	return store.NuvioSession{AccessToken: s.AccessToken, RefreshToken: s.RefreshToken, ExpiresAt: time.Unix(s.ExpiresAt, 0)}, nil
}

func (c *Client) rpc(ctx context.Context, token, function string, params, out any) error {
	return c.do(ctx, http.MethodPost, "/rest/v1/rpc/"+function, token, params, out)
}

func (c *Client) do(ctx context.Context, method, path, token string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.backend+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("apikey", c.apiKey)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &responseError{method, path, resp.StatusCode, bytes.TrimSpace(body)}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
