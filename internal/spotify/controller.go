package spotify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"quasarbridge/internal/playback"
	"quasarbridge/internal/sound"
)

// Config is the "spotify" object of config.json.
type Config struct {
	Enabled bool `json:"enabled"`
	// TokenFile is written by spotify-login; relative paths are resolved
	// against the directory of config.json.
	TokenFile string `json:"token_file"`
	// Device is the Spotify Connect name of the speaker, as spotifyd or
	// librespot announce it (device_name).
	Device string `json:"device"`
	// Mode is "replace" (all music requests) or "explicit" (only "… в Spotify").
	Mode Mode `json:"mode"`
	// Market is an optional ISO country code for search. Empty (the default)
	// lets Spotify use the account's country. "from_token" needs the
	// user-read-private scope, which spotify-login does not request.
	Market string `json:"market"`
	// Announce speaks "Включаю …" before the music. Off by default: the music
	// itself is the answer.
	Announce bool `json:"announce"`
	// ActiveMinutes is how long after Spotify last played on the speaker
	// "пауза", "дальше", "продолжи" still go to Spotify.
	ActiveMinutes int `json:"active_minutes"`
}

func DefaultConfig() Config {
	return Config{TokenFile: "spotify-token.json", Mode: ModeReplace, ActiveMinutes: 60}
}

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Device) == "" {
		return errors.New("spotify.device must name the speaker's Spotify Connect device")
	}
	if c.TokenFile == "" {
		return errors.New("spotify.token_file must not be empty")
	}
	if c.Mode != ModeReplace && c.Mode != ModeExplicit {
		return errors.New(`spotify.mode must be "replace" or "explicit"`)
	}
	if c.ActiveMinutes < 1 || c.ActiveMinutes > 24*60 {
		return errors.New("spotify.active_minutes must be between 1 and 1440")
	}
	return nil
}

// UserError carries a sentence Alice can say as is.
type UserError struct {
	Message string
	Err     error
}

func (e *UserError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}
func (e *UserError) Unwrap() error       { return e.Err }
func (e *UserError) UserMessage() string { return e.Message }

// Controller implements bridge.SpotifyController.
type Controller struct {
	cfg   Config
	api   *api
	now   func() time.Time
	async func(func())

	mu          sync.Mutex
	deviceID    string
	deviceAt    time.Time
	stateAt     time.Time
	stateOurs   bool
	statePlay   bool
	lastPlaying time.Time
	userID      string
}

// New reads the token file and prepares the Web API client.
func New(cfg Config, configDir string) (*Controller, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	path := cfg.TokenFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(configDir, path)
	}
	tok, err := LoadToken(path)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	return newController(cfg, hc, DefaultAPIURL, DefaultAccountsURL, path, tok, time.Now), nil
}

func newController(cfg Config, hc *http.Client, apiURL, accountsURL, tokenPath string, tok Token, now func() time.Time) *Controller {
	t := &tokens{path: tokenPath, tok: tok, http: hc, accountsURL: accountsURL, now: now}
	return &Controller{cfg: cfg, api: &api{http: hc, base: apiURL, market: cfg.Market, tokens: t}, now: now, async: func(f func()) { go f() }}
}

// Wants reports whether the phrase starts music that Spotify should play.
// Playback controls are never taken here: they go through Active/Control.
func (c *Controller) Wants(text string) bool {
	if playback.Command(text) != "" {
		return false
	}
	_, ok := Parse(text, c.cfg.Mode)
	return ok
}

func (c *Controller) Announce() bool { return c.cfg.Announce }

type target struct {
	contextURI string
	offsetURI  string
	offsetPos  *int
	resume     bool
	title      string
}

func (t target) answer() string {
	if t.resume {
		return "Продолжаю в Spotify."
	}
	return "Включаю " + t.title + "."
}

// Play finds what the phrase asks for and starts it on the speaker.
func (c *Controller) Play(ctx context.Context, text string) (string, error) {
	req, ok := Parse(text, c.cfg.Mode)
	if !ok {
		return "", &UserError{Message: "Не поняла, что включить в Spotify."}
	}
	t, err := c.resolve(ctx, req)
	if err != nil {
		slog.Info("spotify_play", "kind", req.Kind, "found", false)
		return "", err
	}
	if err := c.start(ctx, t); err != nil {
		slog.Warn("spotify_play_failed", "kind", req.Kind, "error", err)
		return "", err
	}
	c.markPlaying(true)
	c.stopAlice(ctx)
	slog.Info("spotify_play", "kind", req.Kind, "explicit", req.Explicit, "found", true)
	return t.answer(), nil
}

