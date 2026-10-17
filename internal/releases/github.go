package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

// ─── GitHub ───────────────────────────────────────────────────────────────────

// AppRepo is the app's repository, where its releases and CHANGELOG.md live.
const AppRepo = "ansibleforms/ansibleforms"

// a short client : GitHub answers fast or not at all
var client = &http.Client{Timeout: 30 * time.Second}

// get fetches a URL, with the GitHub token when one is set (GITHUB_TOKEN, GH_TOKEN), so a
// server behind a shared address does not run into the anonymous rate limit.
//
// Args:
//
//	ctx: the context, to cancel.
//	url: the address.
//
// Returns:
//
//	The body.
//
// Errors:
//
//	When the request fails or GitHub does not answer 200.
func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ansibleforms-installer")
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if tok := os.Getenv(name); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
			break
		}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, res.Status)
	}
	return body, nil
}

// ListAppVersions lists the app's published releases.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Returns:
//
//	The versions, newest first; drafts, pre-releases and tags that are not X.Y.Z left out.
//
// Errors:
//
//	When GitHub cannot be reached.
func ListAppVersions(ctx context.Context) ([]Version, error) {
	body, err := get(ctx, "https://api.github.com/repos/"+AppRepo+"/releases?per_page=100")
	if err != nil {
		return nil, fmt.Errorf("listing the AnsibleForms releases: %w", err)
	}
	var list []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("reading the AnsibleForms releases: %w", err)
	}
	var out []Version
	for _, r := range list {
		if r.Draft || r.Prerelease {
			continue
		}
		if v, err := ParseVersion(r.Tag); err == nil {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Compare(out[j]) > 0 })
	return out, nil
}

// FetchChangelog reads the app's CHANGELOG.md as it was released with a version, which
// holds the notes of that version and of every one before it.
//
// Args:
//
//	ctx: the context, to cancel.
//	v: the version (its tag).
//
// Returns:
//
//	The entries (ParseChangelog).
//
// Errors:
//
//	When the file cannot be fetched.
func FetchChangelog(ctx context.Context, v Version) ([]Entry, error) {
	body, err := get(ctx, "https://raw.githubusercontent.com/"+AppRepo+"/"+v.String()+"/CHANGELOG.md")
	if err != nil {
		return nil, fmt.Errorf("fetching the release notes of %s: %w", v, err)
	}
	return ParseChangelog(string(body)), nil
}
