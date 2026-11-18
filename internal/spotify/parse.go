// Package spotify sends the speaker's music requests to a Spotify Connect
// device instead of Yandex Music.
//
// The bridge already holds every voice request until cloud ASR finishes, so a
// recognised music phrase is cancelled in Yandex (local execution) and started
// through the Spotify Web API on the speaker's own Connect device (spotifyd or
// librespot). Alice never starts her own player for these phrases.
package spotify

import (
	"strings"
	"unicode"
)

// Kind is what the phrase asks to play.
type Kind string

const (
	// KindAuto is a bare "включи X": the artist when X names one, otherwise a track.
	KindAuto     Kind = "auto"
	KindTrack    Kind = "track"
	KindAlbum    Kind = "album"
	KindPlaylist Kind = "playlist"
	KindArtist   Kind = "artist"
	// KindLiked plays the user's saved tracks: "включи музыку", "мою волну".
	KindLiked Kind = "liked"
	// KindResume continues the account's last playback on the speaker: "включи Spotify".
	KindResume Kind = "resume"
)

// Mode selects which phrases are taken from Yandex.
type Mode string

const (
	// ModeReplace takes every music request: Spotify replaces Yandex Music.
	ModeReplace Mode = "replace"
	// ModeExplicit takes only phrases that name Spotify.
	ModeExplicit Mode = "explicit"
)

// Request is a parsed music phrase.
type Request struct {
	Kind Kind
	// Query is the text to search for, lower-cased.
	Query string
	// Artist narrows a track or album: "песню X группы Y".
	Artist string
	// Own marks "мой плейлист X": the user's playlists are searched first.
	Own bool
	// Explicit is set when the phrase named Spotify.
	Explicit bool
}

var verbs = [][]string{
	{"найди", "и", "включи"}, {"найди", "и", "поставь"},
	{"хочу", "послушать"}, {"давай", "послушаем"}, {"дай", "послушать"},
	{"можешь", "включить"}, {"можешь", "поставить"}, {"можно", "включить"}, {"можно", "поставить"},
	{"включи"}, {"включите"}, {"включай"}, {"включить"}, {"вруби"}, {"врубай"},
	{"поставь"}, {"поставьте"}, {"ставь"}, {"запусти"}, {"запустите"},
	{"сыграй"}, {"сыграйте"}, {"проиграй"}, {"воспроизведи"}, {"играй"},
}

var (
	leadingFillers = set("алиса", "пожалуйста", "ну", "а", "давай", "слушай", "эй")
	afterVerb      = set("мне", "нам", "ка", "пожалуйста", "сейчас", "тут", "здесь")
	// "включи Кино погромче": the volume part stays with Alice's own controls.
	trailing     = set("пожалуйста", "плиз", "погромче", "потише", "громче", "тише")
	spotifyPreps = set("в", "на", "из", "через", "с", "со", "по")

	likedPhrases = set(
		"музыку", "музычку", "мою музыку", "любимую музыку", "мою любимую музыку",
		"любимые песни", "любимые треки", "мои любимые песни", "мои любимые треки",
		"мои лайки", "лайки", "мои треки", "мои песни", "песни", "треки", "песню", "трек",
		"мою волну", "волну", "мою медиатеку", "медиатеку", "мою библиотеку", "избранное",
		"что нибудь", "что то", "какую нибудь музыку", "музыку какую нибудь", "что нибудь послушать",
	)

	trackMarkers    = set("песню", "песенку", "трек", "трэк", "композицию", "сингл")
	albumMarkers    = set("альбом", "пластинку")
	playlistMarkers = set("плейлист", "подборку")
	// Accusative markers before an artist: "включи группу Кино".
	artistMarkers = set("исполнителя", "артиста", "группу", "певца", "певицу", "рэпера", "репера", "музыканта", "композитора")
	// Plural content of an artist: "включи песни Кино", "музыку Кино".
	artistContent = set("музыку", "песни", "треки", "композиции", "песенки", "хиты")
	// Genitive connectors: "песню X группы Y", "музыку группы Y".
	artistOf   = set("группы", "исполнителя", "артиста", "певца", "певицы", "рэпера", "репера", "музыканта", "композитора")
	ownMarkers = set("мой", "мою", "мои", "моих")
	mediaOf    = set("фильма", "мультфильма", "мультика", "сериала", "игры")

	// Phrases that belong to Alice even in replace mode: devices, timers,
	// radio, fairy tales and similar. A music marker ("песню …") or naming
	// Spotify overrides them.
	reservedWords = set(
		"свет", "светильник", "светильники", "тв", "телевизор", "телек", "бра",
		"шторы", "штору", "шторку", "шторки", "жалюзи",
		"тише", "громче", "погромче", "потише", "звук", "пауза", "паузу",
		"дальше", "назад", "сначала", "заново", "лайк", "дизлайк",
		"радио", "игру", "игра", "шум",
	)
	reservedPrefixes = []string{
		"ламп", "люстр", "розетк", "кондиционер", "термостат", "обогрев", "отоплен",
		"вентилятор", "пылесос", "чайник", "бойлер", "увлажнит", "очистит", "кофемашин",
		"кофеварк", "стиральн", "посудомо", "робот", "утюг", "подсветк", "ночник",
		"гирлянд", "торшер", "прожектор", "сигнализац", "полив",
		"таймер", "будильник", "напомин", "громкост", "блютуз", "блютус", "bluetooth",
		"вайфай", "wifi", "режим", "микрофон", "airplay", "эйрплей",
		"сказк", "подкаст", "аудиокниг", "новост", "мульт", "фильм", "видео", "ютуб",
		"youtube", "сериал", "телепередач", "медитац", "зарядк", "гимнастик",
		"викторин", "квест", "следующ", "предыдущ", "повтор", "перемот",
	}
	natureSounds = set("природы", "дождя", "леса", "моря", "океана", "костра", "грозы", "птиц", "волн", "ветра")
)

