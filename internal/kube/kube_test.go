package kube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/fakebin"
	"github.com/ansibleforms/installer/internal/state"
	"gopkg.in/yaml.v3"
)

// TestValuesRunner checks a Runner's values : the chart's runner mode with the app it dials,
// its name, its token and its join token, its volume, and nothing of the app (no ingress).
func TestValuesRunner(t *testing.T) {
	data := values(Options{StorageClass: "fast", Runner: &RunnerOptions{
		AppURL: "https://forms.example.com", Name: "dc2-runner", JoinToken: "afj_x", Token: "t0123456789abcdef", TrustAppCert: true,
	}})
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

// ─── the cluster, faked ───────────────────────────────────────────────────────

// cluster fakes helm and kubectl, and keeps the state in a folder of the test's own.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The fake programs.
func cluster(t *testing.T) *fakebin.Bin {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root the state is /etc/ansibleforms/installer.json")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b := fakebin.New(t)
	b.Add(t, "helm", "")
	b.Add(t, "kubectl", "")
	return b
}

// TestClasses checks the ingress and storage classes listed, and none when kubectl fails.
func TestClasses(t *testing.T) {
	b := cluster(t)
	b.Add(t, "kubectl", `echo "nginx traefik"`)
	ctx := context.Background()
	if got := IngressClasses(ctx); strings.Join(got, ",") != "nginx,traefik" {
		t.Errorf("ingress %v", got)
	}
	if got := StorageClasses(ctx); len(got) != 2 {
		t.Errorf("storage %v", got)
	}
	b.Add(t, "kubectl", "exit 1")
	if IngressClasses(ctx) != nil || StorageClasses(ctx) != nil {
		t.Error("kubectl failed")
	}
}

// TestValuesController checks a controller's values: secrets made by the chart, the ingress
// and its TLS, and the default storage class.
func TestValuesController(t *testing.T) {
	var v map[string]any
	if err := yaml.Unmarshal(values(Options{Hostname: "forms.example.com", IngressClass: "nginx", TLS: true}), &v); err != nil {
		t.Fatal(err)
	}
	in := v["ingress"].(map[string]any)
	if in["hostname"] != "forms.example.com" || in["tls"].(map[string]any)["enabled"] != true {
		t.Errorf("ingress %v", in)
	}
	if _, ok := v["storages"].(map[string]any)["mysql"].(map[string]any)["className"]; ok {
		t.Error("a class set without one")
	}
}

// TestInstall checks a controller and a Runner installed with helm, their values file and
// state written, and a chart not published yet explained.
func TestInstall(t *testing.T) {
	b := cluster(t)
	ctx := context.Background()
	st, err := Install(ctx, Options{Version: "7.3.0", Namespace: "forms", Release: "af", Hostname: "forms.example.com", ExtraValues: "/x.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if st.URL != "http://forms.example.com" || !b.Called("helm upgrade --install af "+Chart+" --version 7.3.0") || !b.Called("helm upgrade --install af "+Chart+" --version 7.3.0 --namespace forms --create-namespace --values "+st.ValuesFile+" --wait --timeout 15m --values /x.yaml") {
		t.Errorf("state %+v, calls %q", st, b.Calls())
	}
	if _, err := state.Load(); err != nil {
		t.Errorf("state not saved: %v", err)
	}
	r, err := Install(ctx, Options{Version: "7.3.0", Namespace: "r", Release: "r", TLS: true, Runner: &RunnerOptions{AppURL: "https://app"}})
	if err != nil || r.Role != "runner" || r.URL != "https://app" {
		t.Errorf("runner %+v, %v", r, err)
	}
	b.Add(t, "helm", "echo 'Error: manifest unknown' >&2; exit 1")
	if _, err := Install(ctx, Options{Version: "9.9.9", Namespace: "f", Release: "f"}); err == nil || !strings.Contains(err.Error(), "9.9.9") {
		t.Errorf("no chart yet: %v", err)
	}
}

// TestInstallUnwritable checks a values file that cannot be written, and its folder.
func TestInstallUnwritable(t *testing.T) {
	cluster(t)
	ctx := context.Background()
	dir := filepath.Dir(state.Path())
	if err := os.MkdirAll(filepath.Join(dir, "af-values.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(ctx, Options{Release: "af"}); err == nil {
		t.Error("a values file that is a folder")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	t.Setenv("XDG_CONFIG_HOME", file)
	if _, err := Install(ctx, Options{Release: "af"}); err == nil {
		t.Error("a folder under a file")
	}
	if err := Adopt(ctx, &state.State{Release: "af"}); err == nil {
		t.Error("adopt: a folder under a file")
	}
}

// TestAdopt checks the values the release runs with written down, {} for none, and helm's
// failure.
func TestAdopt(t *testing.T) {
	b := cluster(t)
	ctx := context.Background()
	b.Add(t, "helm", `echo "ingress: {enabled: true}"`)
	st := &state.State{Release: "af", Namespace: "forms"}
	if err := Adopt(ctx, st); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(st.ValuesFile)
	if !strings.Contains(string(data), "ingress: {enabled: true}") {
		t.Errorf("values %s", data)
	}
	b.Add(t, "helm", "echo null")
	if err := Adopt(ctx, st); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(st.ValuesFile); !strings.HasSuffix(string(data), "{}\n") {
		t.Errorf("no values %s", data)
	}
	b.Add(t, "helm", "exit 1")
	if err := Adopt(ctx, st); err == nil {
		t.Error("helm failed")
	}
}

// TestChartHint checks the hint only for a chart that is not there.
func TestChartHint(t *testing.T) {
	plain := errors.New("timed out")
	if chartHint(plain, "7.3.0") != plain {
		t.Error("another error got the hint")
	}
	if err := chartHint(errors.New("chart not found"), "7.3.0"); !errors.Is(err, err) || !strings.Contains(err.Error(), "7.3.0") {
		t.Errorf("hint %v", err)
	}
}

// TestBackupUpgradeRollback checks the dump, the upgrade that makes one first, the rollback
// that loads it, and each failure.
func TestBackupUpgradeRollback(t *testing.T) {
	b := cluster(t)
	ctx := context.Background()
	b.Add(t, "kubectl", `case "$*" in *mysqldump*) echo "-- dump" ;; esac`)
	st := &state.State{Release: "af", Namespace: "forms", Version: "7.2.0"}
	dump, err := Upgrade(ctx, st, "7.3.0")
	if err != nil || st.Version != "7.3.0" || !strings.HasPrefix(filepath.Base(dump), "af-7.2.0-") {
		t.Fatalf("upgrade %s, %v", dump, err)
	}
	if err := Rollback(ctx, st, "7.2.0", dump); err != nil || st.Version != "7.2.0" || !b.Called("kubectl -n forms rollout restart deploy/af-server") {
		t.Errorf("rollback %v", err)
	}
	if err := Rollback(ctx, st, "7.2.0", ""); err != nil {
		t.Errorf("rollback without a dump: %v", err)
	}
	b.Add(t, "kubectl", `case "$*" in *"exec -i"*) exit 1 ;; esac`)
	if err := Rollback(ctx, st, "7.2.0", dump); err == nil {
		t.Error("the restore failed")
	}
	b.Add(t, "helm", "exit 1")
	if err := Rollback(ctx, st, "7.2.0", dump); err == nil {
		t.Error("helm rollback failed")
	}
	if _, err := Upgrade(ctx, st, "7.3.0"); err == nil {
		t.Error("helm upgrade failed")
	}
	b.Add(t, "kubectl", "exit 1")
	if _, err := Upgrade(ctx, st, "7.3.0"); err == nil {
		t.Error("the dump failed")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	t.Setenv("XDG_CONFIG_HOME", file)
	if _, err := Backup(ctx, st); err == nil {
		t.Error("backups under a file")
	}
}

// TestStatusUninstall checks the status shown, the release removed with or without its
// namespace, and each failure.
func TestStatusUninstall(t *testing.T) {
	b := cluster(t)
	ctx := context.Background()
	st := &state.State{Release: "af", Namespace: "forms"}
	if err := Status(ctx, st); err != nil {
		t.Error(err)
	}
	if err := Uninstall(ctx, st, true); err != nil || !b.Called("kubectl delete namespace forms --wait") {
		t.Errorf("purge %v", err)
	}
	b.Add(t, "kubectl", "exit 1")
	if err := Uninstall(ctx, st, true); err == nil {
		t.Error("the namespace stayed")
	}
	if err := Uninstall(ctx, st, false); err != nil {
		t.Errorf("keep %v", err)
	}
	b.Add(t, "helm", "exit 1")
	if Status(ctx, st) == nil || Uninstall(ctx, st, false) == nil {
		t.Error("helm failed")
	}
}
