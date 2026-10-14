package units

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwoglom/osaid-simulator-pi/admin/internal/config"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/system"
)

func TestRenderAndSync(t *testing.T) {
	cfg, err := config.Parse([]byte(config.Default))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := Paths{UnitDir: filepath.Join(dir, "units"), StateDir: filepath.Join(dir, "state")}
	g7, _ := cfg.Find("g7sim")
	u := Render(cfg, g7, p)
	for _, want := range []string{
		"Conflicts=bluetooth.service osaid-sim-faketandem.service osaid-sim-pod-dash.service osaid-sim-pod-five.service",
		"AmbientCapabilities=CAP_NET_RAW CAP_NET_ADMIN",
		"ExecStartPre=+/usr/sbin/rfkill unblock bluetooth",
		"KillMode=control-group",
		"MemoryMax=160M",
		"OOMScoreAdjust=500",
		"User=osaid",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
	m := &Manager{Run: system.NewMock(), Paths: p}
	if err := m.Sync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"g7sim", "pod-dash", "pod-five", "faketandem"} {
		if _, err := os.Stat(filepath.Join(p.UnitDir, UnitName(n))); err != nil {
			t.Errorf("unit for %s not written: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(p.SimDir(n), "run.sh")); err != nil {
			t.Errorf("run.sh for %s not written: %v", n, err)
		}
	}
	if got := m.Flags("pod-dash"); got != "-q" {
		t.Errorf("pod flags = %q", got)
	}
	if err := m.SetFlags("pod-dash", "-v"); err != nil {
		t.Fatal(err)
	}
	if got := m.Flags("pod-dash"); got != "-v" {
		t.Errorf("pod flags after set = %q", got)
	}
	// removing a simulator removes its unit
	cfg.Simulators = cfg.Simulators[:3]
	if err := m.Sync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.UnitDir, UnitName("faketandem"))); !os.IsNotExist(err) {
		t.Errorf("stale unit kept")
	}
}
