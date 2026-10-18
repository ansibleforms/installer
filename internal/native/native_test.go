package native

import (
	"strings"
	"testing"
)

// an inbound Runner's RTE serves HTTPS with the data folder's certificate ; the others HTTP
func TestRteEnvHTTPS(t *testing.T) {
	l := layout{dir: "/opt/ansibleforms-runner", name: "runner"}
	env := strings.Join(rteEnv(l, true), "\n")
	for _, want := range []string{
		"HTTPS=1",
		"HTTPS_CERT=/opt/ansibleforms-runner/data/certificates/cert.pem",
		"HTTPS_KEY=/opt/ansibleforms-runner/data/certificates/key.pem",
	} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("rteEnv(https) lacks %s", want)
		}
	}
	if strings.Contains(env, "HTTPS=0") {
		t.Error("rteEnv(https) still sets HTTPS=0")
	}
	plain := strings.Join(rteEnv(l, false), "\n")
	if !strings.Contains(plain, "HTTPS=0\n") || strings.Contains(plain, "HTTPS_CERT") {
		t.Errorf("rteEnv(http) = %s", plain)
	}
}
