package spotify

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultAccountsURL = "https://accounts.spotify.com"
	DefaultAPIURL      = "https://api.spotify.com/v1"
	// Scopes are what spotify-login asks for: playback control on the
	// speaker, saved tracks for "включи музыку" and the user's playlists.
	Scopes = "user-read-playback-state user-modify-playback-state user-library-read playlist-read-private playlist-read-collaborative"
)

// Token is the stored sign-in of a PKCE client: no client secret exists, the
// refresh token alone renews access. Spotify may rotate the refresh token on
// every renewal, so the file is rewritten each time.
type Token struct {
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"`
}

func LoadToken(path string) (Token, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Token{}, fmt.Errorf("cannot read Spotify token file: %w", err)
	}
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return Token{}, errors.New("Spotify token file is not valid JSON")
	}
	if t.ClientID == "" || t.RefreshToken == "" {
		return Token{}, errors.New("Spotify token file has no client_id or refresh_token: run spotify-login")
	}
	return t, nil
}

// SaveToken replaces the file atomically with owner-only permissions.
func SaveToken(path string, t Token) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spotify-token-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Exchange trades a PKCE authorization code for tokens.
func Exchange(ctx context.Context, hc *http.Client, accountsURL, clientID, code, redirectURI, verifier string) (Token, error) {
	return requestToken(ctx, hc, accountsURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}, Token{ClientID: clientID}, time.Now())
}

var errSignInExpired = &UserError{Message: "Вход в Spotify больше не действует. Войдите заново командой spotify-login."}

func requestToken(ctx context.Context, hc *http.Client, accountsURL string, form url.Values, prev Token, now time.Time) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(accountsURL, "/")+"/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("Spotify accounts request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, err
	}
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
	}
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode != http.StatusOK || r.AccessToken == "" {
		if r.Error == "invalid_grant" {
			return Token{}, errSignInExpired
		}
		return Token{}, fmt.Errorf("Spotify accounts answered %d %s", resp.StatusCode, r.Error)
	}
	t := prev
	t.AccessToken = r.AccessToken
	t.ExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	if r.RefreshToken != "" {
		t.RefreshToken = r.RefreshToken
	}
	if r.Scope != "" {
		t.Scope = r.Scope
	}
	return t, nil
}

type tokens struct {
	mu          sync.Mutex
	path        string
	tok         Token
	http        *http.Client
	accountsURL string
	now         func() time.Time
}

// access returns a valid access token, renewing it a minute before expiry or
// when force is set (after a 401).
func (s *tokens) access(ctx context.Context, force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && s.tok.AccessToken != "" && s.now().Before(s.tok.ExpiresAt.Add(-time.Minute)) {
		return s.tok.AccessToken, nil
	}
	t, err := requestToken(ctx, s.http, s.accountsURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {s.tok.RefreshToken},
		"client_id":     {s.tok.ClientID},
	}, s.tok, s.now())
	if err != nil {
		return "", err
	}
	s.tok = t
	if s.path != "" {
		if err := SaveToken(s.path, t); err != nil {
			// The new token still works in memory; a rotated refresh token
			// that is not on disk is lost on restart, so say so loudly.
			slog.Error("spotify_token_save_failed", "error", err)
		}
	}
	return t.AccessToken, nil
}
