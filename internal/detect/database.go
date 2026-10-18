package detect

import (
	"context"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ansibleforms/installer/internal/state"
)

// ─── a Runner of 7.2, in database mode ────────────────────────────────────────

// DatabaseModeVersion is the version a database-mode Runner is upgraded from when its image
// does not say (the floating tag 7 of the docker repository): database mode is how every
// RTE of 7.0 to 7.2 ran, and the move to a socket mode is the same from any of them.
const DatabaseModeVersion = "7.2.0"

// DatabaseRunner is what a Runner of 7.2 in the deprecated database mode says of itself:
// an RTE alone in its compose project, with the app's database settings (DB_*) and no
// RTE_MODE. Its upgrade moves it to a socket mode under the same runner, which its RTE proves
// with the same token: the app then moves the runner itself, without an approval.
type DatabaseRunner struct {
	// Container is its RTE container (af_rte).
	Container string
	// ComposeFile is the compose file its project was started from (docker-compose.yml in its
	// folder, or the first -f file).
	ComposeFile string
	// RunnerName is the name of its runner under Connections > Runners: RTE_NAME, else the one
	// a 7.2 RTE registered itself as (the host of RTE_URL, with its port when it is not 8000,
	// else rte-<hostname>-<PORT>).
	RunnerName string
	// NameKnown: the RTE registered itself (RTE_REGISTER is not 0), so RunnerName is its
	// runner's name; false: an admin added the runner by hand, under a name of their own.
	NameKnown bool
	// Token is its RTE_TOKEN, the token of its runner: never shown, never logged.
	Token string
	// URL is its RTE_URL, the address the app called it on ("" : its container's IP).
	URL string
	// Settings are the settings of its environment a socket-mode RTE still reads (carried),
	// copied to the new .env; their values may be secrets, only their names are shown.
	Settings map[string]string
}

// carried are the settings of a database-mode RTE that a socket-mode one still reads, and
// that the installer's Runner layout does not write itself: the playbooks at once, the drain
// on a stop, the output kept, the proxies, the CAs it trusts, the time zone and the metrics.
var carried = []string{
	"RTE_MAX_JOBS", "RTE_DRAIN_SECONDS", "PROCESS_MAX_BUFFER", "METRICS_TOKEN",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"NODE_EXTRA_CA_CERTS", "TZ", "LOG_TZ",
}

// runnerNameRe is a runner's name as the app accepts it from an RTE (RTE_NAME)
var runnerNameRe = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,250}$`)

// ValidRunnerName tells whether a name is one the app accepts as an RTE's runner (RTE_NAME).
//
// Args:
//
//	name: the name.
//
// Returns:
//
//	True when the app takes it.
func ValidRunnerName(name string) bool {
	return runnerNameRe.MatchString(name)
}

// databaseRunner reads a Runner's RTE container as a 7.2 database-mode one: the app's DB_*
// in its environment, and no RTE_MODE (or RTE_MODE=database).
//
// Args:
//
//	c: its RTE container.
//	dir: its compose project's folder ("" : not started with compose).
//
// Returns:
//
//	What its upgrade needs; nil when it is not in database mode.
func databaseRunner(c *container, dir string) *DatabaseRunner {
	mode := strings.ToLower(strings.TrimSpace(c.env("RTE_MODE")))
	if mode != "" && mode != "database" {
		return nil
	}
	if c.env("DB_HOST") == "" && c.env("DB_USER") == "" {
		return nil
	}
	d := &DatabaseRunner{
		Container: c.name(),
		Token:     c.env("RTE_TOKEN"),
		URL:       strings.TrimRight(strings.TrimSpace(c.env("RTE_URL")), "/"),
		NameKnown: strings.TrimSpace(c.env("RTE_REGISTER")) != "0",
		Settings:  map[string]string{},
	}
	d.RunnerName = registeredName(c.env("RTE_NAME"), d.URL, c.Config.Hostname, c.env("PORT"))
	if strings.TrimSpace(c.env("RTE_NAME")) != "" {
		d.NameKnown = true
	}
	for _, k := range carried {
		if v := c.env(k); v != "" {
			d.Settings[k] = v
		}
	}
	// the compose file it was started from : the first -f file, else the folder's
	if dir != "" {
		d.ComposeFile = filepath.Join(dir, "docker-compose.yml")
		if files := c.Config.Labels[labelFiles]; files != "" {
			if first := filepath.Clean(strings.Split(files, ",")[0]); strings.HasPrefix(first, filepath.Clean(dir)+string(filepath.Separator)) {
				d.ComposeFile = first
			}
		}
	}
	return d
}

// registeredName is the name a 7.2 RTE registered its runner under (rte/register.js of 7.2):
// RTE_NAME when it is set (7.3 reads it), else the host of RTE_URL with its port when that is
// not 8000, else its node id, rte-<hostname>-<PORT>.
//
// Args:
//
//	name: its RTE_NAME.
//	rteURL: its RTE_URL.
//	hostname: its container's host name.
//	port: its PORT ("" : 8000).
//
// Returns:
//
//	The runner's name.
func registeredName(name, rteURL, hostname, port string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	if u, err := url.Parse(rteURL); err == nil && u.Hostname() != "" && (u.Scheme == "http" || u.Scheme == "https") {
		if p := u.Port(); p != "" && p != "8000" {
			return u.Hostname() + "-" + p
		}
		return u.Hostname()
	}
	if port = strings.TrimSpace(port); port == "" {
		port = "8000"
	}
	return "rte-" + hostname + "-" + port
}

// FindDatabaseRunner finds again the database-mode Runner an installation was taken over as:
// its RTE container, still there, in the same folder and compose project.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (DatabaseMode).
//
// Returns:
//
//	The installation found with its Database set; nil when its container is gone.
func FindDatabaseRunner(ctx context.Context, st *state.State) *Install {
	project := st.Project
	if project == "" {
		project = st.Name
	}
	for _, i := range containers(ctx, defaultEnv(), st.Runtime) {
		if i.Database != nil && i.Project == project && filepath.Clean(i.Dir) == filepath.Clean(st.Dir) {
			return &i
		}
	}
	return nil
}
