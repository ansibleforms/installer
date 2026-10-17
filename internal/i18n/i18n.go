// Package i18n holds the installer's texts in the app's languages: one JSON catalog per
// language, embedded in the binary, the language taken from --lang, else the server's locale,
// else English.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

//go:embed locales/*.json
var files embed.FS

// Language is one the installer speaks, as the app's language menu lists it.
type Language struct {
	Code  string
	Label string
}

// Languages are the app's languages, in the order of its menu (client/src/config/languages.js).
var Languages = []Language{
	{"ca", "Català"},
	{"de", "Deutsch"},
	{"en", "English"},
	{"es", "Español"},
	{"fr", "Français"},
	{"it", "Italiano"},
	{"nl", "Nederlands"},
	{"pl", "Polski"},
	{"pt", "Português"},
	{"ja", "日本語"},
	{"zh", "简体中文"},
}

// Fallback is the language of a text missing in the chosen one.
const Fallback = "en"

var (
	current  = Fallback
	catalogs = map[string]map[string]string{}
)

// load reads a language's catalog once.
//
// Args:
//
//	code: the language.
//
// Returns:
//
//	Its texts by key; empty when it has none.
func load(code string) map[string]string {
	if c, ok := catalogs[code]; ok {
		return c
	}
	c := map[string]string{}
	if data, err := files.ReadFile("locales/" + code + ".json"); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	catalogs[code] = c
	return c
}

// Supported tells whether the installer speaks a language.
//
// Args:
//
//	code: the language (de, pt).
//
// Returns:
//
//	True when it is one of the app's.
func Supported(code string) bool {
	for _, l := range Languages {
		if l.Code == code {
			return true
		}
	}
	return false
}

// Set chooses the language.
//
// Args:
//
//	code: the language; one it does not speak keeps the current one.
func Set(code string) {
	if Supported(code) {
		current = code
	}
}

// Current is the language chosen.
//
// Returns:
//
//	Its code.
func Current() string {
	return current
}

// FromEnv finds the server's language in its locale (LC_ALL, LC_MESSAGES, LANG, LANGUAGE).
//
// Returns:
//
//	The code of a language the installer speaks, or "" when the locale names none (C, POSIX).
func FromEnv() string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		// pt_BR.UTF-8, de_DE, zh_CN:en ; the first one named
		v = strings.SplitN(v, ":", 2)[0]
		code := strings.ToLower(strings.SplitN(strings.SplitN(v, ".", 2)[0], "_", 2)[0])
		if Supported(code) {
			return code
		}
		return ""
	}
	return ""
}

// T gives a text in the chosen language, its {placeholders} filled.
//
// Args:
//
//	key: the text's key.
//	args: placeholder names and values, in pairs ("version", "7.3.0").
//
// Returns:
//
//	The text; in English when the language lacks it, the key itself when English does too.
func T(key string, args ...any) string {
	s, ok := load(current)[key]
	if !ok || s == "" {
		s, ok = load(Fallback)[key]
		if !ok {
			s = key
		}
	}
	for i := 0; i+1 < len(args); i += 2 {
		s = strings.ReplaceAll(s, "{"+fmt.Sprint(args[i])+"}", fmt.Sprint(args[i+1]))
	}
	return s
}
