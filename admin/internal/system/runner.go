// Package system wraps the host commands the admin depends on (systemctl,
// systemd-run, journalctl, nmcli, date …) behind a Runner so the service can be
// exercised on a developer machine with -mock.
package system

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type Runner interface {
	// Run executes name args… and returns combined stdout; stderr is folded into
	// the error on failure.
	Run(ctx context.Context, name string, args ...string) (string, error)
	// Start launches a long-running command whose stdout+stderr stream to w.
	// The returned func stops it.
	Stream(ctx context.Context, w io.Writer, name string, args ...string) error
	Mock() bool
}

type Exec struct{}

func (Exec) Mock() bool { return false }

func (Exec) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (Exec) Stream(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// MockRunner pretends to be a Raspberry Pi: it records commands, keeps a map of
// "active" units, and answers the queries the admin makes.
type MockRunner struct {
	mu     sync.Mutex
	active map[string]bool
	Log    []string
}

func NewMock() *MockRunner { return &MockRunner{active: map[string]bool{}} }

func (m *MockRunner) Mock() bool { return true }

func (m *MockRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	m.Log = append(m.Log, line)
	log.Printf("mock: %s", line)
	switch name {
	case "systemctl":
		if len(args) == 0 {
			return "", nil
		}
		switch args[0] {
		case "start", "restart":
			u := args[len(args)-1]
			// honour Conflicts= for osaid-sim units: only one radio user at a time
			if strings.HasPrefix(u, "osaid-sim-") {
				for k := range m.active {
					if strings.HasPrefix(k, "osaid-sim-") {
						delete(m.active, k)
					}
				}
			}
			m.active[u] = true
		case "stop", "kill":
			m.active[args[len(args)-1]] = false
		case "show":
			u := args[len(args)-1]
			st, sub, pid, mem := "inactive", "dead", "0", "[not set]"
			if m.active[u] {
				st, sub, pid, mem = "active", "running", "4242", "23068672"
			}
			return fmt.Sprintf("ActiveState=%s\nSubState=%s\nMainPID=%s\nMemoryCurrent=%s\nNTPSynchronized=no\n", st, sub, pid, mem), nil
		case "is-active":
			if m.active[args[len(args)-1]] {
				return "active\n", nil
			}
			return "inactive\n", fmt.Errorf("inactive")
		}
		return "", nil
	case "systemd-run":
		// run the payload directly, after the "--" separator
		for i, a := range args {
			if a == "--" && i+1 < len(args) {
				return Exec{}.Run(ctx, args[i+1], args[i+2:]...)
			}
		}
		return "", nil
	case "nmcli":
		if len(args) > 0 && args[0] == "-t" {
			return "GENERAL.STATE:100 (connected)\nGENERAL.CONNECTION:mock-wifi\nIP4.ADDRESS[1]:192.168.1.50/24\n", nil
		}
		return "", nil
	case "timedatectl":
		return "NTPSynchronized=no\nTimezone=UTC\n", nil
	case "rfkill", "date", "fake-hwclock", "raspi-config", "hostname":
		return "", nil
	}
	return "", nil
}

func (m *MockRunner) Stream(ctx context.Context, w io.Writer, name string, args ...string) error {
	fmt.Fprintf(w, "[mock] %s %s\n", name, strings.Join(args, " "))
	if name == "journalctl" {
		fmt.Fprintln(w, "[mock] no journal on this machine; on the Pi this streams the unit's log")
	}
	<-ctx.Done()
	return nil
}

// Hostname is a tiny helper used by status.
func Hostname() string {
	h, _ := os.Hostname()
	return h
}
