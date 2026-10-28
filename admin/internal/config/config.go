// Package config is the schema of /boot/firmware/osaid.yaml, the single source of
// truth for which simulators exist, how they are built and run, and the optional
// Wi-Fi uplink. The admin service renders systemd units from it.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

type Config struct {
	Version    int         `json:"version" yaml:"version"`
	Network    Network     `json:"network" yaml:"network"`
	Wifi       Wifi        `json:"wifi" yaml:"wifi"`
	Defaults   Defaults    `json:"defaults" yaml:"defaults"`
	Simulators []Simulator `json:"simulators" yaml:"simulators"`
}

type Network struct {
	Address string `yaml:"address"` // CIDR of the Pi on usb0; informational (set by the gadget service)
}

type Wifi struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	SSID    string `json:"ssid" yaml:"ssid"`
	PSK     string `json:"psk" yaml:"psk"`
	Country string `json:"country" yaml:"country"`
}

type Defaults struct {
	User         string   `json:"user" yaml:"user"`
	Capabilities []string `json:"capabilities" yaml:"capabilities"`
	MemoryMax    string   `json:"memory_max" yaml:"memory_max"`
	Restart      string   `json:"restart" yaml:"restart"`
}

type Source struct {
	GitHub string `yaml:"github,omitempty"` // owner/repo, used for display, zip matching and "fetch"
	Ref    string `yaml:"ref,omitempty"`    // default ref for "fetch from GitHub"
}

type Build struct {
	Kind    string `yaml:"kind"`              // go | prebuilt | script
	Package string `yaml:"package,omitempty"` // go: package path within the tree (default ".")
	Output  string `yaml:"output,omitempty"`  // go: binary name (default: sim name)
	Script  string `yaml:"script,omitempty"`  // script: relative path run inside the tree
}

