package bridge

import (
	"context"
	"errors"
	"testing"

	"quasarbridge/internal/playback"
	"quasarbridge/internal/sound"
	"quasarbridge/internal/spotify"
)

type fakeSpotify struct {
	wants      func(string) bool
	active     bool
	announce   bool
	playErr    error
	played     []string
	controlled []string
	yielded    []string
}

func (f *fakeSpotify) Wants(text string) bool {
	if f.wants != nil {
		return f.wants(text)
	}
	_, ok := spotify.Parse(text, spotify.ModeReplace)
	return ok && playback.Command(text) == ""
}
func (f *fakeSpotify) Play(_ context.Context, text string) (string, error) {
	f.played = append(f.played, text)
	if f.playErr != nil {
		return "", f.playErr
	}
	return "Включаю КИНО.", nil
}
func (f *fakeSpotify) Active(context.Context) bool { return f.active }
func (f *fakeSpotify) Control(_ context.Context, action string) (string, error) {
	f.controlled = append(f.controlled, action)
	return "Пауза.", nil
}
func (f *fakeSpotify) Yield(text string) { f.yielded = append(f.yielded, text) }
func (f *fakeSpotify) Announce() bool    { return f.announce }

type fakeHome struct{ handled []string }

func (h *fakeHome) Handle(_ context.Context, text string) (string, error) {
	h.handled = append(h.handled, text)
	return "Готово.", nil
}

func TestSpotifyTakesMusicFromYandex(t *testing.T) {
	sp := &fakeSpotify{}
	e := &Engine{Spotify: sp}
	ctx := context.Background()
	// The built-in yandex_services rule matches «песню»; Spotify takes it.
	for _, text := range []string{"Алиса, включи песню Группа крови", "включи Кино", "включи музыку"} {
		if d := e.Decide(ctx, text); d.Route != "spotify" || d.Source != "spotify" {
			t.Errorf("Decide(%q) = %+v", text, d)
		}
	}
	r, err := e.Resolve(ctx, "включи Кино")
	if err != nil {
		t.Fatal(err)
	}
	if r.Route != "spotify" || r.Answer != "Включаю КИНО." || !r.silent || r.keepVoice {
		t.Errorf("resolution = %+v", r)
	}
	if len(sp.played) != 1 || sp.played[0] != "включи Кино" {
		t.Errorf("played = %v", sp.played)
	}
}

func TestSpotifyAnnounceSpeaks(t *testing.T) {
	e := &Engine{Spotify: &fakeSpotify{announce: true}}
	r, err := e.Resolve(context.Background(), "включи Кино")
	if err != nil {
		t.Fatal(err)
	}
	if r.silent || !r.keepVoice {
		t.Errorf("resolution = %+v", r)
	}
}

func TestSpotifyLeavesHomeAndAlicePhrases(t *testing.T) {
	home := &fakeHome{}
	// Even a controller that wants everything does not take a home rule.
	e := &Engine{Spotify: &fakeSpotify{wants: func(string) bool { return true }}, Home: home}
	if d := e.Decide(context.Background(), "включи свет в спальне"); d.Route != "home" {
		t.Errorf("home phrase routed to %+v", d)
	}

	e = &Engine{Spotify: &fakeSpotify{}}
	for _, text := range []string{"включи свет", "поставь таймер на 5 минут", "включи радио", "какая погода"} {
		if d := e.Decide(context.Background(), text); d.Route == "spotify" {
			t.Errorf("Decide(%q) = %+v", text, d)
		}
	}
}

func TestSpotifyControlsOnlyWhileActive(t *testing.T) {
	sp := &fakeSpotify{active: true}
	e := &Engine{Spotify: sp}
	ctx := context.Background()
	if d := e.Decide(ctx, "Алиса, пауза"); d.Route != "spotify" || d.Source != "spotify_control" {
		t.Fatalf("Decide = %+v", d)
	}
	r, err := e.Resolve(ctx, "следующий трек")
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.controlled) != 1 || sp.controlled[0] != "next" || !r.silent {
		t.Errorf("controlled = %v, resolution = %+v", sp.controlled, r)
	}

	sp.active = false
	if d := e.Decide(ctx, "пауза"); d.Route == "spotify" {
		t.Errorf("inactive Spotify took a control: %+v", d)
	}
}

func TestSpotifyUserErrorIsSpoken(t *testing.T) {
	e := &Engine{Spotify: &fakeSpotify{playErr: &spotify.UserError{Message: "Не нашла «абв» в Spotify.", Err: errors.New("nothing found")}}}
	r, err := e.Resolve(context.Background(), "включи абв")
	if err != nil {
		t.Fatal(err)
	}
	if r.Route != "spotify" || r.Answer != "Не нашла «абв» в Spotify." || r.silent {
		t.Errorf("resolution = %+v", r)
	}
}

func TestSpotifyUnexpectedErrorPropagates(t *testing.T) {
	e := &Engine{Spotify: &fakeSpotify{playErr: errors.New("connection reset")}}
	if _, err := e.Resolve(context.Background(), "включи Кино"); err == nil {
		t.Fatal("error swallowed")
	}
}

func TestYandexRouteTellsSpotify(t *testing.T) {
	sp := &fakeSpotify{}
	e := &Engine{Spotify: sp}
	r, err := e.Resolve(context.Background(), "включи радио")
	if err != nil || r.Route != "yandex" {
		t.Fatalf("resolution = %+v, %v", r, err)
	}
	if len(sp.yielded) != 1 || sp.yielded[0] != "включи радио" {
		t.Errorf("yielded = %v", sp.yielded)
	}
}

func TestNoSpotifyKeepsYandexMusic(t *testing.T) {
	if d := (&Engine{}).Decide(context.Background(), "включи песню Группа крови"); d.Route == "spotify" {
		t.Errorf("Decide = %+v", d)
	}
}

func TestSilentFlagsReachReplies(t *testing.T) {
	n := &Native{}
	if !n.hasSilentPlayback(&nativeRequest{forceSilent: true}) {
		t.Error("forceSilent ignored")
	}
	stop, err := makeSoundDirective(sound.Request{Command: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	queued := &nativeRequest{soundRequest: &sound.Request{Command: "stop"}, soundDirective: stop}
	if !n.hasSilentPlayback(queued) {
		t.Error("a queued stop no longer silences the reply")
	}
	queued.keepVoice = true
	if n.hasSilentPlayback(queued) {
		t.Error("keepVoice ignored")
	}
}

func TestNoModelKeepsModelRoutesWithYandex(t *testing.T) {
	e := &Engine{Spotify: &fakeSpotify{}, NoModel: true}
	ctx := context.Background()
	for _, text := range []string{"спроси нейросеть, почему небо голубое", "напиши код сортировки"} {
		if d := e.Decide(ctx, text); d.Route != "yandex" {
			t.Errorf("Decide(%q) = %+v, want yandex", text, d)
		}
	}
	if d := e.Decide(ctx, "включи Кино"); d.Route != "spotify" {
		t.Errorf("Spotify lost in no-model mode: %+v", d)
	}
	if e.IsRefusal("Я пока не умею отвечать на такие вопросы.") {
		t.Error("refusal fallback active without a model")
	}
	if !(&Engine{}).IsRefusal("Я пока не умею отвечать на такие вопросы.") {
		t.Skip("built-in refusal detector does not match this phrase; nothing to compare")
	}
}
