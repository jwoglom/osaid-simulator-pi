package builds

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwoglom/osaid-simulator-pi/admin/internal/config"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/units"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	_ = w.Close()
	return buf.Bytes()
}

func TestUnzipStripsTopDir(t *testing.T) {
	z := makeZip(t, map[string]string{"repo-main/go.mod": "module github.com/x/repo\n", "repo-main/main.go": "package main\nfunc main(){}\n", "repo-main/ui/index.html": "<html>"})
	zp := filepath.Join(t.TempDir(), "a.zip")
	_ = os.WriteFile(zp, z, 0o644)
	dst := filepath.Join(t.TempDir(), "src")
	top, err := unzip(zp, dst)
	if err != nil {
		t.Fatal(err)
	}
	if top != "repo-main" {
		t.Errorf("top = %q", top)
	}
	if _, err := os.Stat(filepath.Join(dst, "ui", "index.html")); err != nil {
		t.Error("ui/index.html missing")
	}
}

func TestUnzipRejectsTraversal(t *testing.T) {
	z := makeZip(t, map[string]string{"../evil": "x"})
	zp := filepath.Join(t.TempDir(), "a.zip")
	_ = os.WriteFile(zp, z, 0o644)
	if _, err := unzip(zp, filepath.Join(t.TempDir(), "src")); err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadBuildsAndActivates(t *testing.T) {
	cfg, _ := config.Parse([]byte(config.Default))
	sim, _ := cfg.Find("g7sim")
	state := t.TempDir()
	m := &Manager{Paths: units.Paths{StateDir: state, UnitDir: filepath.Join(state, "u")}, Mock: true, Jobs: 1}
	z := makeZip(t, map[string]string{
		"g7sim-main/go.mod":  "module github.com/loopkit/g7sim\n\ngo 1.20\n",
		"g7sim-main/main.go": "package main\nfunc main(){}\n",
	})
	// wrong repo is refused
	bad := makeZip(t, map[string]string{"pod-main/go.mod": "module github.com/avereha/pod\n", "pod-main/main.go": "package main\nfunc main(){}\n"})
	if _, err := m.Upload(context.Background(), cfg, sim, "pod-main.zip", bytes.NewReader(bad), false); err == nil {
		t.Fatal("expected module mismatch error")
	}
	b, err := m.Upload(context.Background(), cfg, sim, "g7sim-main.zip", bytes.NewReader(z), false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		got, _ := m.Get(sim.Name, b.ID)
		if !got.Running {
			if !got.OK {
				log, _ := os.ReadFile(m.LogPath(sim.Name, b.ID))
				t.Fatalf("build failed: %s\n%s", got.Error, log)
			}
			if !got.Current {
				t.Fatal("successful build is not current")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("build timed out")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(m.Paths.Current(sim.Name), "g7sim")); err != nil {
		t.Fatal("binary missing from current:", err)
	}
	if err := m.Delete(sim.Name, b.ID); err == nil {
		t.Fatal("deleting the active build must fail")
	}
}