// stopAlice stops Alice's own player (Yandex Music, radio) so that it does not
// resume under Spotify after the reply. The directive goes with the reply.
func (c *Controller) stopAlice(ctx context.Context) {
	switch sound.CurrentPlayback(ctx).PlayerState {
	case "Playing", "Paused":
	default:
		return
	}
	if !sound.HasPlayer(ctx) {
		return
	}
	if err := sound.Play(ctx, sound.Request{Command: "stop"}); err != nil {
		slog.Warn("spotify_stop_alice_failed", "error", err)
	}
}

func (c *Controller) resolve(ctx context.Context, req Request) (target, error) {
	switch req.Kind {
	case KindResume:
		return target{resume: true}, nil
	case KindLiked:
		// Liked Songs play as the collection context. A uris list would also
		// work on official apps, but librespot/spotifyd answer 403
		// "Restriction violated" to any play with uris (seen on Mini 3).
		id, err := c.user(ctx)
		if err != nil {
			return target{}, c.userError(err)
		}
		total, err := c.api.savedCount(ctx)
		if err != nil {
			return target{}, c.userError(err)
		}
		if total == 0 {
			return target{}, &UserError{Message: "В Spotify пока нет любимых треков."}
		}
		// Start somewhere random instead of switching the account's shuffle,
		// which would stay on for the next album too.
		pos := rand.IntN(total)
		return target{contextURI: "spotify:user:" + id + ":collection", offsetPos: &pos, title: "ваши любимые треки"}, nil
	case KindPlaylist:
		return c.playlist(ctx, req)
	}
	t, err := c.find(ctx, req, req.Query)
	if errors.Is(err, errNotFound) {
		if stem := Stem(req.Query); stem != "" {
			t, err = c.find(ctx, req, stem)
		}
	}
	if errors.Is(err, errNotFound) {
		return target{}, &UserError{Message: fmt.Sprintf("Не нашла «%s» в Spotify.", req.Query), Err: err}
	}
	if err != nil {
		return target{}, c.userError(err)
	}
	return t, nil
}

var errNotFound = errors.New("nothing found")

func (c *Controller) find(ctx context.Context, req Request, query string) (target, error) {
	switch req.Kind {
	case KindArtist:
		r, err := c.api.search(ctx, query, "artist", 5)
		if err != nil {
			return target{}, err
		}
		if a := pickArtist(r.Artists.Items, query, true); a != nil {
			return artistTarget(a), nil
		}
	case KindAlbum:
		r, err := c.api.search(ctx, strings.TrimSpace(query+" "+req.Artist), "album", 5)
		if err != nil {
			return target{}, err
		}
		if a := pickAlbum(r.Albums.Items, req.Artist); a != nil {
			return target{contextURI: a.URI, title: "альбом «" + a.Name + "»" + by(a.Artists)}, nil
		}
	case KindTrack:
		r, err := c.api.search(ctx, strings.TrimSpace(query+" "+req.Artist), "track", 5)
		if err != nil {
			return target{}, err
		}
		if t := pickTrack(r.Tracks.Items, req.Artist); t != nil {
			return trackTarget(t), nil
		}
	default: // KindAuto
		r, err := c.api.search(ctx, query, "artist,track", 5)
		if err != nil {
			return target{}, err
		}
		if a := pickArtist(r.Artists.Items, query, false); a != nil {
			return artistTarget(a), nil
		}
		if t := pickTrack(r.Tracks.Items, ""); t != nil {
			return trackTarget(t), nil
		}
	}
	return target{}, errNotFound
}

func (c *Controller) playlist(ctx context.Context, req Request) (target, error) {
	own, err := c.api.myPlaylists(ctx)
	if err != nil {
		return target{}, c.userError(err)
	}
	q := compact(req.Query)
	for _, p := range own {
		if SameName(req.Query, p.Name) {
			return target{contextURI: p.URI, title: "плейлист «" + p.Name + "»"}, nil
		}
	}
	for _, p := range own {
		if q != "" && strings.Contains(compact(p.Name), q) {
			return target{contextURI: p.URI, title: "плейлист «" + p.Name + "»"}, nil
		}
	}
	if !req.Own {
		r, err := c.api.search(ctx, req.Query, "playlist", 5)
		if err != nil {
			return target{}, c.userError(err)
		}
		for _, p := range r.Playlists.Items {
			if p != nil && p.URI != "" {
				return target{contextURI: p.URI, title: "плейлист «" + p.Name + "»"}, nil
			}
		}
	}
	return target{}, &UserError{Message: fmt.Sprintf("Не нашла плейлист «%s» в Spotify.", req.Query)}
}

