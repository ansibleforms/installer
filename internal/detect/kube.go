package detect

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// ─── Kubernetes ───────────────────────────────────────────────────────────────

// release is a line of helm list -o json.
type release struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Status     string `json:"status"`
	Chart      string `json:"chart"`
	AppVersion string `json:"app_version"`
}

// workloads is what kubectl get deploy,sts -o json says.
type workloads struct {
	Items []struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name        string            `json:"name"`
			Namespace   string            `json:"namespace"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	} `json:"items"`
}

// argoApp tells whether Argo CD manages a workload, and its Application's name when it says.
// Argo CD marks what it applies with the tracking-id annotation (app:group/kind:ns/name) or
// the argocd.argoproj.io/instance label ; with its default label tracking it leaves only the
// chart's app.kubernetes.io/instance and managed-by Helm, without a Helm release in the
// cluster (it renders the chart itself) : the caller decides that case, knowing the releases.
//
// Args:
//
//	labels: the workload's labels.
//	annotations: its annotations.
//
// Returns:
//
//	The Application's name ("" when unknown), and whether Argo CD marks it.
func argoApp(labels, annotations map[string]string) (string, bool) {
	if id := annotations["argocd.argoproj.io/tracking-id"]; id != "" {
		name, _, _ := strings.Cut(id, ":")
		return name, true
	}
	if name := labels["argocd.argoproj.io/instance"]; name != "" {
		return name, true
	}
	return "", false
}

// chartRe cuts helm's chart column (ansibleforms-7.2.0) into its name and its version
var chartRe = regexp.MustCompile(`^(.+)-v?(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)$`)

// kubernetes finds the Helm releases of the ansibleforms chart, and the Deployments and
// StatefulSets that run the app's or an RTE's image without one, on the cluster kubectl and
// helm reach (each asked with a short timeout: an unreachable cluster is skipped).
//
// Args:
//
//	ctx: the context, to cancel.
//	env: the commands.
//
// Returns:
//
//	One installation per release, and per workload no release explains.
func kubernetes(ctx context.Context, env Env) []Install {
	var found []Install
	byRelease := map[string]int{} // namespace/release -> index in found
	if env.Has("helm") {
		if out, err := env.run(ctx, "helm", "list", "--all-namespaces", "--all", "--output", "json"); err == nil {
			var list []release
			if json.Unmarshal([]byte(out), &list) == nil {
				for _, r := range list {
					m := chartRe.FindStringSubmatch(r.Chart)
					if m == nil || m[1] != "ansibleforms" {
						continue
					}
					v := fullVersion(r.AppVersion)
					if v == "" {
						v = m[2]
					}
					i := Install{Engine: Kubernetes, Role: RoleController, Version: v, Running: r.Status == "deployed",
						Namespace: r.Namespace, Release: r.Name, Chart: r.Chart, key: "helm:" + r.Namespace + "/" + r.Name}
					byRelease[r.Namespace+"/"+r.Name] = len(found)
					found = append(found, i)
				}
			}
		}
	}
	if env.Has("kubectl") {
		out, err := env.run(ctx, "kubectl", "get", "deployments,statefulsets", "--all-namespaces", "--output", "json", "--request-timeout=5s")
		var w workloads
		if err == nil && json.Unmarshal([]byte(out), &w) == nil {
			for _, item := range w.Items {
				kind, version := "", ""
				for _, c := range item.Spec.Template.Spec.Containers {
					k := imageKind(c.Image)
					if k == "" {
						continue
					}
					if kind != "app" {
						kind = k
					}
					if _, tag := splitImage(c.Image); version == "" || k == "app" {
						if v := fullVersion(tag); v != "" {
							version = v
						}
					}
				}
				if kind == "" {
					continue
				}
				md := item.Metadata
				name := shortKind(item.Kind) + "/" + md.Name
				instance := md.Labels["app.kubernetes.io/instance"]
				if n, ok := byRelease[md.Namespace+"/"+instance]; ok && instance != "" {
					found[n].Workloads = append(found[n].Workloads, name)
					continue
				}
				// workloads of one instance (the same label) together, else each alone
				group := instance
				if group == "" {
					group = md.Name
				}
				key := "kube:" + md.Namespace + "/" + group
				if n, ok := byRelease[key]; ok {
					found[n].Workloads = append(found[n].Workloads, name)
					if kind == "app" {
						found[n].Role = RoleController
					}
					if found[n].Version == "" {
						found[n].Version = version
					}
					found[n].Running = found[n].Running || item.Status.ReadyReplicas > 0
					continue
				}
				role := RoleController
				if kind != "app" {
					role = RoleRunner
				}
				// rendered from the chart (managed-by Helm) with no Helm release : Argo CD, which
				// renders the chart itself and tracks it by its instance label
				problem, app := NoHelm, ""
				if name, ok := argoApp(md.Labels, md.Annotations); ok {
					problem, app = ArgoCD, name
				} else if md.Labels["app.kubernetes.io/managed-by"] == "Helm" && instance != "" {
					problem, app = ArgoCD, instance
				}
				byRelease[key] = len(found)
				found = append(found, Install{Engine: Kubernetes, Role: role, Version: version, Running: item.Status.ReadyReplicas > 0,
					Namespace: md.Namespace, Release: group, Workloads: []string{name}, Problem: problem, ArgoApp: app, key: key})
			}
		}
	}
	for n := range found {
		sort.Strings(found[n].Workloads)
		if found[n].Problem == "" && found[n].Version == "" {
			found[n].Problem = UnknownVersion
		}
	}
	return found
}

// shortKind is kubectl's short name of a kind.
//
// Args:
//
//	kind: Deployment or StatefulSet.
//
// Returns:
//
//	deploy, sts, or the kind as it is.
func shortKind(kind string) string {
	switch kind {
	case "Deployment":
		return "deploy"
	case "StatefulSet":
		return "sts"
	}
	return kind
}
