package detect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// the app answers its version endpoint; an RTE its liveness probe and asks its token for
// the rest; another service's /live is not an RTE; nothing listening is nothing
func TestProbePort(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s: only GET may be sent", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/api/v2/version" {
			_, _ = w.Write([]byte(`{"version":"7.2.0","server":{"gitSha":"dev"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer app.Close()
	rte := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live":
			_, _ = w.Write([]byte(`{"status":"live"}`))
		case "/rte/v1/health":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer rte.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/live" {
			_, _ = w.Write([]byte(`{"status":"live"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer other.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if a := ProbePort(ctx, serverPort(t, app)); !a.App || a.Version != "7.2.0" || a.HTTPS {
		t.Errorf("app: %+v", a)
	}
	if a := ProbePort(ctx, serverPort(t, rte)); !a.RTE || a.App || !a.HTTPS {
		t.Errorf("rte: %+v", a)
	}
	if a := ProbePort(ctx, serverPort(t, other)); a.App || a.RTE {
		t.Errorf("another service: %+v", a)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	port := serverPort(t, closed)
	closed.Close()
	if a := ProbePort(ctx, port); a.App || a.RTE {
		t.Errorf("nothing listening: %+v", a)
	}
}

// TestProbePortNotOne checks a redirect, a port answering 404 everywhere, and a /live that is
// not an RTE's, all nothing ; and a request that cannot be made.
func TestProbePortNotOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for name, h := range map[string]http.HandlerFunc{
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", http.StatusFound) },
		"404":      http.NotFound,
		"live":     func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"status":"starting"}`)) },
	} {
		srv := httptest.NewServer(h)
		if a := ProbePort(ctx, serverPort(t, srv)); a != (Answer{}) {
			t.Errorf("%s: %+v", name, a)
		}
		srv.Close()
	}
	if _, _, err := get(ctx, http.DefaultClient, "http://\x7f"); err == nil {
		t.Error("a request that cannot be made")
	}
}