type Simulator struct {
	Name         string            `json:"name" yaml:"name"`
	Title        string            `json:"title" yaml:"title,omitempty"`
	Source       Source            `json:"source" yaml:"source,omitempty"`
	Build        Build             `json:"build" yaml:"build"`
	Exec         string            `json:"exec" yaml:"exec"`
	Flags        string            `json:"flags" yaml:"flags,omitempty"`
	Env          map[string]string `json:"env" yaml:"env,omitempty"`
	UIPort       int               `json:"ui_port" yaml:"ui_port,omitempty"`
	Exclusive    string            `json:"exclusive" yaml:"exclusive,omitempty"`
	Autostart    bool              `json:"autostart" yaml:"autostart,omitempty"`
	User         string            `json:"user" yaml:"user,omitempty"`
	Capabilities []string          `json:"capabilities" yaml:"capabilities,omitempty"`
	MemoryMax    string            `json:"memory_max" yaml:"memory_max,omitempty"`
	Restart      string            `json:"restart" yaml:"restart,omitempty"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Effective returns the simulator with defaults folded in.
func (c *Config) Effective(s Simulator) Simulator {
	if s.User == "" {
		s.User = c.Defaults.User
	}
	if s.User == "" {
		s.User = "root"
	}
	if len(s.Capabilities) == 0 {
		s.Capabilities = c.Defaults.Capabilities
	}
	if s.MemoryMax == "" {
		s.MemoryMax = c.Defaults.MemoryMax
	}
	if s.Restart == "" {
		s.Restart = c.Defaults.Restart
	}
	if s.Restart == "" {
		s.Restart = "always"
	}
	if s.Build.Package == "" {
		s.Build.Package = "."
	}
	if s.Build.Output == "" {
		s.Build.Output = s.Name
	}
	if s.Title == "" {
		s.Title = s.Name
	}
	return s
}

func (c *Config) Find(name string) (Simulator, bool) {
	for _, s := range c.Simulators {
		if s.Name == name {
			return c.Effective(s), true
		}
	}
	return Simulator{}, false
}

// Peers returns the names of the other simulators in s's exclusive group.
func (c *Config) Peers(s Simulator) []string {
	var out []string
	if s.Exclusive == "" {
		return out
	}
	for _, o := range c.Simulators {
		if o.Name != s.Name && o.Exclusive == s.Exclusive {
			out = append(out, o.Name)
		}
	}
	sort.Strings(out)
	return out
}

func (c *Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("version must be %d", CurrentVersion)
	}
	seen := map[string]bool{}
	ports := map[int]string{}
	for i, s := range c.Simulators {
		if !nameRE.MatchString(s.Name) {
			return fmt.Errorf("simulators[%d]: name %q must match %s", i, s.Name, nameRE)
		}
		if seen[s.Name] {
			return fmt.Errorf("simulators[%d]: duplicate name %q", i, s.Name)
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Exec) == "" {
			return fmt.Errorf("simulator %q: exec is required", s.Name)
		}
		switch s.Build.Kind {
		case "go", "prebuilt", "script":
		case "":
			return fmt.Errorf("simulator %q: build.kind is required (go, prebuilt or script)", s.Name)
		default:
			return fmt.Errorf("simulator %q: unknown build.kind %q", s.Name, s.Build.Kind)
		}
		if s.Build.Kind == "script" && s.Build.Script == "" {
			return fmt.Errorf("simulator %q: build.script is required for kind script", s.Name)
		}
		if s.UIPort != 0 {
			if s.UIPort < 1 || s.UIPort > 65535 || s.UIPort == 80 {
				return fmt.Errorf("simulator %q: ui_port %d is not usable", s.Name, s.UIPort)
			}
			if o, dup := ports[s.UIPort]; dup {
				return fmt.Errorf("simulator %q: ui_port %d already used by %q", s.Name, s.UIPort, o)
			}
			ports[s.UIPort] = s.Name
		}
		for k := range s.Env {
			if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(k) {
				return fmt.Errorf("simulator %q: bad env name %q", s.Name, k)
			}
		}
		if strings.ContainsAny(s.Flags, "\n\r") {
			return fmt.Errorf("simulator %q: flags must be a single line", s.Name)
		}
	}
	if c.Wifi.Enabled && c.Wifi.SSID == "" {
		return fmt.Errorf("wifi: ssid is required when enabled")
	}
	if c.Wifi.Country != "" && !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(c.Wifi.Country) {
		return fmt.Errorf("wifi: country must be a two-letter code")
	}
	return nil
}

func Parse(data []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func Load(path string) (*Config, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	c, err := Parse(data)
	return c, data, err
}

// Save writes raw YAML atomically. The boot volume is FAT, so rename within the
// same directory is the best we can do; a temp file plus rename still avoids a
// half-written file on power loss.
func Save(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path), ".osaid.yaml.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Marshal(c *Config) ([]byte, error) {
	return yaml.Marshal(c)
}

// Default is the configuration shipped in the image.
const Default = `# OSAID simulator Pi configuration.
# Edit from the admin UI (http://10.55.0.1/) or directly on this boot volume.
# The admin service renders one systemd unit per simulator from this file.
version: 1

network:
  address: 10.55.0.1/24

# Optional Wi-Fi uplink so the Pi itself can reach the internet (fetch sources,
# NTP, apt). Off by default; the laptop's connection is never affected.
wifi:
  enabled: false
  ssid: ""
  psk: ""
  country: US

defaults:
  user: osaid
  capabilities: [CAP_NET_RAW, CAP_NET_ADMIN]
  memory_max: 160M
  restart: always

# exec runs through /bin/sh with:
#   PATH prefixed by the active build directory (so the binary is found by name)
#   OSAID_BUILD  = active build directory   OSAID_DATA = persistent state dir (cwd)
#   FLAGS        = the editable "flags" string below
simulators:
  - name: g7sim
    title: Dexcom G7 (g7sim)
    source: { github: LoopKit/g7sim, ref: main }
    build: { kind: go, package: ., output: g7sim }
    exec: g7sim -api :8081 ${FLAGS}
    flags: ""
    ui_port: 8081
    exclusive: radio

  - name: pod-dash
    title: Omnipod DASH (pod)
    source: { github: LoopKit/pod, ref: main }
    build: { kind: go, package: ., output: pod }
    exec: pod -api :8082 ${FLAGS}
    flags: -q
    ui_port: 8082
    exclusive: radio

  # LoopKit/pod branch "o5-sim-support": pod with an Omnipod 5 mode
  # (-mode o5|dash). Its control panel is built into the binary, like pod-dash's.
  - name: pod-five
    title: Omnipod 5 (pod, branch o5-sim-support)
    source: { github: LoopKit/pod, ref: o5-sim-support }
    build: { kind: go, package: ., output: pod }
    exec: pod -api :8084 -mode o5 ${FLAGS}
    flags: -q
    ui_port: 8084
    exclusive: radio

  - name: faketandem
    title: Tandem Mobi (faketandem)
    source: { github: jwoglom/faketandem, ref: main }
    build: { kind: go, package: ., output: faketandem }
    exec: faketandem -api-addr :8083 -ui-dir ${OSAID_BUILD}/ui ${FLAGS}
    flags: ""
    ui_port: 8083
    memory_max: 320M
    exclusive: radio
`
