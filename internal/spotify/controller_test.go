package spotify

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"quasarbridge/internal/sound"
)

type call struct {
	method, path, device, body string
	query                      url.Values
}

type fakeSpotify struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []call
	search   map[string]string // "type|q" → JSON body
	devices  string
	player   string // GET /me/player body, "" → 204
	saved    string
	playlist string
	playErr  []int  // statuses returned by successive PUT /me/player/play
	reject   string // access token answered with 401 once
	refresh  int
}

func newFake(t *testing.T) (*fakeSpotify, *httptest.Server) {
	f := &fakeSpotify{t: t, search: map[string]string{},
		devices: `{"devices":[{"id":"phone","name":"iPhone","type":"Smartphone"},{"id":"dev1","name":"Мини 3","type":"Speaker"}]}`}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSpotify) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	if r.URL.Path == "/accounts/api/token" {
		f.refresh++
		form, _ := url.ParseQuery(string(raw))
		if form.Get("grant_type") != "refresh_token" || form.Get("client_id") != "client" || form.Get("refresh_token") == "" {
			http.Error(w, `{"error":"invalid_request"}`, 400)
			return
		}
		io.WriteString(w, `{"access_token":"A2","refresh_token":"R2","expires_in":3600}`)
		return
	}
	if f.reject != "" && r.Header.Get("Authorization") == "Bearer "+f.reject {
		f.reject = ""
		w.WriteHeader(401)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	f.calls = append(f.calls, call{r.Method, path, r.URL.Query().Get("device_id"), string(raw), r.URL.Query()})
	switch {
	case path == "/search":
		key := r.URL.Query().Get("type") + "|" + r.URL.Query().Get("q")
		if body, ok := f.search[key]; ok {
			io.WriteString(w, body)
			return
		}
		io.WriteString(w, `{}`)
	case path == "/me/player/devices":
		io.WriteString(w, f.devices)
	case path == "/me/player" && r.Method == http.MethodGet:
		if f.player == "" {
			w.WriteHeader(204)
			return
		}
		io.WriteString(w, f.player)
	case path == "/me/player/play":
		if len(f.playErr) > 0 {
			status := f.playErr[0]
			f.playErr = f.playErr[1:]
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"status":404,"message":"Player command failed: No active device found","reason":"NO_ACTIVE_DEVICE"}}`)
			return
		}
		w.WriteHeader(204)
	case path == "/me/tracks":
		io.WriteString(w, f.saved)
	case path == "/me":
		io.WriteString(w, `{"id":"user1"}`)
	case path == "/me/playlists":
		io.WriteString(w, cmpOr(f.playlist, `{"items":[]}`))
	default:
		w.WriteHeader(204)
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (f *fakeSpotify) find(method, path string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.method == method && c.path == path {
			out = append(out, c)
		}
	}
	return out
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTest(t *testing.T, cfg Config) (*Controller, *fakeSpotify, *clock, string) {
	f, srv := newFake(t)
	clk := &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "token.json")
	tok := Token{ClientID: "client", AccessToken: "A1", RefreshToken: "R1", ExpiresAt: clk.t.Add(time.Hour)}
	if err := SaveToken(path, tok); err != nil {
		t.Fatal(err)
	}
	if cfg.Device == "" {
		cfg.Device = "Мини 3"
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeReplace
	}
	if cfg.ActiveMinutes == 0 {
		cfg.ActiveMinutes = 60
	}
	c := newController(cfg, srv.Client(), srv.URL+"/v1", srv.URL+"/accounts", path, tok, clk.now)
	c.async = func(f func()) { f() }
	return c, f, clk, path
}

func playBodyOf(t *testing.T, c call) playBody {
	t.Helper()
	var b playBody
	if err := json.Unmarshal([]byte(c.body), &b); err != nil {
		t.Fatalf("play body %q: %v", c.body, err)
	}
	return b
}

func TestPlayArtistByName(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.search["artist,track|кино"] = `{"artists":{"items":[{"name":"КИНО","uri":"spotify:artist:kino"}]},"tracks":{"items":[{"name":"Кукушка","uri":"spotify:track:x","album":{"uri":"spotify:album:x"}}]}}`
	answer, err := c.Play(context.Background(), "Алиса, включи Кино")
	if err != nil {
		t.Fatal(err)
	}
	plays := f.find("PUT", "/me/player/play")
	if len(plays) != 1 || plays[0].device != "dev1" {
		t.Fatalf("plays = %+v", plays)
	}
	if b := playBodyOf(t, plays[0]); b.ContextURI != "spotify:artist:kino" || b.Offset != nil {
		t.Errorf("body = %+v", b)
	}
	if answer != "Включаю КИНО." {
		t.Errorf("answer = %q", answer)
	}
}

func TestPlayTrackInsideAlbum(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.search["artist,track|группа крови"] = `{"artists":{"items":[{"name":"КИНО","uri":"spotify:artist:kino"}]},"tracks":{"items":[{"name":"Группа крови","uri":"spotify:track:gk","artists":[{"name":"КИНО"}],"album":{"uri":"spotify:album:gk"}}]}}`
	answer, err := c.Play(context.Background(), "включи Группа крови")
	if err != nil {
		t.Fatal(err)
	}
	b := playBodyOf(t, f.find("PUT", "/me/player/play")[0])
	if b.ContextURI != "spotify:album:gk" || b.Offset == nil || b.Offset.URI != "spotify:track:gk" || b.Offset.Position != nil {
		t.Errorf("body = %+v", b)
	}
	if answer != "Включаю «Группа крови» — КИНО." {
		t.Errorf("answer = %q", answer)
	}
}

func TestTrackPrefersHeardArtist(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.search["track|yesterday beatles"] = `{"tracks":{"items":[
		{"name":"Yesterday","uri":"spotify:track:cover","artists":[{"name":"Cover Band"}],"album":{"uri":"spotify:album:cover"}},
		{"name":"Yesterday","uri":"spotify:track:orig","artists":[{"name":"The Beatles"}],"album":{"uri":"spotify:album:help"}},
		null]}}`
	if _, err := c.Play(context.Background(), "включи песню Yesterday группы Beatles"); err != nil {
		t.Fatal(err)
	}
	if b := playBodyOf(t, f.find("PUT", "/me/player/play")[0]); b.Offset == nil || b.Offset.URI != "spotify:track:orig" {
		t.Errorf("body = %+v", b)
	}
}

func TestSkipsUnplayableTracks(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.search["track|song"] = `{"tracks":{"items":[
		{"name":"Song","uri":"spotify:track:blocked","is_playable":false,"album":{"uri":"spotify:album:a"}},
		{"name":"Song","uri":"spotify:track:ok","is_playable":true,"album":{"uri":"spotify:album:b"}}]}}`
	if _, err := c.Play(context.Background(), "включи трек song"); err != nil {
		t.Fatal(err)
	}
	if b := playBodyOf(t, f.find("PUT", "/me/player/play")[0]); b.Offset.URI != "spotify:track:ok" {
		t.Errorf("body = %+v", b)
	}
}

func TestStemRetryAfterEmptySearch(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.search["artist,track|маркул"] = `{"artists":{"items":[{"name":"Markul","uri":"spotify:artist:markul"}]}}`
	if _, err := c.Play(context.Background(), "включи Маркула"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.find("GET", "/search")); n != 2 {
		t.Errorf("searches = %d, want 2", n)
	}
	if b := playBodyOf(t, f.find("PUT", "/me/player/play")[0]); b.ContextURI != "spotify:artist:markul" {
		t.Errorf("body = %+v", b)
	}
}

func TestNotFoundIsSpoken(t *testing.T) {
	c, _, _, _ := newTest(t, Config{})
	_, err := c.Play(context.Background(), "включи несуществующую группу")
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.UserMessage(), "Не нашла") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingDevice(t *testing.T) {
	c, f, _, _ := newTest(t, Config{Device: "Кухня"})
	f.search["artist,track|кино"] = `{"artists":{"items":[{"name":"Кино","uri":"spotify:artist:kino"}]}}`
	_, err := c.Play(context.Background(), "включи кино")
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.UserMessage(), "«Кухня»") {
		t.Fatalf("err = %v", err)
	}
	if len(f.find("PUT", "/me/player/play")) != 0 {
		t.Error("played without a device")
	}
}

func TestPlayTransfersWhenNoActiveDevice(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.search["artist,track|кино"] = `{"artists":{"items":[{"name":"Кино","uri":"spotify:artist:kino"}]}}`
	f.playErr = []int{404}
	if _, err := c.Play(context.Background(), "включи кино"); err != nil {
		t.Fatal(err)
	}
	transfers := f.find("PUT", "/me/player")
	if len(transfers) != 1 || !strings.Contains(transfers[0].body, `"dev1"`) || !strings.Contains(transfers[0].body, `"play":false`) {
		t.Errorf("transfers = %+v", transfers)
	}
	if n := len(f.find("PUT", "/me/player/play")); n != 2 {
		t.Errorf("plays = %d, want 2", n)
	}
	if n := len(f.find("GET", "/me/player/devices")); n != 2 {
		t.Errorf("device lookups = %d, want 2 (cached id refreshed)", n)
	}
}

func TestRefreshAfter401KeepsRotatedToken(t *testing.T) {
	c, f, _, path := newTest(t, Config{})
	f.reject = "A1"
	f.search["artist,track|кино"] = `{"artists":{"items":[{"name":"Кино","uri":"spotify:artist:kino"}]}}`
	if _, err := c.Play(context.Background(), "включи кино"); err != nil {
		t.Fatal(err)
	}
	if f.refresh != 1 {
		t.Errorf("refreshes = %d", f.refresh)
	}
	saved, err := LoadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "A2" || saved.RefreshToken != "R2" || saved.ClientID != "client" {
		t.Errorf("saved = %+v", saved)
	}
}

func TestRefreshBeforeExpiry(t *testing.T) {
	c, f, clk, _ := newTest(t, Config{})
	clk.t = clk.t.Add(59*time.Minute + 30*time.Second)
	if _, err := c.Control(context.Background(), "next"); err != nil {
		t.Fatal(err)
	}
	if f.refresh != 1 {
		t.Errorf("refreshes = %d, want 1", f.refresh)
	}
}

// Liked Songs start as the collection context at a random position: spotifyd
// answers 403 "Restriction violated" to any play with uris (seen on Mini 3).
func TestLikedTracks(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.saved = `{"total":3,"items":[{"track":{"uri":"spotify:track:1"}}]}`
	seen := map[int]bool{}
	for i := range 40 {
		answer, err := c.Play(context.Background(), "включи мои треки")
		if err != nil {
			t.Fatal(err)
		}
		if answer != "Включаю ваши любимые треки." {
			t.Errorf("answer = %q", answer)
		}
		call := f.find("PUT", "/me/player/play")[i]
		if strings.Contains(call.body, `"uris"`) {
			t.Fatalf("play with uris: %s", call.body)
		}
		b := playBodyOf(t, call)
		if b.ContextURI != "spotify:user:user1:collection" || b.Offset == nil || b.Offset.Position == nil || b.Offset.URI != "" {
			t.Fatalf("body = %s", call.body)
		}
		if p := *b.Offset.Position; p < 0 || p >= 3 {
			t.Fatalf("position %d outside 3 liked tracks", p)
		}
		seen[*b.Offset.Position] = true
	}
	if len(seen) < 2 {
		t.Errorf("40 starts all at the same position: %v", seen)
	}
	if n := len(f.find("GET", "/me")); n != 1 {
		t.Errorf("user id requests = %d, want 1 (cached)", n)
	}
}

func TestNoLikedTracks(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.saved = `{"total":0,"items":[]}`
	_, err := c.Play(context.Background(), "включи музыку")
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.UserMessage(), "нет любимых") {
		t.Fatalf("err = %v", err)
	}
	if len(f.find("PUT", "/me/player/play")) != 0 {
		t.Error("played an empty collection")
	}
}

func TestTrackWithoutAlbumSkipped(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.search["track|song"] = `{"tracks":{"items":[
		{"name":"Song","uri":"spotify:track:loose"},
		{"name":"Song","uri":"spotify:track:ok","album":{"uri":"spotify:album:b"}}]}}`
	if _, err := c.Play(context.Background(), "включи трек song"); err != nil {
		t.Fatal(err)
	}
	call := f.find("PUT", "/me/player/play")[0]
	if b := playBodyOf(t, call); b.ContextURI != "spotify:album:b" || strings.Contains(call.body, `"uris"`) {
		t.Errorf("body = %s", call.body)
	}
}

func TestOwnPlaylistFirst(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.playlist = `{"items":[{"name":"Работа","uri":"spotify:playlist:work"},{"name":"Дорога домой","uri":"spotify:playlist:road"}]}`
	if _, err := c.Play(context.Background(), "включи плейлист дорога"); err != nil {
		t.Fatal(err)
	}
	if b := playBodyOf(t, f.find("PUT", "/me/player/play")[0]); b.ContextURI != "spotify:playlist:road" {
		t.Errorf("body = %+v", b)
	}
	if len(f.find("GET", "/search")) != 0 {
		t.Error("searched although an own playlist matched")
	}
}

func TestResumeTransfersPlayback(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	answer, err := c.Play(context.Background(), "включи спотифай")
	if err != nil {
		t.Fatal(err)
	}
	tr := f.find("PUT", "/me/player")
	if len(tr) != 1 || !strings.Contains(tr[0].body, `"play":true`) {
		t.Errorf("transfers = %+v", tr)
	}
	if answer != "Продолжаю в Spotify." {
		t.Errorf("answer = %q", answer)
	}
}

func TestStopsAlicePlayer(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.search["artist,track|кино"] = `{"artists":{"items":[{"name":"Кино","uri":"spotify:artist:kino"}]}}`
	var queued []sound.Request
	ctx := sound.WithPlaybackState(context.Background(), sound.PlaybackState{Present: true, PlayerState: "Playing"})
	ctx = sound.WithPlayer(ctx, func(_ context.Context, r sound.Request) error { queued = append(queued, r); return nil })
	if _, err := c.Play(ctx, "включи кино"); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Command != "stop" {
		t.Errorf("queued = %+v", queued)
	}

	queued = nil
	ctx = sound.WithPlaybackState(ctx, sound.PlaybackState{Present: true, PlayerState: "Idle"})
	if _, err := c.Play(ctx, "включи кино"); err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Errorf("stopped an idle player: %+v", queued)
	}
}

func TestPremiumRequired(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.mu.Lock()
	f.devices = `{"devices":[{"id":"dev1","name":"Мини 3"}]}`
	f.mu.Unlock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/devices") {
			f.serve(w, r)
			return
		}
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"status":403,"message":"Player command failed: Premium required","reason":"PREMIUM_REQUIRED"}}`)
	}))
	defer srv.Close()
	c.api.base = srv.URL + "/v1"
	_, err := c.Control(context.Background(), "pause")
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.UserMessage(), "Premium") {
		t.Fatalf("err = %v", err)
	}
}