// Parse recognises a request to start music. It returns false for anything
// that should stay with Alice: playback controls, devices, timers, radio,
// and any phrase that names Yandex.
func Parse(text string, mode Mode) (Request, bool) {
	w := words(text)
	w, ok := stripVerb(w)
	if !ok {
		return Request{}, false
	}
	for _, t := range w {
		if strings.HasPrefix(t, "яндекс") {
			return Request{}, false
		}
	}
	w, explicit := stripSpotify(w)
	w = trimSet(w, afterVerb, trailing)
	if mode == ModeExplicit && !explicit {
		return Request{}, false
	}
	if len(w) == 0 {
		if explicit {
			return Request{Kind: KindResume, Explicit: true}, true
		}
		return Request{}, false
	}
	if likedPhrases[strings.Join(w, " ")] {
		return Request{Kind: KindLiked, Explicit: explicit}, true
	}
	req, marked := marker(w)
	req.Explicit = explicit
	if !marked && !explicit && reserved(w) {
		return Request{}, false
	}
	if req.Query == "" && req.Kind != KindResume {
		req.Kind, req.Artist, req.Own = KindLiked, "", false
	}
	return req, true
}

var yieldPrefixes = []string{"радио", "сказк", "подкаст", "аудиокниг", "новост"}

// Yields reports whether a phrase that stays with Alice starts her own media:
// Yandex Music by name, radio, fairy tales, podcasts, news. Spotify should
// pause for those so the two do not play over each other.
func Yields(text string) bool {
	w, ok := stripVerb(words(text))
	if !ok || len(w) == 0 {
		return false
	}
	for _, t := range w {
		if strings.HasPrefix(t, "яндекс") {
			return true
		}
	}
	for _, p := range yieldPrefixes {
		if strings.HasPrefix(w[0], p) {
			return true
		}
	}
	return false
}

