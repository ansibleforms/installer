package compose

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── the docker repository, served ────────────────────────────────────────────

// tarEntry is one entry of a repository archive made by a test.
type tarEntry struct {
	name, body string
	dir        bool
	mode       int64
}

// repoArchive writes a .tar.gz as GitHub's Codeload serves a repository: one folder first.
//
// Args:
//
//	t: the test.
//	entries: the entries.
//
// Returns:
//
//	The archive.
func repoArchive(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.dir {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(e.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// serveRepo serves archives by ref (refs/tags/v7.3.0, refs/heads/main) as Codeload does, and
// points the package at it.
//
// Args:
//
//	t: the test.
//	refs: the archive of each ref ; a ref missing answers 404.
func serveRepo(t *testing.T, refs map[string][]byte) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimPrefix(r.URL.Path, "/"+DockerRepo+"/tar.gz/")
		b, ok := refs[ref]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	was, wasSource := Codeload, Source
	Codeload, Source = srv.URL+"/", ""
	t.Cleanup(func() { Codeload, Source = was, wasSource })
}

// ─── fetching ─────────────────────────────────────────────────────────────────

// TestFetch checks the files of a version's tag written, the repository's own files, its
// workflows and what only leaves out skipped, executables kept so, a kept file left as it is,
// and main taken when the tag is not there.
func TestFetch(t *testing.T) {
	good := repoArchive(t,
		tarEntry{name: "docker-7.3.0/", dir: true},
		tarEntry{name: "docker-7.3.0/data/", dir: true},
		tarEntry{name: "docker-7.3.0/docker-compose.yml", body: "services: {}\n"},
		tarEntry{name: "docker-7.3.0/data/seed.yaml", body: "seed"},
		tarEntry{name: "docker-7.3.0/data/run.sh", body: "#!/bin/sh", mode: 0o755},
		tarEntry{name: "docker-7.3.0/README.md", body: "repo"},
		tarEntry{name: "docker-7.3.0/.github/workflows/ci.yml", body: "ci"},
		tarEntry{name: "docker-7.3.0/../evil", body: "x"},
		tarEntry{name: "top"},
	)
	serveRepo(t, map[string][]byte{"refs/tags/v7.3.0": good, "refs/heads/main": good})
	dir := t.TempDir()
	ref, written, err := Fetch(context.Background(), "7.3.0", dir, nil, false)
	if err != nil || ref != "v7.3.0" || len(written) != 3 {
		t.Fatalf("ref %s, written %v, %v", ref, written, err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "data", "run.sh")); st.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode %v", st.Mode())
	}
	for _, out := range []string{"README.md", ".github", "../evil"} {
		if _, err := os.Stat(filepath.Join(dir, out)); err == nil {
			t.Errorf("%s written", out)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "data", "seed.yaml"), []byte("mine"), 0o644)
	_, written, _ = Fetch(context.Background(), "7.3.0", dir, func(rel string) bool { return strings.HasPrefix(rel, "data") }, true)
	if b, _ := os.ReadFile(filepath.Join(dir, "data", "seed.yaml")); string(b) != "mine" || len(written) != 0 {
		t.Errorf("kept seed %q, written %v", b, written)
	}
	if ref, _, err := Fetch(context.Background(), "7.9.0", t.TempDir(), nil, false); err != nil || ref != "main" {
		t.Errorf("main: %s, %v", ref, err)
	}
}

// TestFetchFails checks no repository at all, an archive that is no gzip or a broken tar,
// and files that cannot be written.
func TestFetchFails(t *testing.T) {
	ctx := context.Background()
	serveRepo(t, map[string][]byte{})
	if _, _, err := Fetch(ctx, "7.3.0", t.TempDir(), nil, false); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("no repository: %v", err)
	}
	var garbage bytes.Buffer
	gz := gzip.NewWriter(&garbage)
	_, _ = gz.Write(bytes.Repeat([]byte("x"), 600))
	_ = gz.Close()
	noise := make([]byte, 64*1024)
	_, _ = rand.Read(noise)
	cut := repoArchive(t, tarEntry{name: "d/big", body: string(noise)})
	for name, b := range map[string][]byte{
		"not gzip":     []byte("plain"),
		"broken tar":   garbage.Bytes(),
		"cut short":    cut[:len(cut)/2],
		"dir on file":  repoArchive(t, tarEntry{name: "d/file/x/", dir: true}),
		"file on file": repoArchive(t, tarEntry{name: "d/file/y", body: "y"}),
		"file on dir":  repoArchive(t, tarEntry{name: "d/dir", body: "y"}),
	} {
		serveRepo(t, map[string][]byte{"refs/tags/v7.3.0": b})
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "file"), nil, 0o644)
		_ = os.MkdirAll(filepath.Join(dir, "dir", "full"), 0o755)
		if _, _, err := Fetch(ctx, "7.3.0", dir, nil, false); err == nil {
			t.Errorf("%s: fetched", name)
		}
	}
	Codeload = "http://\x7f/"
	if _, _, err := Fetch(ctx, "7.3.0", t.TempDir(), nil, false); err == nil {
		t.Error("an address that is none")
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	Codeload = srv.URL + "/"
	if _, _, err := Fetch(ctx, "7.3.0", t.TempDir(), nil, false); err == nil {
		t.Error("nothing listening")
	}
}

