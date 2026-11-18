package spotify

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s",
	'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "",
	'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// compact folds a name for comparison: lower case, no spaces or punctuation,
// a leading "the" dropped. "The Beatles" → "beatles", "КИНО" → "кино".
func compact(s string) string {
	w := words(s)
	if len(w) > 1 && w[0] == "the" {
		w = w[1:]
	}
	return strings.Join(w, "")
}

func latin(s string) string {
	var b strings.Builder
	for _, r := range s {
		if t, ok := translit[r]; ok {
			b.WriteString(t)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SameName reports whether a recognised name and a catalogue name are the same
// artist, tolerating case, punctuation, "the" and ASR transliteration
// ("дафт панк" for "Daft Punk").
func SameName(heard, name string) bool {
	a, b := compact(heard), compact(name)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	a, b = latin(a), latin(b)
	if a == b {
		return true
	}
	n := max(utf8.RuneCountInString(a), utf8.RuneCountInString(b))
	return n >= 5 && distance(a, b) <= n/5
}

func distance(a, b string) int {
	x, y := []rune(a), []rune(b)
	prev := make([]int, len(y)+1)
	cur := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(y)]
}

var endings = []string{"ами", "ями", "ого", "его", "ому", "ему", "ой", "ей", "ом", "ем", "ую", "юю", "ая", "яя", "ы", "и", "у", "ю", "а", "я", "е"}

// Stem drops Russian case endings from long Cyrillic words, for a second
// search when the first finds nothing: ASR hears "маркула", the catalogue has
// "Маркул". It returns "" when nothing changed.
func Stem(query string) string {
	w := strings.Fields(query)
	changed := false
	for i, t := range w {
		if utf8.RuneCountInString(t) <= 4 || !cyrillic(t) {
			continue
		}
		for _, e := range endings {
			if strings.HasSuffix(t, e) {
				w[i] = strings.TrimSuffix(t, e)
				changed = true
				break
			}
		}
	}
	if !changed {
		return ""
	}
	return strings.Join(w, " ")
}

func cyrillic(s string) bool {
	for _, r := range s {
		if !unicode.Is(unicode.Cyrillic, r) {
			return false
		}
	}
	return true
}
