package releases

import "testing"

const sample = `# Changelog

All notable changes to this project will be documented in this file.

## [7.3.0](https://github.com/ansibleforms/ansibleforms/compare/7.2.0...7.3.0) (2026-10-20)


### ⚠ BREAKING CHANGES

* **auth:** the old token endpoint is gone ([#1200](https://github.com/x/y/issues/1200)) ([abc1234](https://github.com/x/y/commit/abc1234))

### Added

* Profile &gt; Permissions in tabs ([#1160](https://github.com/x/y/issues/1160)) ([e429f23](https://github.com/x/y/commit/e429f23)), closes [#1065](https://github.com/x/y/issues/1065)
* a long item
  that wraps

## [7.2.0](https://github.com/ansibleforms/ansibleforms/compare/7.1.0...7.2.0) (2026-10-14)

### Deprecated

* ` + "`LOG_SYSLOG_*`" + ` variables

## [7.1.0] - 2026-10-01

### Fixed

* something
`

func TestParseChangelog(t *testing.T) {
	entries := ParseChangelog(sample)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	first := entries[0]
	if first.Version.String() != "7.3.0" || first.Date != "2026-10-20" {
		t.Fatalf("first entry: %+v", first)
	}
	if len(first.Sections) != 2 || first.Sections[0].Title != "⚠ BREAKING CHANGES" {
		t.Fatalf("sections: %+v", first.Sections)
	}
	if got := first.Sections[0].Items[0]; got != "auth: the old token endpoint is gone" {
		t.Fatalf("breaking item: %q", got)
	}
	if got := first.Sections[1].Items[0]; got != "Profile &gt; Permissions in tabs" {
		t.Fatalf("references left: %q", got)
	}
	if got := first.Sections[1].Items[1]; got != "a long item that wraps" {
		t.Fatalf("wrapped item: %q", got)
	}
	if entries[2].Version.String() != "7.1.0" || entries[2].Date != "2026-10-01" {
		t.Fatalf("keep a changelog heading: %+v", entries[2])
	}
}

func TestBetweenAndBreaking(t *testing.T) {
	entries := ParseChangelog(sample)
	from, _ := ParseVersion("7.1.0")
	to, _ := ParseVersion("7.3.0")
	got := Between(entries, from, to)
	if len(got) != 2 || got[0].Version.String() != "7.3.0" || got[1].Version.String() != "7.2.0" {
		t.Fatalf("between: %+v", got)
	}
	if !HasBreaking(got) {
		t.Fatal("7.3.0 has a breaking change")
	}
	if HasBreaking(got[1:]) {
		t.Fatal("7.2.0 only deprecates")
	}
}

func TestVersion(t *testing.T) {
	a, err := ParseVersion("v7.10.0")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ParseVersion("7.9.3")
	if a.Compare(b) != 1 || b.Compare(a) != -1 || a.Compare(a) != 0 {
		t.Fatal("compare")
	}
	if _, err := ParseVersion("7.3.0-rc.1"); err == nil {
		t.Fatal("a pre-release is not a release")
	}
}

func TestKindOf(t *testing.T) {
	cases := map[string]Kind{
		"⚠ BREAKING CHANGES": KindBreaking,
		"Removed":            KindRemoved,
		"Deprecated":         KindDeprecated,
		"Security":           KindSecurity,
		"Added":              KindOther,
	}
	for title, want := range cases {
		if got := KindOf(title); got != want {
			t.Errorf("%s: want %d, got %d", title, want, got)
		}
	}
}
