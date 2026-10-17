package releases

import (
	"regexp"
	"strings"
)

// ─── the release notes ────────────────────────────────────────────────────────
// The app's CHANGELOG.md is written by release-please in the Keep a Changelog shape: a
// "## [7.3.0](compare link) (2026-10-20)" heading per version, then "### Added", "### Fixed",
// "### ⚠ BREAKING CHANGES", "### Deprecated", "### Removed"... sections of "* " items.

// Section is one "###" part of a version's notes.
type Section struct {
	// Title is the heading as written, without the "###" (Added, ⚠ BREAKING CHANGES).
	Title string
	// Items are its bullet points, without the bullet.
	Items []string
}

// Entry is the release notes of one version.
type Entry struct {
	Version  Version
	Date     string
	Sections []Section
}

// a version's heading : "## [7.3.0](https://...) (2026-10-20)", "## 7.3.0 (2026-10-20)" or
// "## [7.3.0] - 2026-10-20"
var headingRe = regexp.MustCompile(`^##\s+\[?v?(\d+\.\d+\.\d+)\]?(?:\([^)]*\))?\s*(?:[-(]\s*(\d{4}-\d{2}-\d{2})\)?)?`)

// ParseChangelog reads the release notes of every version in a CHANGELOG.md.
//
// Args:
//
//	text: the file's content.
//
// Returns:
//
//	The entries, in the file's order (newest first); a heading that is not a version
//	(the file's title, "Unreleased") and the text before the first version are left out.
func ParseChangelog(text string) []Entry {
	var entries []Entry
	var cur *Entry
	var sec *Section
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \r\t")
		if strings.HasPrefix(line, "## ") {
			sec = nil
			cur = nil
			m := headingRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			v, err := ParseVersion(m[1])
			if err != nil {
				continue
			}
			entries = append(entries, Entry{Version: v, Date: m[2]})
			cur = &entries[len(entries)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "### ") {
			cur.Sections = append(cur.Sections, Section{Title: strings.TrimSpace(line[4:])})
			sec = &cur.Sections[len(cur.Sections)-1]
			continue
		}
		trimmed := strings.TrimSpace(line)
		if sec == nil || trimmed == "" {
			continue
		}
		// a bullet starts an item ; an indented line goes on with the one before
		if strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "- ") {
			sec.Items = append(sec.Items, cleanItem(trimmed[2:]))
		} else if len(sec.Items) > 0 {
			sec.Items[len(sec.Items)-1] += " " + cleanItem(trimmed)
		}
	}
	return entries
}

// a markdown link [text](url) and a commit or issue reference at an item's end
var (
	linkRe = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	// the references release-please adds : (#1075) (e429f23), closes #1065, closes #1066
	trailingRef = regexp.MustCompile(`(?:\s*,?\s*(?:\((?:#\d+|[0-9a-f]{7,40})\)|(?:closes|fixes|resolves)\s+#\d+))+\s*$`)
)

// cleanItem makes an item readable in a terminal: links keep their text, the trailing
// commit and pull request references go.
//
// Args:
//
//	s: the item as written.
//
// Returns:
//
//	The item for a terminal.
func cleanItem(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	s = trailingRef.ReplaceAllString(s, "")
	return strings.TrimSpace(strings.ReplaceAll(s, "**", ""))
}

// Between picks the entries an upgrade brings: newer than the installed version, up to and
// including the target.
//
// Args:
//
//	entries: every entry (ParseChangelog).
//	from: the installed version (left out).
//	to: the version installed after the upgrade (included).
//
// Returns:
//
//	The entries, newest first.
func Between(entries []Entry, from, to Version) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Version.Compare(from) > 0 && e.Version.Compare(to) <= 0 {
			out = append(out, e)
		}
	}
	return out
}

// ─── what needs attention ─────────────────────────────────────────────────────

// Kind sorts a section by how much it asks of an administrator before upgrading.
type Kind int

const (
	// KindOther is everything else (Added, Fixed, Performance...).
	KindOther Kind = iota
	// KindSecurity is a security fix: worth knowing, nothing to do.
	KindSecurity
	// KindDeprecated is what still works but goes in a later major.
	KindDeprecated
	// KindRemoved is what no longer exists.
	KindRemoved
	// KindBreaking is what may need a change before or right after upgrading.
	KindBreaking
)

// KindOf sorts a section by its title.
//
// Args:
//
//	title: the section's title (⚠ BREAKING CHANGES, Deprecated, Removed...).
//
// Returns:
//
//	Its kind, KindOther when it asks nothing.
func KindOf(title string) Kind {
	t := strings.ToLower(title)
	switch {
	case strings.Contains(t, "breaking"):
		return KindBreaking
	case strings.Contains(t, "removed"):
		return KindRemoved
	case strings.Contains(t, "deprecat"):
		return KindDeprecated
	case strings.Contains(t, "security"):
		return KindSecurity
	}
	return KindOther
}

// HasBreaking tells whether any of the entries announces a breaking change or a removal,
// which the upgrade asks the administrator to confirm.
//
// Args:
//
//	entries: the entries the upgrade brings.
//
// Returns:
//
//	True when one has a breaking or removed section with items.
func HasBreaking(entries []Entry) bool {
	for _, e := range entries {
		for _, s := range e.Sections {
			if k := KindOf(s.Title); (k == KindBreaking || k == KindRemoved) && len(s.Items) > 0 {
				return true
			}
		}
	}
	return false
}
