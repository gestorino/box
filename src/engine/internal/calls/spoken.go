package calls

import (
	"strconv"
	"strings"
	"unicode"

	"concierge/engine/internal/verify"
)

// Language is a language the Voice check call can be held in.
type Language struct{ Code, Name string }

// Languages the check speaks; the first is the default.
var Languages = []Language{{"en", "English"}, {"pt", "Português"}, {"ru", "Русский"}, {"es", "Español"}}

func languageOf(code string) (Language, bool) {
	for _, l := range Languages {
		if l.Code == code {
			return l, true
		}
	}
	return Language{}, false
}

// numberWords are each language's words for 0–99 that the reader adds up: units, teens, tens (and
// Spanish's one-word twenties), plus the word that joins tens and units.
var numberWords = map[string]map[string]int{
	"en": {
		"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
		"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
		"seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fourty": 40,
		"fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	},
	"pt": {
		"zero": 0, "um": 1, "uma": 1, "dois": 2, "duas": 2, "três": 3, "tres": 3, "quatro": 4, "cinco": 5, "seis": 6,
		"sete": 7, "oito": 8, "nove": 9, "dez": 10, "onze": 11, "doze": 12, "treze": 13, "catorze": 14, "quatorze": 14,
		"quinze": 15, "dezasseis": 16, "dezesseis": 16, "dezassete": 17, "dezessete": 17, "dezoito": 18,
		"dezanove": 19, "dezenove": 19, "vinte": 20, "trinta": 30, "quarenta": 40, "cinquenta": 50, "sessenta": 60,
		"setenta": 70, "oitenta": 80, "noventa": 90,
	},
	"ru": {
		"ноль": 0, "один": 1, "одна": 1, "два": 2, "две": 2, "три": 3, "четыре": 4, "пять": 5, "шесть": 6, "семь": 7,
		"восемь": 8, "девять": 9, "десять": 10, "одиннадцать": 11, "двенадцать": 12, "тринадцать": 13,
		"четырнадцать": 14, "пятнадцать": 15, "шестнадцать": 16, "семнадцать": 17, "восемнадцать": 18,
		"девятнадцать": 19, "двадцать": 20, "тридцать": 30, "сорок": 40, "пятьдесят": 50, "шестьдесят": 60,
		"семьдесят": 70, "восемьдесят": 80, "девяносто": 90,
	},
	"es": {
		"cero": 0, "uno": 1, "un": 1, "una": 1, "dos": 2, "tres": 3, "cuatro": 4, "cinco": 5, "seis": 6, "siete": 7,
		"ocho": 8, "nueve": 9, "diez": 10, "once": 11, "doce": 12, "trece": 13, "catorce": 14, "quince": 15,
		"dieciséis": 16, "dieciseis": 16, "diecisiete": 17, "dieciocho": 18, "diecinueve": 19, "veinte": 20,
		"veintiuno": 21, "veintiún": 21, "veintidós": 22, "veintidos": 22, "veintitrés": 23, "veintitres": 23,
		"veinticuatro": 24, "veinticinco": 25, "veintiséis": 26, "veintiseis": 26, "veintisiete": 27,
		"veintiocho": 28, "veintinueve": 29, "treinta": 30, "cuarenta": 40, "cincuenta": 50, "sesenta": 60,
		"setenta": 70, "ochenta": 80, "noventa": 90,
	},
}

var joiners = map[string]map[string]bool{"en": {"and": true}, "pt": {"e": true}, "ru": {"и": true}, "es": {"y": true}}

// spokenNumbers is every number in a transcript of speech, in order: digits as written, and the
// words of lang (or English, which an owner may fall back to) added up — "forty two", "quarenta e
// dois", "30 e 2", "сорок два" are each one number. Hyphens separate words ("forty-two"), so
// "4-7-2" is three numbers, as the transcriber writes a spoken code. Unknown words are skipped.
func spokenNumbers(lang, text string) []int {
	value := func(tok string) (int, bool) {
		if n, err := strconv.Atoi(tok); err == nil {
			return n, true
		}
		if n, ok := numberWords[lang][tok]; ok {
			return n, true
		}
		n, ok := numberWords["en"][tok]
		return n, ok
	}
	joins := func(tok string) bool { return joiners[lang][tok] || joiners["en"][tok] }
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var out []int
	for i := 0; i < len(tokens); i++ {
		n, ok := value(tokens[i])
		if !ok {
			continue
		}
		if n >= 20 && n <= 90 && n%10 == 0 { // tens: a following unit (after an optional joiner) adds on
			j := i + 1
			if j < len(tokens) && joins(tokens[j]) {
				j++
			}
			if j < len(tokens) {
				if u, ok := value(tokens[j]); ok && u >= 1 && u <= 9 {
					n, i = n+u, j
				}
			}
		}
		out = append(out, n)
	}
	return out
}

// answeredAloud is the spoken check's rule: the last number the owner said is the sum. The last,
// because people correct themselves on the phone ("forty… forty-two"); the box decides, never the
// voice model.
func answeredAloud(c verify.Challenge, lang, text string) bool {
	nums := spokenNumbers(lang, text)
	return len(nums) > 0 && nums[len(nums)-1] == c.A+c.B
}
