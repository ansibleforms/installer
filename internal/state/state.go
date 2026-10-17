// Package state remembers what the installer installed, so an upgrade, a backup or an
// uninstall later on knows where and how without asking again.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Mode is how AnsibleForms runs.
type Mode string

const (
	// Compose: Docker Compose, on Docker or Podman.
	Compose Mode = "compose"
	// Kubernetes: the Helm chart, on a cluster.
	Kubernetes Mode = "kubernetes"
)

// State is one installation.
type State struct {
	// Mode is compose or kubernetes.
	Mode Mode `json:"mode"`
	// Role is controller (the app, its database and a local RTE) or runner (an RTE alone,
	// for a controller elsewhere).
	Role string `json:"role"`
	// Version is the AnsibleForms version installed.
	Version string `json:"version"`
	// InstalledAt and UpdatedAt are when it was installed and last upgraded.
	InstalledAt time.Time `json:"installedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`

	// Compose : the runtime (docker, podman), the folder, the instance name (its containers'
	// prefix), the web port and whether it serves HTTPS.
	Runtime string `json:"runtime,omitempty"`
	Dir     string `json:"dir,omitempty"`
	Name    string `json:"name,omitempty"`
	Port    int    `json:"port,omitempty"`
	HTTPS   bool   `json:"https,omitempty"`

	// Kubernetes : the namespace, the Helm release, the values file the installer wrote and
	// the address the ingress serves.
	Namespace  string `json:"namespace,omitempty"`
	Release    string `json:"release,omitempty"`
	ValuesFile string `json:"valuesFile,omitempty"`
	URL        string `json:"url,omitempty"`
}

// ErrNone says nothing was installed by the installer on this server (for this user).
var ErrNone = errors.New("no AnsibleForms installation found: run the installer to install one first")

// Path is where the state is kept: /etc/ansibleforms for root, else the user's own config.
//
// Returns:
//
//	The file's path.
func Path() string {
	if os.Geteuid() == 0 {
		return "/etc/ansibleforms/installer.json"
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "ansibleforms", "installer.json")
}

// Load reads the state.
//
// Returns:
//
//	The installation.
//
// Errors:
//
//	ErrNone when there is none, else when the file cannot be read.
func Load() (*State, error) {
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNone
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("reading %s: %w", Path(), err)
	}
	return &s, nil
}

// Save writes the state, readable by its owner only (it names where the secrets are).
//
// Errors:
//
//	When the file cannot be written.
func (s *State) Save() error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

// Remove forgets the installation (after an uninstall).
//
// Errors:
//
//	When the file exists and cannot be removed.
func Remove() error {
	err := os.Remove(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
