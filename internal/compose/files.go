// Package compose installs, upgrades and backs up AnsibleForms with Docker Compose, on
// Docker or Podman, from the files of the ansibleforms/docker repository.
package compose

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ─── the docker repository's files ────────────────────────────────────────────

// DockerRepo is the repository with the compose file, the .env template and the data seed.
const DockerRepo = "ansibleforms/docker"

// download fetches the docker repository as a tarball : at the tag of the version (vX.Y.Z,
// made on every app release), else at the branch of its major (main for 7, as before tags).
//
// Args:
//
//	ctx: the context, to cancel.
//	version: the AnsibleForms version (7.3.0).
//
// Returns:
//
//	The tarball's body (to close), and the reference it came from.
//
// Errors:
//
//	When neither can be fetched.
func download(ctx context.Context, version string) (io.ReadCloser, string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	refs := []string{"refs/tags/v" + version, "refs/heads/main"}
	var last error
	for _, ref := range refs {
		url := "https://codeload.github.com/" + DockerRepo + "/tar.gz/" + ref
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("User-Agent", "ansibleforms-installer")
		res, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		if res.StatusCode == http.StatusOK {
			return res.Body, strings.TrimPrefix(strings.TrimPrefix(ref, "refs/tags/"), "refs/heads/"), nil
		}
		res.Body.Close()
		last = fmt.Errorf("%s answered %s", url, res.Status)
	}
	return nil, "", last
}

// Fetch writes the docker repository's files into a folder.
//
// Args:
//
//	ctx: the context, to cancel.
//	version: the AnsibleForms version, whose files are taken.
//	dir: the installation folder.
//	only: when set, the files written are only those it accepts (their path inside the
//	      repository, docker-compose.yml, data/...) ; nil writes them all.
//	keep: a file that already exists is left as it is (the user's data on an upgrade).
//
// Returns:
//
//	The reference the files came from (v7.3.0, or main), and the files written.
//
// Errors:
//
//	When the download or a write fails.
func Fetch(ctx context.Context, version, dir string, only func(string) bool, keep bool) (string, []string, error) {
	// a local copy of the docker repository (--source) : an air-gapped server, or a test
	if Source != "" {
		return copyLocal(Source, dir, only, keep)
	}
	body, ref, err := download(ctx, version)
	if err != nil {
		return "", nil, fmt.Errorf("downloading the compose files: %w", err)
	}
	defer body.Close()
	gz, err := gzip.NewReader(body)
	if err != nil {
		return "", nil, err
	}
	tr := tar.NewReader(gz)
	var written []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, err
		}
		// docker-<ref>/docker-compose.yml : the first part is the archive's own folder
		_, rel, ok := strings.Cut(h.Name, "/")
		if !ok || rel == "" || strings.HasPrefix(rel, ".github/") || isRepoOnly(rel) {
			continue
		}
		if only != nil && !only(strings.TrimSuffix(rel, "/")) {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(os.PathSeparator)) {
			continue // a path out of the folder : never written
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", nil, err
			}
		case tar.TypeReg:
			if keep {
				if _, err := os.Stat(target); err == nil {
					continue
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", nil, err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return "", nil, err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // the repository's own small files
				f.Close()
				return "", nil, err
			}
			if err := f.Close(); err != nil {
				return "", nil, err
			}
			written = append(written, rel)
		}
	}
	return ref, written, nil
}

// Source is a local copy of the docker repository to take the files from instead of GitHub
// (--source) ; empty : downloaded.
var Source string

