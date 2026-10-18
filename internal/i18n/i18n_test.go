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
