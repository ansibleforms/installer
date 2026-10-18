package detect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ─── Docker and Podman ────────────────────────────────────────────────────────

// container is what docker inspect and podman inspect both say of a container.
type container struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
		Env    []string          `json:"Env"`
	} `json:"Config"`
	State struct {
		Status string `json:"Status"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

// the labels compose puts on its containers (Docker Compose and podman-compose alike)
const (
	labelProject = "com.docker.compose.project"
	labelDir     = "com.docker.compose.project.working_dir"
	labelFiles   = "com.docker.compose.project.config_files"
	labelService = "com.docker.compose.service"
	labelVersion = "org.opencontainers.image.version"
)

// name is the container's name without the slash Docker puts before it.
func (c container) name() string { return strings.TrimPrefix(c.Name, "/") }

// env reads a variable of the container's environment.
func (c container) env(key string) string {
	for _, e := range c.Config.Env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

// published is the first port of the host published for the container.
//
// Returns:
//
//	The port, 0 when none is published.
func (c container) published() int {
	keys := make([]string, 0, len(c.NetworkSettings.Ports))
	for k := range c.NetworkSettings.Ports {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, b := range c.NetworkSettings.Ports[k] {
			if p := atoi(b.HostPort); p != 0 {
				return p
			}
		}
	}
	return 0
}

// containers finds the AnsibleForms containers of an engine, running or stopped, grouped by
// compose project.
//
// Args:
//
//	ctx: the context, to cancel.
//	env: the commands and the probe.
//	engine: docker or podman.
//
// Returns:
//
//	One installation per compose project (or per container run without compose).
func containers(ctx context.Context, env Env, engine string) []Install {
	if !env.Has(engine) {
		return nil
	}
	out, err := env.run(ctx, engine, "ps", "-a", "-q", "--no-trunc")
	if err != nil {
		return nil // no daemon, no rights: nothing to say about it
	}
	ids := strings.Fields(out)
	if len(ids) == 0 {
		return nil
	}
	out, err = env.run(ctx, engine, append([]string{"inspect"}, ids...)...)
	if err != nil {
		return nil
	}
	var all []container
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		return nil
	}
	// the containers of a project together : an app container's project brings its database
	groups := map[string][]container{}
	var order []string
	for _, c := range all {
		key := c.Config.Labels[labelProject]
		if key == "" {
			key = "container:" + c.ID
		} else {
			key = "project:" + c.Config.Labels[labelDir] + ":" + key
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], c)
	}
	var found []Install
	for _, key := range order {
		if i, ok := project(ctx, env, engine, groups[key]); ok {
			found = append(found, i)
		}
	}
	return found
}

// project makes an installation of a compose project's containers, when one of them runs the
// app's or an RTE's image.
//
// Args:
//
//	ctx: the context, to cancel.
//	env: the probe.
//	engine: docker or podman.
//	group: the project's containers (or a container alone).
//
// Returns:
//
//	The installation, and false when no container is AnsibleForms.
func project(ctx context.Context, env Env, engine string, group []container) (Install, bool) {
	var app, rte *container
	services := map[string]string{} // service -> container name
	for n := range group {
		c := &group[n]
		switch imageKind(c.Config.Image) {
		case "app":
			if app == nil {
				app = c
			}
		case "rte":
			if rte == nil {
				rte = c
			}
		}
		if s := c.Config.Labels[labelService]; s != "" {
			services[s] = c.name()
		}
	}
	main := app
	if main == nil {
		main = rte
	}
	if main == nil {
		return Install{}, false
	}
	labels := main.Config.Labels
	i := Install{Engine: engine, Role: RoleController, Project: labels[labelProject], Dir: labels[labelDir], key: "container:" + main.ID}
	if app == nil {
		i.Role = RoleRunner
	}
	for _, c := range group {
		i.Containers = append(i.Containers, c.name())
		if c.State.Status == "running" {
			i.Running = true
		}
	}
	sort.Strings(i.Containers)
	suffix := map[string]string{RoleController: "_app", RoleRunner: "_rte"}[i.Role]
	i.Name = strings.TrimSuffix(main.name(), suffix)

	// the port and HTTPS from the project's .env, else what the container publishes
	envFile := map[string]string(nil)
	if i.Dir != "" {
		envFile = readEnv(filepath.Join(i.Dir, ".env"))
	}
	if i.Role == RoleController {
		i.Port, i.HTTPS = hostPort(envFile["WEBAPP_LOCAL_PORT"]), envFile["HTTPS"] == "1"
	} else {
		i.Port, i.HTTPS = hostPort(envFile["RTE_LOCAL_PORT"]), envFile["HTTPS"] == "1"
	}
	if i.Port == 0 {
		i.Port = main.published()
		if i.Role == RoleController {
			i.HTTPS = main.env("HTTPS") == "1"
		}
	}

	// the version : the image's tag, else its label, else what the app answers
	_, tag := splitImage(main.Config.Image)
	i.Version = fullVersion(tag)
	if i.Version == "" {
		i.Version = fullVersion(labels[labelVersion])
	}
	if i.Version == "" && i.Role == RoleController && main.State.Status == "running" {
		if a := env.probe(ctx, main.published()); a.App {
			i.Version = fullVersion(a.Version)
		}
	}
	i.Problem = composeProblem(i, main, services, envFile)
	return i, true
}

// composeProblem tells why a compose installation cannot be taken over, if it cannot. The
// upgrade replaces the compose file with the docker repository's (a controller) or the
// installer's Runner file, keeps the .env and the data, and backs up through the mysqldb
// service: anything else would be guessed.
//
// Args:
//
//	i: the installation found.
//	main: its app container (its RTE for a Runner).
//	services: the project's services and their containers' names.
//	envFile: its .env, nil when it cannot be read.
//
// Returns:
//
//	The problem's code, "" when there is none.
func composeProblem(i Install, main *container, services map[string]string, envFile map[string]string) string {
	if i.Project == "" || i.Dir == "" {
		return NotCompose
	}
	composeFile := filepath.Join(i.Dir, "docker-compose.yml")
	content, err := os.ReadFile(composeFile)
	if err != nil || envFile == nil {
		return NoComposeFile
	}
	// started from another file (-f) than the folder's docker-compose.yml
	if files := main.Config.Labels[labelFiles]; files != "" && filepath.Clean(strings.Split(files, ",")[0]) != composeFile {
		return NotRepoLayout
	}
	if i.Role == RoleRunner {
		if len(services) != 1 || services["rte"] != i.Name+"_rte" || envFile["RTE_MODE"] == "" || envFile["RTE_LOCAL_PORT"] == "" {
			return RunnerLayout
		}
	} else {
		if services["app"] != i.Name+"_app" || services["rte"] != i.Name+"_rte" || !strings.Contains(string(content), "ghcr.io/ansibleforms/") {
			return NotRepoLayout
		}
		if _, ok := services["mysqldb"]; !ok {
			return ExternalDB
		}
		if services["mysqldb"] != i.Name+"_db" {
			return NotRepoLayout
		}
	}
	if i.Version == "" {
		return UnknownVersion
	}
	return ""
}