// pickArtist takes the first artist whose name is what was heard. With
// fallback (an explicit "группу X") the top result is used otherwise.
func pickArtist(items []*Artist, heard string, fallback bool) *Artist {
	for _, a := range items {
		if a != nil && SameName(heard, a.Name) {
			return a
		}
	}
	if fallback {
		for _, a := range items {
			if a != nil && a.URI != "" {
				return a
			}
		}
	}
	return nil
}

func pickTrack(items []*Track, artist string) *Track {
	var first *Track
	for _, t := range items {
		// A track starts inside its album: without one there is no context to play.
		if t == nil || t.URI == "" || t.Album.URI == "" || (t.IsPlayable != nil && !*t.IsPlayable) {
			continue
		}
		if artist == "" || hasArtist(t.Artists, artist) {
			return t
		}
		if first == nil {
			first = t
		}
	}
	return first
}

func pickAlbum(items []*Album, artist string) *Album {
	var first *Album
	for _, a := range items {
		if a == nil || a.URI == "" {
			continue
		}
		if artist == "" || hasArtist(a.Artists, artist) {
			return a
		}
		if first == nil {
			first = a
		}
	}
	return first
}

func hasArtist(list []Artist, heard string) bool {
	for _, a := range list {
		if SameName(heard, a.Name) {
			return true
		}
	}
	return false
}

func artistTarget(a *Artist) target { return target{contextURI: a.URI, title: a.Name} }

// A track plays inside its album, so the music continues after it.
func trackTarget(t *Track) target {
	return target{contextURI: t.Album.URI, offsetURI: t.URI, title: "«" + t.Name + "»" + by(t.Artists)}
}

func by(list []Artist) string {
	if len(list) == 0 || list[0].Name == "" {
		return ""
	}
	return " — " + list[0].Name
}

func (c *Controller) start(ctx context.Context, t target) error {
	dev, err := c.device(ctx, false)
	if err != nil {
		return err
	}
	err = c.send(ctx, dev, t)
	if retryOnDevice(err) {
		// The device may have reconnected with a new id, or it is not the
		// account's active player yet: look it up again and transfer first.
		if dev, err = c.device(ctx, true); err != nil {
			return err
		}
		if err = c.api.transfer(ctx, dev, false); err == nil {
			err = c.send(ctx, dev, t)
		}
	}
	if err != nil {
		return c.userError(err)
	}
	return nil
}

func (c *Controller) send(ctx context.Context, dev string, t target) error {
	if t.resume {
		return c.api.transfer(ctx, dev, true)
	}
	body := &playBody{ContextURI: t.contextURI}
	if t.offsetURI != "" || t.offsetPos != nil {
		body.Offset = &playOffset{URI: t.offsetURI, Position: t.offsetPos}
	}
	return c.api.play(ctx, dev, body)
}

func retryOnDevice(err error) bool {
	var e *APIError
	if !errors.As(err, &e) {
		return false
	}
	return e.Status == http.StatusNotFound || e.Status == http.StatusBadGateway || e.Status == http.StatusServiceUnavailable || e.Reason == "NO_ACTIVE_DEVICE"
}

func (c *Controller) userError(err error) error {
	var ue *UserError
	if errors.As(err, &ue) {
		return err
	}
	var e *APIError
	if errors.As(err, &e) {
		switch {
		case e.Reason == "PREMIUM_REQUIRED":
			return &UserError{Message: "Чтобы управлять Spotify с колонки, нужен Premium.", Err: err}
		case e.Status == http.StatusTooManyRequests:
			return &UserError{Message: "Spotify просит подождать. Попробуйте через минуту.", Err: err}
		case e.Status == http.StatusNotFound:
			return &UserError{Message: fmt.Sprintf("Колонка «%s» не отвечает в Spotify. Проверьте spotifyd.", c.cfg.Device), Err: err}
		case e.Status == http.StatusForbidden:
			return &UserError{Message: "Spotify не дал выполнить команду.", Err: err}
		}
	}
	return err
}

