package spotify

import "testing"

func TestParseMusic(t *testing.T) {
	cases := []struct {
		text string
		want Request
	}{
		{"Алиса, включи Кино", Request{Kind: KindAuto, Query: "кино"}},
		{"поставь Daft Punk", Request{Kind: KindAuto, Query: "daft punk"}},
		{"включи мне, пожалуйста, Земфиру", Request{Kind: KindAuto, Query: "земфиру"}},
		{"включи Кино погромче", Request{Kind: KindAuto, Query: "кино"}},
		{"включи Светлану Лободу", Request{Kind: KindAuto, Query: "светлану лободу"}},
		{"включи Звуки Му", Request{Kind: KindAuto, Query: "звуки му"}},
		{"включи Алису", Request{Kind: KindAuto, Query: "алису"}},
		{"включи AC/DC", Request{Kind: KindAuto, Query: "ac dc"}},
		{"включи Guns N' Roses", Request{Kind: KindAuto, Query: "guns n roses"}},
		{"хочу послушать Сплин", Request{Kind: KindAuto, Query: "сплин"}},
		{"давай послушаем Аквариум", Request{Kind: KindAuto, Query: "аквариум"}},
		{"ну включи-ка Чайф", Request{Kind: KindAuto, Query: "чайф"}},
		{"включи песню Группа крови", Request{Kind: KindTrack, Query: "группа крови"}},
		{"поставь песню группа крови группы кино", Request{Kind: KindTrack, Query: "группа крови", Artist: "кино"}},
		{"включи трек Get Lucky исполнителя Daft Punk", Request{Kind: KindTrack, Query: "get lucky", Artist: "daft punk"}},
		{"включи альбом Звезда по имени Солнце", Request{Kind: KindAlbum, Query: "звезда по имени солнце"}},
		{"поставь альбом Discovery группы Daft Punk", Request{Kind: KindAlbum, Query: "discovery", Artist: "daft punk"}},
		{"включи плейлист для бега", Request{Kind: KindPlaylist, Query: "для бега"}},
		{"включи плей лист утро", Request{Kind: KindPlaylist, Query: "утро"}},
		{"включи мой плейлист дорога", Request{Kind: KindPlaylist, Query: "дорога", Own: true}},
		{"включи группу Кино", Request{Kind: KindArtist, Query: "кино"}},
		{"включи песни Кино", Request{Kind: KindArtist, Query: "кино"}},
		{"включи музыку группы Кино", Request{Kind: KindArtist, Query: "кино"}},
		{"включи что-нибудь из Кино", Request{Kind: KindArtist, Query: "кино"}},
		{"включи музыку из фильма Интерстеллар", Request{Kind: KindAlbum, Query: "интерстеллар"}},
		{"включи саундтрек к фильму Интерстеллар", Request{Kind: KindAlbum, Query: "интерстеллар"}},
		{"включи музыку", Request{Kind: KindLiked}},
		{"включи мою волну", Request{Kind: KindLiked}},
		{"поставь что-нибудь", Request{Kind: KindLiked}},
		{"включи мои лайки", Request{Kind: KindLiked}},
		{"включи плейлист", Request{Kind: KindLiked}},
		{"включи Кино в Спотифай", Request{Kind: KindAuto, Query: "кино", Explicit: true}},
		{"включи в спотифае Daft Punk", Request{Kind: KindAuto, Query: "daft punk", Explicit: true}},
		{"поставь на Spotify песню Yesterday", Request{Kind: KindTrack, Query: "yesterday", Explicit: true}},
		{"включи Spotify", Request{Kind: KindResume, Explicit: true}},
		{"включи в спотифай радио гага", Request{Kind: KindAuto, Query: "радио гага", Explicit: true}},
		{"включи музыку в спотифай", Request{Kind: KindLiked, Explicit: true}},
	}
	for _, c := range cases {
		got, ok := Parse(c.text, ModeReplace)
		if !ok || got != c.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", c.text, got, ok, c.want)
		}
	}
}

// Everything here must stay with Alice in the default replace mode.
func TestParseLeavesAlicePhrases(t *testing.T) {
	for _, text := range []string{
		"включи свет", "включи свет на кухне", "Алиса, включи на кухне свет", "выключи свет",
		"включи телевизор", "включи кондиционер", "включи робот-пылесос", "включи лампу в спальне",
		"поставь таймер на 5 минут", "поставь будильник на семь утра", "поставь напоминание",
		"включи радио", "включи радио Рекорд", "включи сказку", "включи подкаст", "включи новости",
		"включи звуки природы", "включи белый шум", "включи звук", "включи погромче", "включи тише",
		"поставь на паузу", "пауза", "дальше", "включи следующий трек", "включи предыдущую песню",
		"поставь лайк", "включи блютуз", "включи режим не беспокоить", "включи мультики",
		"включи Кино в Яндекс Музыке", "включи Яндекс Музыку", "поставь Кино на повтор",
		"какая погода", "расскажи анекдот", "спроси нейросеть, что послушать", "Кино",
		"включи", "поставь пожалуйста",
	} {
		if got, ok := Parse(text, ModeReplace); ok {
			t.Errorf("Parse(%q) = %+v, want no match", text, got)
		}
	}
}

func TestParseExplicitMode(t *testing.T) {
	if _, ok := Parse("включи Кино", ModeExplicit); ok {
		t.Error("explicit mode took a phrase without Spotify")
	}
	if _, ok := Parse("включи музыку", ModeExplicit); ok {
		t.Error("explicit mode took «включи музыку»")
	}
	got, ok := Parse("включи Кино в спотифай", ModeExplicit)
	if !ok || got.Query != "кино" || !got.Explicit {
		t.Errorf("explicit phrase: %+v, %v", got, ok)
	}
}

func TestYields(t *testing.T) {
	for text, want := range map[string]bool{
		"включи радио":            true,
		"включи Яндекс Музыку":    true,
		"включи Кино в Яндексе":   true,
		"включи сказку про репку": true,
		"включи свет":             false,
		"какая погода":            false,
		"включи Кино":             false,
	} {
		if got := Yields(text); got != want {
			t.Errorf("Yields(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestSameName(t *testing.T) {
	for _, c := range []struct {
		heard, name string
		want        bool
	}{
		{"кино", "КИНО", true},
		{"daft punk", "Daft Punk", true},
		{"дафт панк", "Daft Punk", true},
		{"beatles", "The Beatles", true},
		{"ac dc", "AC/DC", true},
		{"земфира", "Земфира", true},
		{"кино", "Кишлак", false},
		{"сплин", "Splean", false},
		{"мот", "Мот", true},
		{"мот", "Макс Корж", false},
	} {
		if got := SameName(c.heard, c.name); got != c.want {
			t.Errorf("SameName(%q, %q) = %v, want %v", c.heard, c.name, got, c.want)
		}
	}
}

func TestStem(t *testing.T) {
	for in, want := range map[string]string{
		"маркула":          "маркул",
		"земфиру":          "земфир",
		"кино":             "",
		"daft punk":        "",
		"светлану лободу":  "светлан лобод",
		"группа крови кап": "групп кров кап",
	} {
		if got := Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
}
