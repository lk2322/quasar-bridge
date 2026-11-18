package spotify

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// APIError is a non-2xx answer of the Web API.
type APIError struct {
	Status  int
	Message string
	Reason  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Spotify Web API %d %s %s", e.Status, e.Reason, e.Message)
}

type Artist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URI  string `json:"uri"`
}
type Album struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	URI     string   `json:"uri"`
	Artists []Artist `json:"artists"`
}
type Track struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	URI        string   `json:"uri"`
	Artists    []Artist `json:"artists"`
	Album      Album    `json:"album"`
	IsPlayable *bool    `json:"is_playable"`
}
type Playlist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URI  string `json:"uri"`
}
type Device struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	IsActive     bool   `json:"is_active"`
	IsRestricted bool   `json:"is_restricted"`
}
type PlayerState struct {
	Device    Device `json:"device"`
	IsPlaying bool   `json:"is_playing"`
}

// Search pages may contain null items (seen for playlists).
type page[T any] struct {
	Items []*T `json:"items"`
}
type searchResult struct {
	Tracks    page[Track]    `json:"tracks"`
	Artists   page[Artist]   `json:"artists"`
	Albums    page[Album]    `json:"albums"`
	Playlists page[Playlist] `json:"playlists"`
}

// playBody always starts a context. No uris: librespot/spotifyd reject any
// play with uris as "Restriction violated".
type playBody struct {
	ContextURI string      `json:"context_uri,omitempty"`
	Offset     *playOffset `json:"offset,omitempty"`
}

// playOffset starts a context at a track (URI) or at an index (Position).
type playOffset struct {
	URI      string `json:"uri,omitempty"`
	Position *int   `json:"position,omitzero"`
}

type api struct {
	http   *http.Client
	base   string
	market string
	tokens *tokens
}

func (a *api) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	u := strings.TrimRight(a.base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		token, err := a.tokens.access(ctx, attempt > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := a.http.Do(req)
		if err != nil {
			return fmt.Errorf("Spotify Web API request failed: %w", err)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			var e struct {
				Error struct {
					Message string `json:"message"`
					Reason  string `json:"reason"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &e)
			return &APIError{Status: resp.StatusCode, Message: e.Error.Message, Reason: e.Error.Reason}
		}
		if out == nil || len(bytes.TrimSpace(raw)) == 0 {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
}

func (a *api) search(ctx context.Context, query, types string, limit int) (searchResult, error) {
	q := url.Values{"q": {query}, "type": {types}, "limit": {strconv.Itoa(limit)}}
	if a.market != "" {
		q.Set("market", a.market)
	}
	var r searchResult
	err := a.do(ctx, http.MethodGet, "/search", q, nil, &r)
	return r, err
}

func (a *api) devices(ctx context.Context) ([]Device, error) {
	var r struct {
		Devices []Device `json:"devices"`
	}
	err := a.do(ctx, http.MethodGet, "/me/player/devices", nil, nil, &r)
	return r.Devices, err
}

// player returns nil when the account plays nothing anywhere (204).
func (a *api) player(ctx context.Context) (*PlayerState, error) {
	var r *PlayerState
	err := a.do(ctx, http.MethodGet, "/me/player", nil, nil, &r)
	return r, err
}

func (a *api) play(ctx context.Context, deviceID string, body *playBody) error {
	var b any
	if body != nil {
		b = body
	}
	return a.do(ctx, http.MethodPut, "/me/player/play", url.Values{"device_id": {deviceID}}, b, nil)
}

func (a *api) transfer(ctx context.Context, deviceID string, play bool) error {
	body := struct {
		DeviceIDs []string `json:"device_ids"`
		Play      bool     `json:"play"`
	}{[]string{deviceID}, play}
	return a.do(ctx, http.MethodPut, "/me/player", nil, body, nil)
}

func (a *api) pause(ctx context.Context, deviceID string) error {
	return a.do(ctx, http.MethodPut, "/me/player/pause", url.Values{"device_id": {deviceID}}, nil, nil)
}

func (a *api) next(ctx context.Context, deviceID string) error {
	return a.do(ctx, http.MethodPost, "/me/player/next", url.Values{"device_id": {deviceID}}, nil, nil)
}

func (a *api) previous(ctx context.Context, deviceID string) error {
	return a.do(ctx, http.MethodPost, "/me/player/previous", url.Values{"device_id": {deviceID}}, nil, nil)
}

func (a *api) seek(ctx context.Context, deviceID string, ms int) error {
	return a.do(ctx, http.MethodPut, "/me/player/seek", url.Values{"device_id": {deviceID}, "position_ms": {strconv.Itoa(ms)}}, nil, nil)
}

// savedCount is the number of tracks in Liked Songs.
func (a *api) savedCount(ctx context.Context) (int, error) {
	var r struct {
		Total int `json:"total"`
	}
	err := a.do(ctx, http.MethodGet, "/me/tracks", url.Values{"limit": {"1"}}, nil, &r)
	return r.Total, err
}

func (a *api) userID(ctx context.Context) (string, error) {
	var r struct {
		ID string `json:"id"`
	}
	if err := a.do(ctx, http.MethodGet, "/me", nil, nil, &r); err != nil {
		return "", err
	}
	if r.ID == "" {
		return "", errors.New("Spotify returned no user id")
	}
	return r.ID, nil
}

func (a *api) myPlaylists(ctx context.Context) ([]Playlist, error) {
	var r page[Playlist]
	err := a.do(ctx, http.MethodGet, "/me/playlists", url.Values{"limit": {"50"}}, nil, &r)
	var out []Playlist
	for _, p := range r.Items {
		if p != nil && p.URI != "" {
			out = append(out, *p)
		}
	}
	return out, err
}