// TestCopyLocal checks a local copy: .git skipped, a missing copy, a folder it cannot read, a
// file gone while it is copied, and a file that cannot be written.
func TestCopyLocal(t *testing.T) {
	src := t.TempDir()
	put(t, src, map[string]string{".git/HEAD": "ref", "data/a": "a", "LICENSE": "l"})
	dir := t.TempDir()
	if _, written, err := copyLocal(src, dir, nil, false); err != nil || len(written) != 1 {
		t.Errorf("written %v, %v", written, err)
	}
	if _, _, err := copyLocal(filepath.Join(src, "none"), dir, nil, false); err == nil {
		t.Error("a missing copy")
	}
	gone := func(rel string) bool {
		if rel == "data/a" {
			_ = os.Remove(filepath.Join(src, "data", "a"))
		}
		return true
	}
	if _, _, err := copyLocal(src, t.TempDir(), gone, false); err == nil {
		t.Error("a file gone while copied")
	}
	put(t, src, map[string]string{"data/a": "a"})
	blocked := t.TempDir()
	_ = os.WriteFile(filepath.Join(blocked, "data"), nil, 0o644)
	if _, _, err := copyLocal(src, blocked, func(rel string) bool { return rel != "data" }, false); err == nil {
		t.Error("a file that cannot be written")
	}
}

// ─── images and settings ──────────────────────────────────────────────────────

// TestPrepareCompose checks the images pinned to the version (or to AF_APP_IMAGE and
// AF_RTE_IMAGE) and the containers named after the instance.
func TestPrepareCompose(t *testing.T) {
	in := "    image: ghcr.io/ansibleforms/ansibleforms:7\n    image: ghcr.io/ansibleforms/ansibleforms-rte-full:7\n    container_name: af_app\n"
	t.Setenv("AF_APP_IMAGE", "")
	t.Setenv("AF_RTE_IMAGE", "")
	got := PrepareCompose(in, "7.3.0", "lab")
	if !strings.Contains(got, "ansibleforms:7.3.0") || !strings.Contains(got, "rte-full:7.3.0") || !strings.Contains(got, "container_name: lab_app") {
		t.Errorf("pinned %s", got)
	}
	t.Setenv("AF_APP_IMAGE", "local/app:dev")
	t.Setenv("AF_RTE_IMAGE", "local/rte:dev")
	got = PrepareCompose(in, "7.3.0", "af")
	if !strings.Contains(got, "local/app:dev") || !strings.Contains(got, "local/rte:dev") || RteImage("7.3.0") != "local/rte:dev" {
		t.Errorf("overridden %s", got)
	}
}

// TestSetEnvMore checks a commented example taken only when no live line follows, a second
// line of a key left, and a file without a final newline.
func TestSetEnvMore(t *testing.T) {
	got := SetEnv("# A=example\nA=live\n# B=example\nB=1\nB=2\nnot a line\nC D=x", map[string]string{"A": "a", "B": "b", "Z": "z"})
	if !strings.Contains(got, "# A=example\nA=a") || !strings.Contains(got, "B=b\nB=2") || !strings.HasSuffix(got, "C D=x\n\n# Set by the AnsibleForms installer\nZ=z\n") {
		t.Errorf("got %q", got)
	}
	if hasLive([]string{"X=1"}, "Y") || !hasLive([]string{" Y=1"}, "Y") {
		t.Error("hasLive")
	}
}

// ─── certificates and copies ──────────────────────────────────────────────────

// TestSelfSignedNames checks the names and addresses of the certificate: the host name, or
// localhost without one, and the server's own addresses only.
func TestSelfSignedNames(t *testing.T) {
	wasHost, wasAddrs := hostname, interfaceAddrs
	defer func() { hostname, interfaceAddrs = wasHost, wasAddrs }()
	hostname = func() (string, error) { return "", errors.New("no name") }
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("10.0.0.5")},
			&net.IPNet{IP: net.ParseIP("127.0.0.1")},
			&net.IPNet{IP: net.ParseIP("fe80::1")},
			&net.IPAddr{IP: net.ParseIP("10.0.0.6")},
		}, nil
	}
	dir := t.TempDir()
	if err := SelfSigned(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")); err != nil {
		t.Fatal(err)
	}
	c := readCert(t, filepath.Join(dir, "c.pem"))
	if c.Subject.CommonName != "localhost" || len(c.IPAddresses) != 3 {
		t.Errorf("cn %s, ips %v", c.Subject.CommonName, c.IPAddresses)
	}
}

// TestCertificateFiles checks a certificate whose folder, certificate or key cannot be
// written, and a Runner's certificate that cannot be.
func TestCertificateFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	_ = os.WriteFile(file, nil, 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "certdir", "x"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "keydir", "x"), 0o755)
	for name, paths := range map[string][2]string{
		"folder": {filepath.Join(file, "c.pem"), filepath.Join(dir, "k.pem")},
		"cert":   {filepath.Join(dir, "certdir"), filepath.Join(dir, "k.pem")},
		"key":    {filepath.Join(dir, "c.pem"), filepath.Join(dir, "keydir")},
	} {
		if err := SelfSigned(paths[0], paths[1]); err == nil {
			t.Errorf("%s: written", name)
		}
	}
	if _, err := RunnerCertificate(filepath.Join(file, "c.pem"), filepath.Join(dir, "k.pem"), "runner.example.com"); err == nil {
		t.Error("a Runner's certificate under a file")
	}
}

// TestCopyFile checks a source that is not there, and a folder that cannot be made.
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	_ = os.WriteFile(src, []byte("x"), 0o644)
	if err := CopyFile(filepath.Join(dir, "none"), filepath.Join(dir, "dst"), 0o644); err == nil {
		t.Error("no source")
	}
	if err := CopyFile(src, filepath.Join(src, "under", "dst"), 0o644); err == nil {
		t.Error("a folder under a file")
	}
}
