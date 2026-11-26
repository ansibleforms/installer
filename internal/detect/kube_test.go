package detect

import (
	"context"
	"testing"

	"github.com/ansibleforms/installer/internal/state"
)

// a release of the chart is taken over; a StatefulSet by hand is found, not taken over; the
// chart's MySQL and another chart are not AnsibleForms
func TestFindHelmRelease(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}}
	helmRelease(t, f)
	got := find(context.Background(), f.env())
	if len(got) != 2 {
		t.Fatalf("found %+v, want 2", got)
	}
	rel, lab := got[1], got[0]
	if rel.Namespace != "forms" {
		rel, lab = lab, rel
	}
	if rel.Engine != Kubernetes || rel.Release != "ansibleforms" || rel.Version != "7.2.0" || !rel.Running || !rel.Adoptable() {
		t.Errorf("release: %+v", rel)
	}
	if len(rel.Workloads) != 1 || rel.Workloads[0] != "deploy/ansibleforms-server" {
		t.Errorf("release's workloads: %v", rel.Workloads)
	}
	st := rel.State()
	if st.Mode != state.Kubernetes || st.Namespace != "forms" || st.Release != "ansibleforms" || st.Role != RoleController {
		t.Errorf("state: %+v", st)
	}
	if lab.Problem != NoHelm || lab.Version != "7.1.4" || lab.Workloads[0] != "sts/forms-by-hand" || lab.Running {
		t.Errorf("by hand: %+v", lab)
	}
}

// workloads Argo CD manages are found, named after their Application, and never taken over:
// by its tracking annotation, and by the chart's labels without a Helm release (label tracking)
func TestFindArgoCD(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{"kubectl": true}, out: map[string]string{
		"kubectl get deployments,statefulsets": `{"items":[
 {"kind":"Deployment","metadata":{"name":"forms-server","namespace":"forms","labels":{"app.kubernetes.io/instance":"forms"},
  "annotations":{"argocd.argoproj.io/tracking-id":"ansibleforms-prod:apps/Deployment:forms/forms-server"}},
  "spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/ansibleforms/ansibleforms:7.3.0"}]}}},"status":{"readyReplicas":1}},
 {"kind":"Deployment","metadata":{"name":"lab-server","namespace":"lab","labels":{"app.kubernetes.io/instance":"lab-forms","app.kubernetes.io/managed-by":"Helm"}},
  "spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/ansibleforms/ansibleforms:7.2.0"}]}}},"status":{"readyReplicas":1}}
]}`,
	}}
	got := find(context.Background(), f.env())
	if len(got) != 2 {
		t.Fatalf("found %+v, want 2", got)
	}
	apps := map[string]string{}
	for _, i := range got {
		if i.Problem != ArgoCD || i.Adoptable() {
			t.Errorf("%s: problem %q, want argoCD and not taken over", i.Namespace, i.Problem)
		}
		apps[i.Namespace] = i.ArgoApp
	}
	if apps["forms"] != "ansibleforms-prod" || apps["lab"] != "lab-forms" {
		t.Errorf("applications %v", apps)
	}
}

// TestArgoApp checks Argo CD's marks: its tracking annotation first, then its instance label,
// and nothing for the chart's own labels.
func TestArgoApp(t *testing.T) {
	if name, ok := argoApp(map[string]string{"argocd.argoproj.io/instance": "label"}, map[string]string{"argocd.argoproj.io/tracking-id": "prod:apps/Deployment:ns/x"}); !ok || name != "prod" {
		t.Errorf("annotation: %s, %v", name, ok)
	}
	if name, ok := argoApp(map[string]string{"argocd.argoproj.io/instance": "label"}, nil); !ok || name != "label" {
		t.Errorf("label: %s, %v", name, ok)
	}
	if _, ok := argoApp(map[string]string{"app.kubernetes.io/instance": "forms"}, nil); ok {
		t.Error("the chart's labels taken for Argo CD")
	}
}

// TestChartAndKind checks helm's chart column cut into its name and version, and the short
// names of the workloads.
func TestChartAndKind(t *testing.T) {
	for chart, want := range map[string][2]string{
		"ansibleforms-7.2.0":      {"ansibleforms", "7.2.0"},
		"ansible-forms-v7.3.1":    {"ansible-forms", "7.3.1"},
		"ansibleforms-7.4.0-rc.1": {"ansibleforms", "7.4.0-rc.1"},
	} {
		m := chartRe.FindStringSubmatch(chart)
		if m == nil || m[1] != want[0] || m[2] != want[1] {
			t.Errorf("%s: %v", chart, m)
		}
	}
	if chartRe.MatchString("ansibleforms") {
		t.Error("a chart without a version")
	}
	if shortKind("Deployment") != "deploy" || shortKind("StatefulSet") != "sts" || shortKind("DaemonSet") != "DaemonSet" {
		t.Error("shortKind")
	}
}

// TestKubernetesGroups checks a release without an app version read from its chart, the
// workloads of one instance together (the app's making it a controller), and an RTE alone a
// Runner.
func TestKubernetesGroups(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{"helm": true, "kubectl": true}, out: map[string]string{
		"helm list": `[{"name":"forms","namespace":"a","status":"deployed","chart":"ansibleforms-7.4.1","app_version":""}]`,
		"kubectl get deployments,statefulsets": `{"items":[
 {"kind":"Deployment","metadata":{"name":"lab-rte","namespace":"lab","labels":{"app.kubernetes.io/instance":"lab"}},
  "spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/ansibleforms/ansibleforms-rte:latest"}]}}},"status":{"readyReplicas":0}},
 {"kind":"Deployment","metadata":{"name":"lab-app","namespace":"lab","labels":{"app.kubernetes.io/instance":"lab"}},
  "spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/ansibleforms/ansibleforms:7.2.0"}]}}},"status":{"readyReplicas":1}},
 {"kind":"StatefulSet","metadata":{"name":"solo-rte","namespace":"solo"},
  "spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/ansibleforms/ansibleforms-rte:7.2.0"}]}}},"status":{}}
]}`,
	}}
	got := kubernetes(context.Background(), f.env())
	if len(got) != 3 {
		t.Fatalf("found %+v", got)
	}
	if got[0].Version != "7.4.1" {
		t.Errorf("release version %q", got[0].Version)
	}
	lab := got[1]
	if lab.Role != RoleController || lab.Version != "7.2.0" || !lab.Running || len(lab.Workloads) != 2 {
		t.Errorf("lab %+v", lab)
	}
	if got[2].Role != RoleRunner || got[2].Release != "solo-rte" {
		t.Errorf("solo %+v", got[2])
	}
}