func TestActive(t *testing.T) {
	c, f, clk, _ := newTest(t, Config{})
	ctx := context.Background()

	if c.Active(ctx) {
		t.Fatal("active with nothing playing")
	}

	clk.t = clk.t.Add(time.Minute)
	f.mu.Lock()
	f.player = `{"is_playing":true,"device":{"id":"dev1","name":"Мини 3"}}`
	f.mu.Unlock()
	if !c.Active(ctx) {
		t.Fatal("not active while playing on the speaker")
	}

	alice := sound.WithPlaybackState(ctx, sound.PlaybackState{Present: true, PlayerState: "Playing"})
	if c.Active(alice) {
		t.Fatal("active while Alice's own player plays")
	}

	// Paused on the speaker 30 minutes later: "продолжи" still goes to Spotify.
	clk.t = clk.t.Add(30 * time.Minute)
	f.mu.Lock()
	f.player = `{"is_playing":false,"device":{"id":"dev1","name":"Мини 3"}}`
	f.mu.Unlock()
	if !c.Active(ctx) {
		t.Fatal("not active 30 minutes after playing")
	}
	clk.t = clk.t.Add(31 * time.Minute)
	if c.Active(ctx) {
		t.Fatal("still active an hour after playing")
	}

	clk.t = clk.t.Add(time.Minute)
	f.mu.Lock()
	f.player = `{"is_playing":true,"device":{"id":"phone","name":"iPhone"}}`
	f.mu.Unlock()
	if c.Active(ctx) {
		t.Fatal("active while Spotify plays on the phone")
	}
}

