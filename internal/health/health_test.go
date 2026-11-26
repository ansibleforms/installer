package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// app answers /api/v2/version as an app would, with the body given.
//
// Args:
//
//	t: the test.
//	tls: an https app (a self-signed certificate) rather than http.
//	status: the status it answers.
//	body: the body.
//
// Returns:
//
//	Its address.
func app(t *testing.T, tls bool, status int, body string) string {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/version" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestWait checks that it returns the version an app answers, in either of the two shapes the
// endpoint has had, a self-signed certificate taken.
func TestWait(t *testing.T) {
	for body, want := range map[string]string{`{"version":"7.3.0"}`: "7.3.0", `{"data":{"output":"7.2.1"}}`: "7.2.1"} {
		v, err := Wait(context.Background(), app(t, true, http.StatusOK, body), 10*time.Second)
		if err != nil || v != want {
			t.Errorf("%s: %q, %v", body, v, err)
		}
	}
}

// TestWaitGivesUp checks that an app that never answers 200 is waited for until the context
// ends, and one that is gone until the time is up, saying the last error.
func TestWaitGivesUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Wait(ctx, app(t, false, http.StatusServiceUnavailable, ""), time.Minute); err == nil {
		t.Error("a 503 app was taken")
	}
	if _, err := Wait(context.Background(), "http://127.0.0.1:1", time.Millisecond); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("a gone app: %v", err)
	}
}

// TestAppVersion checks that a self-signed app is refused as a certificate problem, any other
// failure is not one, and the insecure call trusts it.
func TestAppVersion(t *testing.T) {
	ctx := context.Background()
	self := app(t, true, http.StatusOK, `{"version":"7.3.0"}`)
	if _, certOK, err := AppVersion(ctx, self); err == nil || certOK {
		t.Errorf("a self-signed app: certOK %v, %v", certOK, err)
	}
	if v, _, err := AppVersionInsecure(ctx, self); err != nil || v != "7.3.0" {
		t.Errorf("trusted: %q, %v", v, err)
	}
	if v, certOK, err := AppVersion(ctx, app(t, false, http.StatusOK, `{"version":"7.4.0"}`)); err != nil || !certOK || v != "7.4.0" {
		t.Errorf("plain http: %q, %v, %v", v, certOK, err)
	}
	if _, certOK, err := AppVersion(ctx, app(t, false, http.StatusNotFound, "")); err == nil || !certOK {
		t.Errorf("a 404: certOK %v, %v", certOK, err)
	}
}

// TestVersionBadAddress checks an address that is none.
func TestVersionBadAddress(t *testing.T) {
	if _, _, err := AppVersion(context.Background(), "http://\x7f"); err == nil {
		t.Error("an address that is none")
	}
}
