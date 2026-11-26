package native

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── archives ─────────────────────────────────────────────────────────────────

// entry is one entry of an archive made by a test.
type entry struct {
	name, body, link string
	kind             byte
	mode             int64
}

// archive writes a .tar.gz of entries.
//
// Args:
//
//	t: the test.
//	entries: the entries, in order.
//
// Returns:
//
//	The archive's bytes.
func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: e.mode, Linkname: e.link, Size: int64(len(e.body))}
		if e.kind != tar.TypeReg {
			h.Size = 0
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.kind == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// sum is a SHA-256 in hexadecimal.
func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ─── unpacking ────────────────────────────────────────────────────────────────

// TestUntar checks files, folders, links inside the folder, the leading part stripped, and
// every path out of the folder left out.
func TestUntar(t *testing.T) {
	dest := t.TempDir()
	data := archive(t,
		entry{name: "pkg/", kind: tar.TypeDir, mode: 0o755},
		entry{name: "pkg", kind: tar.TypeDir},
		entry{name: "./pkg/./", kind: tar.TypeDir},
		entry{name: "pkg/bin/node", kind: tar.TypeReg, body: "#!node", mode: 0o755},
		entry{name: "pkg/lib/a.js", kind: tar.TypeReg, body: "a"},
		entry{name: "pkg/lib/b.js", kind: tar.TypeSymlink, link: "a.js"},
		entry{name: "pkg/lib/c.js", kind: tar.TypeLink, link: "pkg/lib/a.js"},
		entry{name: "pkg/lib/d.js", kind: tar.TypeSymlink, link: "a.js"},
		entry{name: "pkg/../../evil", kind: tar.TypeReg, body: "x"},
		entry{name: "pkg/abs", kind: tar.TypeSymlink, link: "/etc/passwd"},
		entry{name: "pkg/up", kind: tar.TypeSymlink, link: "../../../etc"},
		entry{name: "pkg/hard", kind: tar.TypeLink, link: "pkg/../../../etc/passwd"},
		entry{name: "pkg/fifo", kind: tar.TypeFifo},
	)
	if err := untar(bytes.NewReader(data), dest, 1); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(dest, "bin", "node")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("bin/node: %v, %v", st, err)
	}
	if l, _ := os.Readlink(filepath.Join(dest, "lib", "b.js")); l != "a.js" {
		t.Errorf("symlink %q", l)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "lib", "c.js")); string(b) != "a" {
		t.Errorf("hard link %q", b)
	}
	for _, out := range []string{"abs", "up", "hard", "fifo", "../evil"} {
		if _, err := os.Lstat(filepath.Join(dest, out)); err == nil {
			t.Errorf("%s was written", out)
		}
	}
}

// TestUntarFails checks what is no archive, a broken tar, and each entry that cannot be
// written.
func TestUntarFails(t *testing.T) {
	if err := untar(strings.NewReader("not gzip"), t.TempDir(), 0); err == nil {
		t.Error("not gzip")
	}
	var garbage bytes.Buffer
	gz := gzip.NewWriter(&garbage)
	_, _ = gz.Write(bytes.Repeat([]byte("x"), 1024))
	_ = gz.Close()
	if err := untar(&garbage, t.TempDir(), 0); err == nil {
		t.Error("a broken tar")
	}
	noise := make([]byte, 64*1024)
	_, _ = rand.Read(noise)
	full := archive(t, entry{name: "big", kind: tar.TypeReg, body: string(noise)})
	if err := untar(bytes.NewReader(full[:len(full)/2]), t.TempDir(), 0); err == nil {
		t.Error("an archive cut short")
	}
	for name, e := range map[string]entry{
		"a folder under a file": {name: "file/dir", kind: tar.TypeDir},
		"a file under a file":   {name: "file/x", kind: tar.TypeReg, body: "x"},
		"a file over a folder":  {name: "dir", kind: tar.TypeReg, body: "x"},
		"a link under a file":   {name: "file/l", kind: tar.TypeSymlink, link: "../dir"},
		"a link over a folder":  {name: "dir", kind: tar.TypeSymlink, link: "file"},
		"a hard link to none":   {name: "h", kind: tar.TypeLink, link: "none"},
	} {
		dest := t.TempDir()
		_ = os.WriteFile(filepath.Join(dest, "file"), nil, 0o644)
		_ = os.MkdirAll(filepath.Join(dest, "dir", "full"), 0o755)
		if err := untar(bytes.NewReader(archive(t, e)), dest, 0); err == nil {
			t.Errorf("%s: written", name)
		}
	}
}

// ─── downloads ────────────────────────────────────────────────────────────────

// files serves files by their path, 404 for the others, and 500 for /broken.
//
// Args:
//
//	t: the test.
//	files: the files by path.
//
// Returns:
//
//	The server's address.
func files(t *testing.T, files map[string][]byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "broken") {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/short" {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte("short"))
			return
		}
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestFetch checks an archive checked against its published checksum and unpacked, and each
// way that fails.
func TestFetch(t *testing.T) {
	ctx := context.Background()
	good := archive(t, entry{name: "uv/uv", kind: tar.TypeReg, body: "uv", mode: 0o755})
	base := files(t, map[string][]byte{
		"/uv.tar.gz":           good,
		"/uv.tar.gz.sha256":    []byte(sum(good) + "  uv.tar.gz\n"),
		"/empty.tar.gz.sha256": []byte("  \n"),
		"/bad.tar.gz":          good,
		"/bad.tar.gz.sha256":   []byte(strings.Repeat("0", 64)),
		"/short.sha256":        []byte(strings.Repeat("0", 64)),
	})
	dest := t.TempDir()
	if err := fetchVerified(ctx, base+"/uv.tar.gz", dest, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "uv")); err != nil {
		t.Error("not unpacked")
	}
	if err := fetchVerified(ctx, base+"/none.tar.gz", dest, 1); !errors.Is(err, errNotFound) {
		t.Errorf("no checksum: %v", err)
	}
	if err := fetchVerified(ctx, base+"/empty.tar.gz", dest, 1); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("an empty checksum: %v", err)
	}
	if err := fetchVerified(ctx, base+"/bad.tar.gz", dest, 1); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("another checksum: %v", err)
	}
	if err := fetchVerified(ctx, base+"/short", dest, 1); err == nil || !strings.Contains(err.Error(), "downloading") {
		t.Errorf("cut short: %v", err)
	}
	if err := fetchSum(ctx, base+"/none", "", dest, 1); !errors.Is(err, errNotFound) {
		t.Errorf("no archive: %v", err)
	}
	if _, err := getText(ctx, base+"/broken"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("a 500: %v", err)
	}
	if _, err := get(ctx, "http://\x7f"); err == nil {
		t.Error("an address that is none")
	}
	if _, err := get(ctx, "http://127.0.0.1:1/x"); err == nil {
		t.Error("nothing listening")
	}
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "none"))
	if err := fetchSum(ctx, base+"/uv.tar.gz", sum(good), dest, 1); err == nil {
		t.Error("no temporary folder")
	}
}
