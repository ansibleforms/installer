package detect

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// ─── the ports ────────────────────────────────────────────────────────────────

// Answer is what answers on a port of this server.
type Answer struct {
	// App: the app answers its version endpoint; RTE: an RTE answers its liveness probe and
	// asks its token for the rest.
	App, RTE bool
	// Version is the app's version ("" for an RTE: it tells it only with its token).
	Version string
	// HTTPS: it answered over HTTPS.
	HTTPS bool
}

// ProbePort asks a port of this server what it is, over HTTPS then HTTP, with GET requests
// that need no sign-in and change nothing: the app's /api/v2/version, else an RTE's /live
// (with /rte/v1/health asking for its token, so another service's /live is not taken for
// one).
//
// Args:
//
//	ctx: the context, to cancel (its deadline bounds the probe).
//	port: the port.
//
// Returns:
//
//	What answers; nothing when neither does.
func ProbePort(ctx context.Context, port int) Answer {
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // only asks what answers on this server
		// a redirect (http to https, a login page) is not an answer
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, scheme := range []string{"https", "http"} {
		base := scheme + "://127.0.0.1:" + strconv.Itoa(port)
		status, body, err := get(ctx, client, base+"/api/v2/version")
		if err != nil {
			continue // not this scheme (or nothing listens)
		}
		if status == http.StatusOK {
			var v struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(body, &v) == nil && v.Version != "" {
				return Answer{App: true, Version: v.Version, HTTPS: scheme == "https"}
			}
		}
		status, body, err = get(ctx, client, base+"/live")
		if err != nil || status != http.StatusOK {
			return Answer{}
		}
		var live struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &live) != nil || live.Status != "live" {
			return Answer{}
		}
		if status, _, err = get(ctx, client, base+"/rte/v1/health"); err == nil && status == http.StatusUnauthorized {
			return Answer{RTE: true, HTTPS: scheme == "https"}
		}
		return Answer{}
	}
	return Answer{}
}

// get sends a GET and reads a little of the answer.
//
// Args:
//
//	ctx: the context, to cancel.
//	client: the HTTP client.
//	url: the address.
//
// Returns:
//
//	The status and the body (64 KiB at most).
//
// Errors:
//
//	When nothing answers (or not in this scheme).
func get(ctx context.Context, client *http.Client, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "ansibleforms-installer")
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	return res.StatusCode, body, nil
}