func TestActiveCachesState(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.player = `{"is_playing":true,"device":{"id":"dev1","name":"Мини 3"}}`
	for range 3 {
		c.Active(context.Background())
	}
	if n := len(f.find("GET", "/me/player")); n != 1 {
		t.Errorf("player requests = %d, want 1", n)
	}
}

func TestControl(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	for action, want := range map[string]call{
		"pause":    {"PUT", "/me/player/pause", "dev1", "", nil},
		"resume":   {"PUT", "/me/player/play", "dev1", "", nil},
		"next":     {"POST", "/me/player/next", "dev1", "", nil},
		"previous": {"POST", "/me/player/previous", "dev1", "", nil},
		"restart":  {"PUT", "/me/player/seek", "dev1", "", nil},
	} {
		if _, err := c.Control(context.Background(), action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		got := f.find(want.method, want.path)
		if len(got) != 1 || got[0].device != "dev1" || got[0].body != "" {
			t.Errorf("%s: calls = %+v", action, got)
		}
	}
}

func TestYieldPausesSpotifyForRadio(t *testing.T) {
	c, f, _, _ := newTest(t, Config{})
	f.player = `{"is_playing":true,"device":{"id":"dev1","name":"Мини 3"}}`
	c.Yield("включи свет")
	if len(f.find("PUT", "/me/player/pause")) != 0 {
		t.Fatal("paused for a non-media phrase")
	}
	c.Yield("включи радио")
	if len(f.find("PUT", "/me/player/pause")) != 1 {
		t.Fatal("did not pause for radio")
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled default: %v", err)
	}
	cfg.Enabled = true
	if cfg.Validate() == nil {
		t.Fatal("enabled without a device")
	}
	cfg.Device = "Мини 3"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "always"
	if cfg.Validate() == nil {
		t.Fatal("accepted an unknown mode")
	}
}

// market=from_token needs user-read-private, which spotify-login does not ask
// for: Spotify answers 403 "Insufficient client scope". Seen on a real account.
func TestSearchOmitsMarketByDefault(t *testing.T) {
	c, f, _, _ := newTest(t, DefaultConfig())
	f.search["artist,track|кино"] = `{"artists":{"items":[{"name":"КИНО","uri":"spotify:artist:kino"}]}}`
	f.saved = `{"total":1,"items":[{"track":{"uri":"spotify:track:1"}}]}`
	if _, err := c.Play(context.Background(), "включи Кино"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Play(context.Background(), "включи музыку"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/search", "/me/tracks"} {
		for _, got := range f.find("GET", path) {
			if got.query.Has("market") {
				t.Errorf("%s sent market=%q", path, got.query.Get("market"))
			}
		}
	}
}
