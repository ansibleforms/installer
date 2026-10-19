package kube

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestValuesRunner checks a Runner's values : the chart's runner mode with the app it dials,
// its name, its token and its join token, its volume, and nothing of the app (no ingress).
func TestValuesRunner(t *testing.T) {
	data, err := values(Options{StorageClass: "fast", Runner: &RunnerOptions{
		AppURL: "https://forms.example.com", Name: "dc2-runner", JoinToken: "afj_x", Token: "t0123456789abcdef", TrustAppCert: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	r := v["runner"].(map[string]any)
	if r["enabled"] != true || r["appUrl"] != "https://forms.example.com" || r["name"] != "dc2-runner" || r["joinToken"] != "afj_x" || r["appTlsVerify"] != false {
		t.Fatalf("runner values = %v", r)
	}
	if tok := v["applications"].(map[string]any)["rte"].(map[string]any)["token"]; tok != "t0123456789abcdef" {
		t.Fatalf("rte token = %v", tok)
	}
	if v["storages"].(map[string]any)["server"].(map[string]any)["className"] != "fast" {
		t.Fatalf("storages = %v", v["storages"])
	}
	if _, ok := v["ingress"]; ok || !strings.Contains(string(data), "for a Runner") {
		t.Fatalf("a Runner has no ingress: %s", data)
	}
}
