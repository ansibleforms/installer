package releases

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fakeGitHub answers as GitHub's API and raw files would, and points the package at it.
//
// Args:
//
//	t: the test.
//	h: the handler.
func fakeGitHub(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wasAPI, wasRaw := apiBase, rawBase
	apiBase, rawBase = srv.URL, srv.URL+"/raw"
	t.Cleanup(func() { apiBase, rawBase = wasAPI, wasRaw })
}

// TestListAppVersions checks that the releases come newest first, without drafts,
// pre-releases or tags that are not versions, and that a GitHub token is sent.
func TestListAppVersions(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gh-token")
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+AppRepo+"/releases" || r.Header.Get("Authorization") != "Bearer gh-token" || r.Header.Get("User-Agent") != "ansibleforms-installer" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`[
			{"tag_name":"7.2.0"},
			{"tag_name":"7.10.0"},
			{"tag_name":"7.11.0","draft":true},
			{"tag_name":"7.12.0-rc.1","prerelease":true},
			{"tag_name":"latest"},
			{"tag_name":"v7.3.1"}
		]`))
	})
	got, err := ListAppVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Version{{7, 10, 0}, {7, 3, 1}, {7, 2, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestListAppVersionsFails checks that a refusal and a body that is no list say so.
func TestListAppVersionsFails(t *testing.T) {
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "rate limited", http.StatusForbidden) })
	if _, err := ListAppVersions(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a 403: %v", err)
	}
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"message":"x"}`)) })
	if _, err := ListAppVersions(context.Background()); err == nil || !strings.Contains(err.Error(), "reading") {
		t.Errorf("not a list: %v", err)
	}
}

// TestFetchChangelog checks that the changelog is read at the version's tag.
func TestFetchChangelog(t *testing.T) {
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/raw/"+AppRepo+"/7.3.0/CHANGELOG.md" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sample))
	})
	entries, err := FetchChangelog(context.Background(), Version{7, 3, 0})
	if err != nil || len(entries) == 0 || entries[0].Version != (Version{7, 3, 0}) {
		t.Fatalf("entries %v, %v", entries, err)
	}
	if _, err := FetchChangelog(context.Background(), Version{9, 9, 9}); err == nil || !strings.Contains(err.Error(), "9.9.9") {
		t.Errorf("a missing tag: %v", err)
	}
}

// TestGetFails checks an address that is none, a server that is gone and a body cut short.
func TestGetFails(t *testing.T) {
	ctx := context.Background()
	if _, err := get(ctx, "http://\x7f"); err == nil {
		t.Error("an address that is none")
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if _, err := get(ctx, srv.URL); err == nil {
		t.Error("a server that is gone")
	}
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer cut.Close()
	if _, err := get(ctx, cut.URL); err == nil {
		t.Error("a body cut short")
	}
}