// user returns the account id, needed for the Liked Songs context URI.
func (c *Controller) user(ctx context.Context) (string, error) {
	c.mu.Lock()
	id := c.userID
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	id, err := c.api.userID(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.userID = id
	c.mu.Unlock()
	return id, nil
}

const deviceTTL = 10 * time.Minute

func (c *Controller) device(ctx context.Context, refresh bool) (string, error) {
	c.mu.Lock()
	if !refresh && c.deviceID != "" && c.now().Sub(c.deviceAt) < deviceTTL {
		id := c.deviceID
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()
	list, err := c.api.devices(ctx)
	if err != nil {
		return "", c.userError(err)
	}
	id := ""
	for _, d := range list {
		if d.ID != "" && compact(d.Name) == compact(c.cfg.Device) {
			id = d.ID
			break
		}
	}
	if id == "" {
		for _, d := range list {
			if d.ID != "" && SameName(c.cfg.Device, d.Name) {
				id = d.ID
				break
			}
		}
	}
	if id == "" {
		return "", &UserError{Message: fmt.Sprintf("Не вижу колонку «%s» в Spotify. Проверьте, что spotifyd запущен и вошёл в аккаунт.", c.cfg.Device)}
	}
	c.mu.Lock()
	c.deviceID, c.deviceAt = id, c.now()
	c.mu.Unlock()
	return id, nil
}

const stateTTL = 10 * time.Second

// Active reports whether playback controls ("пауза", "дальше") belong to
// Spotify: the speaker is the account's player and it played recently, and
// Alice's own player is not playing right now.
func (c *Controller) Active(ctx context.Context) bool {
	if sound.CurrentPlayback(ctx).PlayerState == "Playing" {
		return false
	}
	ours, playing, err := c.state(ctx)
	if err != nil {
		slog.Warn("spotify_state_failed", "error", err)
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ours && (playing || c.now().Sub(c.lastPlaying) < time.Duration(c.cfg.ActiveMinutes)*time.Minute)
}

func (c *Controller) state(ctx context.Context) (ours, playing bool, err error) {
	c.mu.Lock()
	if c.now().Sub(c.stateAt) < stateTTL {
		ours, playing = c.stateOurs, c.statePlay
		c.mu.Unlock()
		return ours, playing, nil
	}
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	st, err := c.api.player(ctx)
	if err != nil {
		return false, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st != nil {
		ours = (c.deviceID != "" && st.Device.ID == c.deviceID) || SameName(c.cfg.Device, st.Device.Name)
		playing = st.IsPlaying
	}
	c.stateAt, c.stateOurs, c.statePlay = c.now(), ours, ours && playing
	if ours && playing {
		c.lastPlaying = c.now()
	}
	return ours, ours && playing, nil
}

func (c *Controller) markPlaying(playing bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stateAt, c.stateOurs, c.statePlay = c.now(), true, playing
	if playing {
		c.lastPlaying = c.now()
	}
}

// Control runs a playback command from playback.Command on the speaker.
func (c *Controller) Control(ctx context.Context, action string) (string, error) {
	dev, err := c.device(ctx, false)
	if err != nil {
		return "", err
	}
	var answer string
	playing := true
	switch action {
	case "pause", "stop":
		err, answer, playing = c.api.pause(ctx, dev), "Пауза.", false
	case "resume":
		err, answer = c.api.play(ctx, dev, nil), "Продолжаю."
	case "next":
		err, answer = c.api.next(ctx, dev), "Следующий трек."
	case "previous":
		err, answer = c.api.previous(ctx, dev), "Предыдущий трек."
	case "restart":
		err, answer = c.api.seek(ctx, dev, 0), "Сначала."
	default:
		return "", fmt.Errorf("unknown playback control %q", action)
	}
	if err != nil {
		return "", c.userError(err)
	}
	c.markPlaying(playing)
	slog.Info("spotify_control", "action", action)
	return answer, nil
}

// Yield pauses Spotify when a phrase hands the speaker to Alice's own media
// (radio, Yandex Music, fairy tales), so the two do not play at once. It
// never blocks the request.
func (c *Controller) Yield(text string) {
	if !Yields(text) {
		return
	}
	c.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.mu.Lock()
		c.stateAt = time.Time{} // ask Spotify, not the cache
		c.mu.Unlock()
		ours, playing, err := c.state(ctx)
		if err != nil || !ours || !playing {
			return
		}
		if _, err := c.Control(ctx, "pause"); err != nil {
			slog.Warn("spotify_yield_failed", "error", err)
		}
	})
}
