package i18n

import (
	"encoding/json"
	"strings"
	"testing"
)

// every catalog has every text of the English one, and none of its own: a text missing in a
// language would show in English in the middle of the others
func TestCatalogsComplete(t *testing.T) {
	read := func(code string) map[string]string {
		data, err := files.ReadFile("locales/" + code + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		return m
	}
	en := read("en")
	for _, l := range Languages {
		m := read(l.Code)
		for k := range en {
			if strings.TrimSpace(m[k]) == "" {
				t.Errorf("%s: %s is missing", l.Code, k)
			}
		}
		for k := range m {
			if _, ok := en[k]; !ok {
				t.Errorf("%s: %s is not in the English catalog", l.Code, k)
			}
		}
	}
}

// TestSetAndT checks a language set only when it is one, a text in it, the fallback for a
// key it lacks, the key itself for one no catalogue has, and the placeholders filled.
func TestSetAndT(t *testing.T) {
	defer Set(Fallback)
	Set("xx")
	if Current() != Fallback || Supported("xx") {
		t.Error("an unknown language was set")
	}
	Set("de")
	if Current() != "de" {
		t.Errorf("current %s", Current())
	}
	if T("usage.title") == "usage.title" || T("usage.title") == load(Fallback)["usage.title"] {
		t.Errorf("de usage.title %q", T("usage.title"))
	}
	catalogs["de"]["only.en"] = ""
	catalogs[Fallback]["only.en"] = "Hello {name}, {n} times"
	if got := T("only.en", "name", "Ada", "n", 3, "odd"); got != "Hello Ada, 3 times" {
		t.Errorf("fallback %q", got)
	}
	if T("no.such.key") != "no.such.key" {
		t.Error("an unknown key")
	}
	if len(load("xx")) != 0 {
		t.Error("an unknown catalogue")
	}
}

// TestFromEnv checks the language read from the locale variables, the first one set.
func TestFromEnv(t *testing.T) {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		t.Setenv(k, "")
	}
	if FromEnv() != "" {
		t.Error("nothing set")
	}
	t.Setenv("LANGUAGE", "zh_CN:en")
	if FromEnv() != "zh" {
		t.Errorf("LANGUAGE %q", FromEnv())
	}
	t.Setenv("LANG", "pt_BR.UTF-8")
	if FromEnv() != "pt" {
		t.Errorf("LANG %q", FromEnv())
	}
	t.Setenv("LC_ALL", "C.UTF-8")
	if FromEnv() != "" {
		t.Errorf("LC_ALL=C %q", FromEnv())
	}
}
