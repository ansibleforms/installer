// Package kube installs, upgrades and backs up AnsibleForms on Kubernetes with its Helm
// chart (ansibleforms/helm-charts), through the helm and kubectl commands.
package kube

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/ui"
)

// Chart is the chart's OCI address; its versions follow the app's (7.3.0).
const Chart = "oci://ghcr.io/ansibleforms/charts/ansibleforms"

// Options are the answers of a Kubernetes installation.
type Options struct {
	Version   string
	Namespace string
	Release   string
	// Hostname is the address the ingress serves (ansibleforms.example.com).
	Hostname string
	// IngressClass is the ingress controller's class (nginx, traefik).
	IngressClass string
	// TLS asks the ingress for TLS (its certificate from cert-manager or the controller's default).
	TLS bool
	// StorageClass is the volumes' class ("" for the cluster's default).
	StorageClass string
	// ExtraValues is a values file of the person's own, applied after the installer's
	// (an external database, resources, node selectors).
	ExtraValues string
}

// IngressClasses lists the cluster's ingress classes.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Returns:
//
//	Their names; none when the cluster has none or cannot tell.
func IngressClasses(ctx context.Context) []string {
	out, err := system.Output(ctx, "", "kubectl", "get", "ingressclass", "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// StorageClasses lists the cluster's storage classes.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Returns:
//
//	Their names; none when the cluster has none or cannot tell.
func StorageClasses(ctx context.Context) []string {
	out, err := system.Output(ctx, "", "kubectl", "get", "storageclass", "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// values are the chart values the installer sets: secrets made by the chart (and kept on
// upgrades), the ingress, and volumes any cluster offers (ReadWriteOnce, one replica).
//
// Args:
//
//	o: the answers.
//
// Returns:
//
//	The values, as a values file.
//
// Errors:
//
//	When they cannot be written as YAML.
func values(o Options) ([]byte, error) {
	storage := func() map[string]any {
		m := map[string]any{"accessMode": "ReadWriteOnce"}
		if o.StorageClass != "" {
			m["className"] = o.StorageClass
		}
		return m
	}
	v := map[string]any{
		"secrets": map[string]any{"generate": true},
		"ingress": map[string]any{
			"enabled":   true,
			"hostname":  o.Hostname,
			"className": o.IngressClass,
			"tls":       map[string]any{"enabled": o.TLS},
		},
		"storages": map[string]any{"mysql": storage(), "server": storage()},
	}
	head := []byte("# Chart values written by the AnsibleForms installer; the upgrades apply them again.\n")
	body, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(head, body...), nil
}

// valuesPath is where the installer keeps an installation's values file, next to its state.
//
// Args:
//
//	release: the Helm release.
//
// Returns:
//
//	The path.
func valuesPath(release string) string {
	return filepath.Join(filepath.Dir(state.Path()), release+"-values.yaml")
}

// helmArgs are the arguments of a helm upgrade --install at a version.
//
// Args:
//
//	st: the installation.
//	version: the chart's version.
//
// Returns:
//
//	The arguments.
func helmArgs(st *state.State, version string, extra string) []string {
	args := []string{
		"upgrade", "--install", st.Release, Chart, "--version", version,
		"--namespace", st.Namespace, "--create-namespace",
		"--values", st.ValuesFile, "--wait", "--timeout", "15m",
	}
	if extra != "" {
		args = append(args, "--values", extra)
	}
	return args
}

// Install installs the chart and waits until its pods are ready.
//
// Args:
//
//	ctx: the context, to cancel.
//	o: the answers.
//
// Returns:
//
//	The installation's state (saved).
//
// Errors:
//
//	When helm fails (a chart version not published yet included).
func Install(ctx context.Context, o Options) (*state.State, error) {
	st := &state.State{
		Mode: state.Kubernetes, Role: "controller", Version: o.Version,
		Namespace: o.Namespace, Release: o.Release, ValuesFile: valuesPath(o.Release),
		InstalledAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	scheme := "https"
	if !o.TLS {
		scheme = "http"
	}
	st.URL = scheme + "://" + o.Hostname
	data, err := values(o)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(st.ValuesFile), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(st.ValuesFile, data, 0o600); err != nil {
		return nil, err
	}
	ui.Step(i18n.T("kube.installing", "release", o.Release, "namespace", o.Namespace))
	if err := system.Run(ctx, "", "helm", helmArgs(st, o.Version, o.ExtraValues)...); err != nil {
		return nil, chartHint(err, o.Version)
	}
	return st, st.Save()
}

// Adopt takes over a release the installer did not install: the values it runs with now
// (helm get values) become its values file, which the upgrades apply again.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation found (its ValuesFile set here; saved by the caller).
//
// Errors:
//
//	When helm cannot tell its values or the file cannot be written.
func Adopt(ctx context.Context, st *state.State) error {
	st.ValuesFile = valuesPath(st.Release)
	ui.Step(i18n.T("kube.adoptValues", "release", st.Release, "file", st.ValuesFile))
	out, err := system.Output(ctx, "", "helm", "get", "values", st.Release, "--namespace", st.Namespace, "--output", "yaml")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "null" {
		out = "{}"
	}
	head := "# Chart values of the release as the installer found it; the upgrades apply them again.\n"
	if err := os.MkdirAll(filepath.Dir(st.ValuesFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(st.ValuesFile, []byte(head+out+"\n"), 0o600)
}

// chartHint explains a chart version that is not there yet: the chart follows each app
// release a little later (its own pull request and tests).
//
// Args:
//
//	err: helm's error.
//	version: the version asked for.
//
// Returns:
//
//	The error, with the hint when it fits.
func chartHint(err error, version string) error {
	if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "manifest unknown") {
		return fmt.Errorf("%w\n%s", err, i18n.T("kube.chartNotYet", "version", version))
	}
	return err
}

// mysqlDeploy is the chart's MySQL Deployment of a release.
//
// Args:
//
//	st: the installation.
//
// Returns:
//
//	Its name, as kubectl names it.
func mysqlDeploy(st *state.State) string {
	return "deploy/" + st.Release + "-mysql"
}

// the dump and the restore, inside the MySQL pod (its root password is in its environment)
const (
	dumpScript    = `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -uroot --single-transaction --routines --triggers --add-drop-database --databases AnsibleForms`
	restoreScript = `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot`
)

// Backup dumps the database of the chart's MySQL into a file on this machine.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//
// Returns:
//
//	The dump's path.
//
// Errors:
//
//	When the dump fails (an external database is backed up by its own means).
func Backup(ctx context.Context, st *state.State) (string, error) {
	dir := filepath.Join(filepath.Dir(state.Path()), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dump := filepath.Join(dir, fmt.Sprintf("%s-%s-%s.sql", st.Release, st.Version, time.Now().Format("20060102-150405")))
	ui.Step(i18n.T("compose.dumping"))
	if err := system.ToFile(ctx, "", dump, "kubectl", "-n", st.Namespace, "exec", mysqlDeploy(st), "--", "sh", "-c", dumpScript); err != nil {
		return "", errors.New(i18n.T("kube.backupFailed", "error", err))
	}
	return dump, nil
}

// Upgrade moves the release to another chart version (the app's), after a backup.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (saved with the new version).
//	to: the version.
//
// Returns:
//
//	The dump made before, for Rollback ("" when none could be made and the person went on).
//
// Errors:
//
//	When helm fails.
func Upgrade(ctx context.Context, st *state.State, to string) (string, error) {
	dump, err := Backup(ctx, st)
	if err != nil {
		return "", err
	}
	ui.Done(i18n.T("compose.backedUp", "file", dump))
	ui.Step(i18n.T("kube.upgrading", "release", st.Release, "version", to))
	if err := system.Run(ctx, "", "helm", helmArgs(st, to, "")...); err != nil {
		return dump, chartHint(err, to)
	}
	st.Version = to
	st.UpdatedAt = time.Now().UTC()
	return dump, st.Save()
}

// Rollback goes back to the release's previous revision and loads the dump made before the
// upgrade.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	previous: the version before.
//	dump: the dump made before the upgrade.
//
// Errors:
//
//	When a step fails.
func Rollback(ctx context.Context, st *state.State, previous, dump string) error {
	ui.Step(i18n.T("compose.rollingBack", "version", previous))
	if err := system.Run(ctx, "", "helm", "rollback", st.Release, "--namespace", st.Namespace, "--wait", "--timeout", "15m"); err != nil {
		return err
	}
	if dump != "" {
		ui.Step(i18n.T("compose.restoring", "file", dump))
		if err := system.FromFile(ctx, "", dump, "kubectl", "-n", st.Namespace, "exec", "-i", mysqlDeploy(st), "--", "sh", "-c", restoreScript); err != nil {
			return err
		}
		_ = system.Run(ctx, "", "kubectl", "-n", st.Namespace, "rollout", "restart", "deploy/"+st.Release+"-server")
	}
	st.Version = previous
	return st.Save()
}

// Status shows the release and its pods.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//
// Errors:
//
//	When helm cannot tell.
func Status(ctx context.Context, st *state.State) error {
	if err := system.Run(ctx, "", "helm", "status", st.Release, "--namespace", st.Namespace); err != nil {
		return err
	}
	return system.Run(ctx, "", "kubectl", "-n", st.Namespace, "get", "pods", "-l", "app.kubernetes.io/instance="+st.Release)
}

// Uninstall removes the release; the chart keeps its Secret (and the volumes stay unless the
// namespace goes too).
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	purge: also delete the namespace (volumes and the kept Secret with it).
//
// Errors:
//
//	When helm fails.
func Uninstall(ctx context.Context, st *state.State, purge bool) error {
	if err := system.Run(ctx, "", "helm", "uninstall", st.Release, "--namespace", st.Namespace, "--wait"); err != nil {
		return err
	}
	if purge {
		if err := system.Run(ctx, "", "kubectl", "delete", "namespace", st.Namespace, "--wait"); err != nil {
			return err
		}
	}
	_ = os.Remove(st.ValuesFile)
	return state.Remove()
}