func marker(w []string) (Request, bool) {
	own := false
	if len(w) > 1 && ownMarkers[w[0]] && (playlistMarkers[w[1]] || isPlayList(w[1:])) {
		own, w = true, w[1:]
	}
	first := w[0]
	switch {
	case isPlayList(w):
		return Request{Kind: KindPlaylist, Query: join(w[2:]), Own: own}, true
	case playlistMarkers[first]:
		return Request{Kind: KindPlaylist, Query: join(w[1:]), Own: own}, true
	case first == "саундтрек":
		return soundtrack(w[1:]), true
	case (trackMarkers[first] || artistContent[first]) && len(w) > 2 && w[1] == "из" && mediaOf[w[2]]:
		return Request{Kind: KindAlbum, Query: join(w[3:])}, true
	case trackMarkers[first]:
		q, artist := splitArtist(w[1:])
		return Request{Kind: KindTrack, Query: q, Artist: artist}, true
	case albumMarkers[first]:
		q, artist := splitArtist(w[1:])
		return Request{Kind: KindAlbum, Query: q, Artist: artist}, true
	case artistMarkers[first]:
		return Request{Kind: KindArtist, Query: join(w[1:])}, true
	case artistContent[first]:
		rest := w[1:]
		if len(rest) > 0 && (artistOf[rest[0]] || rest[0] == "из" || rest[0] == "у" || rest[0] == "от") {
			rest = rest[1:]
		}
		return Request{Kind: KindArtist, Query: join(rest)}, true
	case first == "что" && len(w) > 2 && (w[1] == "нибудь" || w[1] == "то"):
		rest := w[2:]
		if rest[0] == "из" || rest[0] == "у" || rest[0] == "от" {
			rest = rest[1:]
		}
		if len(rest) > 0 && artistOf[rest[0]] {
			rest = rest[1:]
		}
		return Request{Kind: KindArtist, Query: join(rest)}, true
	}
	return Request{Kind: KindAuto, Query: join(w)}, false
}

// "саундтрек к фильму X", "саундтрек из игры X", "саундтрек X".
func soundtrack(w []string) Request {
	if len(w) > 0 && (w[0] == "к" || w[0] == "из") {
		w = w[1:]
	}
	if len(w) > 0 && (mediaOf[w[0]] || strings.HasPrefix(w[0], "фильм") || strings.HasPrefix(w[0], "мульт") || strings.HasPrefix(w[0], "сериал") || strings.HasPrefix(w[0], "игр")) {
		w = w[1:]
	}
	return Request{Kind: KindAlbum, Query: join(w)}
}

func isPlayList(w []string) bool { return len(w) > 1 && w[0] == "плей" && w[1] == "лист" }

// splitArtist cuts "X группы Y" at the last connector.
func splitArtist(w []string) (string, string) {
	for i := len(w) - 2; i > 0; i-- {
		if artistOf[w[i]] {
			return join(w[:i]), join(w[i+1:])
		}
	}
	return join(w), ""
}

func reserved(w []string) bool {
	for i, t := range w {
		if reservedWords[t] {
			return true
		}
		for _, p := range reservedPrefixes {
			if strings.HasPrefix(t, p) {
				return true
			}
		}
		if t == "звуки" && i+1 < len(w) && natureSounds[w[i+1]] {
			return true
		}
	}
	return false
}

func stripVerb(w []string) ([]string, bool) {
	for i := 0; i <= len(w); i++ {
		for _, v := range verbs {
			if hasPrefix(w[i:], v) {
				return trimSet(w[i+len(v):], afterVerb, trailing), true
			}
		}
		if i == len(w) || !leadingFillers[w[i]] {
			return nil, false
		}
	}
	return nil, false
}

func stripSpotify(w []string) ([]string, bool) {
	out := make([]string, 0, len(w))
	found := false
	for _, t := range w {
		if isSpotifyWord(t) {
			found = true
			if n := len(out); n > 0 && spotifyPreps[out[n-1]] {
				out = out[:n-1]
			}
			continue
		}
		out = append(out, t)
	}
	return out, found
}

func isSpotifyWord(t string) bool {
	return strings.HasPrefix(t, "spotif") || strings.HasPrefix(t, "спотиф") || t == "спотик" || t == "спотике"
}

func trimSet(w []string, head, tail map[string]bool) []string {
	for len(w) > 0 && head[w[0]] {
		w = w[1:]
	}
	for len(w) > 0 && tail[w[len(w)-1]] {
		w = w[:len(w)-1]
	}
	return w
}

// words lower-cases the phrase, folds ё and splits on everything that is not
// a letter or digit. Apostrophes are dropped: "guns n' roses" → "guns n roses".
func words(s string) []string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\'' || r == '’' || r == '`':
			return -1
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return r
		}
		return ' '
	}, s)
	return strings.Fields(s)
}

func hasPrefix(w, p []string) bool {
	if len(w) < len(p) {
		return false
	}
	for i := range p {
		if w[i] != p[i] {
			return false
		}
	}
	return true
}

func join(w []string) string { return strings.Join(w, " ") }

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}