// copyLocal writes the files of a local copy of the docker repository into a folder, as Fetch
// writes the downloaded ones.
//
// Args:
//
//	src: the local copy.
//	dir: the installation folder.
//	only: the files taken (nil : all).
//	keep: a file that exists already is left as it is.
//
// Returns:
//
//	"local", and the files written.
//
// Errors:
//
//	When a file cannot be read or written.
func copyLocal(src, dir string, only func(string) bool, keep bool) (string, []string, error) {
	var written []string
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() && (rel == ".git" || rel == ".github") {
			return filepath.SkipDir
		}
		if isRepoOnly(rel) || (only != nil && !only(rel)) {
			return nil
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if keep {
			if _, err := os.Stat(target); err == nil {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := CopyFile(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		written = append(written, rel)
		return nil
	})
	return "local", written, err
}

// isRepoOnly tells whether a file of the repository is about the repository, not the
// installation (its README, licence, contributing guide).
//
// Args:
//
//	rel: the file's path in the repository.
//
// Returns:
//
//	True for a file not copied.
func isRepoOnly(rel string) bool {
	switch rel {
	case "README.md", "LICENSE", "CONTRIBUTING.md", "SECURITY.md", "CHANGELOG.md", ".gitignore":
		return true
	}
	return false
}

// ─── the compose file ─────────────────────────────────────────────────────────

var (
	// the app's images, at any tag (:7, :7.2.0)
	imageRe = regexp.MustCompile(`(ghcr\.io/ansibleforms/ansibleforms(?:-rte-[a-z]+)?):[^\s"']+`)
	// a container's fixed name (af_db, af_app, af_rte)
	containerRe = regexp.MustCompile(`(?m)^(\s*container_name:\s*)af_([a-z]+)\s*$`)
)

// PrepareCompose pins the app's images to the exact version, so an upgrade only happens
// when the installer runs one, and names the containers after the instance.
//
// Args:
//
//	content: the compose file as the repository has it.
//	version: the AnsibleForms version (7.3.0).
//	name: the instance's name; its containers are <name>_db, <name>_app and <name>_rte.
//
// Returns:
//
//	The compose file to write.
func PrepareCompose(content, version, name string) string {
	content = imageRe.ReplaceAllStringFunc(content, func(m string) string {
		repo := imageRe.FindStringSubmatch(m)[1]
		if strings.HasSuffix(repo, "-rte-full") || strings.Contains(repo, "-rte-") {
			if img := os.Getenv("AF_RTE_IMAGE"); img != "" {
				return img
			}
		} else if img := os.Getenv("AF_APP_IMAGE"); img != "" {
			return img
		}
		return repo + ":" + version
	})
	return containerRe.ReplaceAllString(content, "${1}"+name+"_${2}")
}

// RteImage is the RTE image a runner runs : AF_RTE_IMAGE (a mirror, a test build), else the
// published one at the version.
//
// Args:
//
//	version: the AnsibleForms version.
//
// Returns:
//
//	The image.
func RteImage(version string) string {
	if img := os.Getenv("AF_RTE_IMAGE"); img != "" {
		return img
	}
	return "ghcr.io/ansibleforms/ansibleforms-rte-full:" + version
}

// ─── the .env file ────────────────────────────────────────────────────────────

// SetEnv sets variables in a .env file : an existing line (KEY=...) gets the new value, a
// commented-out one (# KEY=...) is uncommented with it, a missing one is added at the end.
//
// Args:
//
//	content: the file's content.
//	values: the variables to set.
//
// Returns:
//
//	The new content.
func SetEnv(content string, values map[string]string) string {
	lines := strings.Split(content, "\n")
	done := map[string]bool{}
	for i, line := range lines {
		t := strings.TrimSpace(line)
		commented := strings.HasPrefix(t, "#")
		t = strings.TrimSpace(strings.TrimPrefix(t, "#"))
		key, _, ok := strings.Cut(t, "=")
		if !ok || strings.ContainsAny(key, " \t") {
			continue
		}
		v, wanted := values[key]
		if !wanted || done[key] {
			continue
		}
		// a commented example is only taken when no live line follows for the same key
		if commented && hasLive(lines[i+1:], key) {
			continue
		}
		lines[i] = key + "=" + v
		done[key] = true
	}
	var extra []string
	for k, v := range values {
		if !done[k] {
			extra = append(extra, k+"="+v)
		}
	}
	out := strings.Join(lines, "\n")
	if len(extra) > 0 {
		sortStrings(extra)
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "\n# Set by the AnsibleForms installer\n" + strings.Join(extra, "\n") + "\n"
	}
	return out
}

// hasLive tells whether a key has an uncommented line further down.
//
// Args:
//
//	lines: the lines after the current one.
//	key: the variable.
//
// Returns:
//
//	True when one sets it.
func hasLive(lines []string, key string) bool {
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), key+"=") {
			return true
		}
	}
	return false
}

// GetEnv reads a variable of a .env file.
//
// Args:
//
//	content: the file's content.
//	key: the variable.
//
// Returns:
//
//	Its value, "" when it is not set.
func GetEnv(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// sortStrings sorts a short list in place (no sort import for one call).
//
// Args:
//
//	s: the list.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ─── secrets and the certificate ──────────────────────────────────────────────

// letters a secret is made of : no character a .env, a shell or a YAML file treats apart
const secretChars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// Secret makes a random secret.
//
// Args:
//
//	n: its length.
//
// Returns:
//
//	The secret.
func Secret(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(secretChars)))
	for i := range b {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err) // the system's random source is gone : nothing safe can be made
		}
		b[i] = secretChars[k.Int64()]
	}
	return string(b)
}

// SelfSigned writes a self-signed certificate of this server (its host name, localhost and
// its addresses), valid ten years, where the app reads its certificate by default.
//
// Args:
//
//	certFile: the certificate's path (data/certificates/cert.pem).
//	keyFile: the key's path (data/certificates/key.pem).
//
// Errors:
//
//	When the files cannot be written.
func SelfSigned(certFile, keyFile string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"AnsibleForms"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
				tpl.IPAddresses = append(tpl.IPAddresses, ipn.IP)
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDer, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	return os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer}), 0o600)
}

// CopyFile copies a file (the user's own certificate and key).
//
// Args:
//
//	src: the file to copy.
//	dst: the copy.
//	mode: the copy's permissions.
//
// Errors:
//
//	When it cannot be read or written.
func CopyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}
