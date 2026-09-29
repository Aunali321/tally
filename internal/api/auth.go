package api

import (
	"cmp"
	"errors"
	"net/http"
	"strings"

	"github.com/Aunali321/tally/internal/store"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type deviceRequest struct {
	Name string `json:"name"`
}

type tokenResponse struct {
	Token string `json:"token"`
}

type deviceResponse struct {
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
	ExpiresIn  int    `json:"expires_in"`
	Interval   int    `json:"interval"`
}

type deviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

type meResponse struct {
	Email string `json:"email"`
}

func (s *server) login(w http.ResponseWriter, r *http.Request) error {
	req, err := decodeJSON[loginRequest](r)
	if err != nil {
		return err
	}
	uid, err := s.store.Authenticate(r.Context(), req.Email, req.Password)
	if errors.Is(err, store.ErrBadCredentials) {
		return &statusError{http.StatusUnauthorized, err.Error()}
	}
	if err != nil {
		return err
	}
	token, err := s.store.IssueToken(r.Context(), uid, cmp.Or(strings.TrimSpace(req.Name), "App"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, tokenResponse{token})
	return nil
}

func (s *server) startDevice(w http.ResponseWriter, r *http.Request) error {
	req, err := decodeOptionalJSON[deviceRequest](r)
	if err != nil {
		return err
	}
	d, err := s.store.StartDevice(r.Context(), cmp.Or(strings.TrimSpace(req.Name), "TV"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, deviceResponse{
		DeviceCode: d.Code,
		UserCode:   store.FormatUserCode(d.UserCode),
		ExpiresIn:  int(store.DeviceTTL.Seconds()),
		Interval:   int(store.DevicePollInterval.Seconds()),
	})
	return nil
}

func (s *server) claimDevice(w http.ResponseWriter, r *http.Request) error {
	req, err := decodeJSON[deviceTokenRequest](r)
	if err != nil {
		return err
	}
	token, err := s.store.ClaimDevice(r.Context(), req.DeviceCode)
	switch {
	case errors.Is(err, store.ErrPending):
		return &statusError{http.StatusPreconditionRequired, err.Error()}
	case errors.Is(err, store.ErrNotFound):
		return &statusError{http.StatusGone, "device code expired or unknown"}
	case err != nil:
		return err
	}
	writeJSON(w, http.StatusOK, tokenResponse{token})
	return nil
}

func (s *server) revoke(w http.ResponseWriter, r *http.Request, _ int64) error {
	token, _ := bearer(r)
	if err := s.store.RevokeToken(r.Context(), token); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *server) me(w http.ResponseWriter, r *http.Request, uid int64) error {
	email, err := s.store.Email(r.Context(), uid)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, meResponse{email})
	return nil
}
