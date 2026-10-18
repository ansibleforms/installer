package native

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ─── downloads ────────────────────────────────────────────────────────────────

// errNotFound says a download is not there (a release without a bundle).
var errNotFound = errors.New("not found")

// client downloads the bundle, Node.js and uv: large files, so a long timeout.
var client = &http.Client{Timeout: 30 * time.Minute}

// get starts a download.
//
// Args:
//
//	ctx: the context, to cancel.
//	url: what to download.
//
// Returns:
//
//	The body, to close.
//
// Errors:
//
//	errNotFound for a 404, else when the request fails or answers otherwise than 200.
func get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ansibleforms-installer")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotFound {
		res.Body.Close()
		return nil, fmt.Errorf("%s: %w", url, errNotFound)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("%s answered %s", url, res.Status)
	}
	return res.Body, nil
}

// getText downloads a small text (a checksum, an index).
func getText(ctx context.Context, url string) (string, error) {
	body, err := get(ctx, url)
	if err != nil {
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 32<<20))
	return string(data), err
}

// fetchVerified downloads a .tar.gz whose checksum is published next to it (<url>.sha256),
// checks it, and unpacks it.
//
// Args:
//
//	ctx: the context, to cancel.
//	url: the archive.
//	dest: the folder it is unpacked into.
//	strip: the leading path parts left out (the archive's own folder).
//
// Errors:
//
//	When a download fails, the checksum differs or the archive cannot be unpacked.
func fetchVerified(ctx context.Context, url, dest string, strip int) error {
	sums, err := getText(ctx, url+".sha256")
	if err != nil {
		return err
	}
	fields := strings.Fields(sums)
	if len(fields) == 0 {
		return fmt.Errorf("%s.sha256 is empty", url)
	}
	return fetchSum(ctx, url, fields[0], dest, strip)
}

// fetchSum downloads a .tar.gz to a temporary file, checks its SHA-256 and unpacks it.
//
// Args:
//
//	ctx: the context, to cancel.
//	url: the archive.
//	sum: its SHA-256, in hexadecimal.
//	dest: the folder it is unpacked into.
//	strip: the leading path parts left out.
//
// Errors:
//
//	When the download fails, the checksum differs or the archive cannot be unpacked.
func fetchSum(ctx context.Context, url, sum, dest string, strip int) error {
	body, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	tmp, err := os.CreateTemp("", "ansibleforms-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), body); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sum) {
		return fmt.Errorf("the checksum of %s is %s, not %s", url, got, sum)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return untar(tmp, dest, strip)
}

// untar unpacks a .tar.gz into a folder: directories, files with their modes, and links. A
// path out of the folder is never written.
//
// Args:
//
//	r: the archive.
//	dest: the folder.
//	strip: the leading path parts left out.
//
// Errors:
//
//	When the archive is broken or a file cannot be written.
func untar(r io.Reader, dest string, strip int) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root := filepath.Clean(dest) + string(os.PathSeparator)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		parts := strings.Split(strings.TrimPrefix(h.Name, "./"), "/")
		if len(parts) <= strip {
			continue
		}
		rel := filepath.Join(parts[strip:]...)
		if rel == "" || rel == "." {
			continue
		}
		target := filepath.Join(dest, rel)
		if !strings.HasPrefix(target, root) {
			continue
		}
		mode := os.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // archives checked against their checksum
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// a link may point anywhere inside the archive, never out of the folder
			resolved := filepath.Join(filepath.Dir(target), h.Linkname)
			if filepath.IsAbs(h.Linkname) || !strings.HasPrefix(resolved, root) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			src := filepath.Join(dest, filepath.Join(strings.Split(h.Linkname, "/")[min(strip, len(strings.Split(h.Linkname, "/"))-1):]...))
			if !strings.HasPrefix(src, root) {
				continue
			}
			_ = os.Remove(target)
			if err := os.Link(src, target); err != nil {
				return err
			}
		}
	}
}
