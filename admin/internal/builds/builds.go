// Package builds turns an uploaded repository zip into an active simulator build.
//
// Layout under <state>/sims/<name>/:
//
//	uploads/<sha256>.zip          the zip as received
//	builds/<id>/src/              unpacked tree (single top-level dir stripped)
//	builds/<id>/build.json        metadata, see Build
//	builds/<id>/build.log         compiler output
//	current -> builds/<id>/src    the active build (atomic symlink swap)
//
// On the Pi each build runs inside a transient systemd unit with a memory cap
// and the worst OOM score on the box, so a compile can never take the admin or
// a running simulator down with it. With -mock the command runs directly.
package builds

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jwoglom/osaid-simulator-pi/admin/internal/config"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/units"
)

const (
	maxZipBytes      = 512 << 20 // uncompressed cap
	keepBuilds       = 3         // besides current
	buildMemoryMax   = "350M"
	buildSwapMax     = "1G"
	manifestFileName = "osaid-manifest.json"
)

type Build struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Kind     string    `json:"kind"`
	ZipName  string    `json:"zip_name"`
	SHA256   string    `json:"sha256"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
	Running  bool      `json:"running"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Binary   string    `json:"binary,omitempty"`
	Current  bool      `json:"current"`
	Seconds  float64   `json:"seconds,omitempty"`
}

// Manifest is the optional file at the root of a prebuilt zip.
type Manifest struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Binary string `json:"binary"`
}

type Manager struct {
	Paths   units.Paths
	GoBin   string // /opt/osaid/go/bin/go
	Mock    bool
	Jobs    int // go build -p
	mu      sync.Mutex
	running string // "<sim>/<id>" while a build runs
}

func (m *Manager) simDir(sim string) string { return m.Paths.SimDir(sim) }
func (m *Manager) buildDir(sim, id string) string {
	return filepath.Join(m.simDir(sim), "builds", id)
}

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// Running reports the build in progress, if any.
func (m *Manager) Running() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// List returns the builds of a simulator, newest first.
func (m *Manager) List(sim string) ([]Build, error) {
	dirs, _ := filepath.Glob(filepath.Join(m.simDir(sim), "builds", "*", "build.json"))
	cur := m.currentID(sim)
	var out []Build
	for _, p := range dirs {
		var b Build
		data, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(data, &b) != nil {
			continue
		}
		b.Current = b.ID == cur
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

func (m *Manager) Get(sim, id string) (Build, error) {
	if !idRE.MatchString(id) {
		return Build{}, errors.New("bad build id")
	}
	data, err := os.ReadFile(filepath.Join(m.buildDir(sim, id), "build.json"))
	if err != nil {
		return Build{}, err
	}
	var b Build
	if err := json.Unmarshal(data, &b); err != nil {
		return Build{}, err
	}
	b.Current = b.ID == m.currentID(sim)
	return b, nil
}

func (m *Manager) currentID(sim string) string {
	target, err := os.Readlink(m.Paths.Current(sim))
	if err != nil {
		return ""
	}
	// builds/<id>/src or an absolute path to it
	parts := strings.Split(filepath.ToSlash(target), "/")
	for i, p := range parts {
		if p == "builds" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// Current describes the active build, if any.
func (m *Manager) Current(sim string) *Build {
	id := m.currentID(sim)
	if id == "" {
		return nil
	}
	b, err := m.Get(sim, id)
	if err != nil {
		return nil
	}
	return &b
}

func (m *Manager) save(sim string, b *Build) error {
	data, _ := json.MarshalIndent(b, "", "  ")
	return os.WriteFile(filepath.Join(m.buildDir(sim, b.ID), "build.json"), data, 0o644)
}

// Upload stores a zip, unpacks it and starts the build asynchronously.
// It returns once the build has been accepted; progress is in the build log.
func (m *Manager) Upload(ctx context.Context, cfg *config.Config, sim config.Simulator, zipName string, r io.Reader, force bool) (*Build, error) {
	m.mu.Lock()
	if m.running != "" {
		m.mu.Unlock()
		return nil, fmt.Errorf("a build is already running (%s); wait for it to finish", m.running)
	}
	m.running = sim.Name + "/…"
	m.mu.Unlock()
	b, err := m.prepare(sim, zipName, r, force)
	if err != nil {
		m.mu.Lock()
		m.running = ""
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Lock()
	m.running = sim.Name + "/" + b.ID
	m.mu.Unlock()
	go m.run(sim, b)
	return b, nil
}

func (m *Manager) prepare(sim config.Simulator, zipName string, r io.Reader, force bool) (*Build, error) {
	upDir := filepath.Join(m.simDir(sim.Name), "uploads")
	if err := os.MkdirAll(upDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(upDir, "upload-*.zip")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	if n == 0 {
		os.Remove(tmp.Name())
		return nil, errors.New("empty upload")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	zipPath := filepath.Join(upDir, sum+".zip")
	os.Remove(zipPath)
	if err := os.Rename(tmp.Name(), zipPath); err != nil {
		return nil, err
	}
	id := sum[:12]
	dir := m.buildDir(sim.Name, id)
	if _, err := os.Stat(dir); err == nil {
		if id == m.currentID(sim.Name) && !force {
			return nil, fmt.Errorf("this exact zip is already the active build (%s)", id)
		}
		os.RemoveAll(dir)
	}
	src := filepath.Join(dir, "src")
	top, err := unzip(zipPath, src)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("unzip: %w", err)
	}
	b := &Build{ID: id, ZipName: filepath.Base(zipName), SHA256: sum, Started: time.Now(), Running: true, Kind: sim.Build.Kind}
	b.Label = top
	if b.Label == "" {
		b.Label = strings.TrimSuffix(filepath.Base(zipName), ".zip")
	}
	// prebuilt zips announce themselves
	if mf, err := readManifest(src); err == nil {
		b.Kind = "prebuilt"
		if mf.Label != "" {
			b.Label = mf.Label
		}
		if mf.Name != "" && mf.Name != sim.Name && !force {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("zip is a prebuilt %q, not %q (use force to override)", mf.Name, sim.Name)
		}
		b.Binary = mf.Binary
	} else if b.Kind == "go" {
		if err := checkModule(src, sim, force); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	if err := m.save(sim.Name, b); err != nil {
		return nil, err
	}
	return b, nil
}

func readManifest(src string) (Manifest, error) {
	var mf Manifest
	data, err := os.ReadFile(filepath.Join(src, manifestFileName))
	if err != nil {
		return mf, err
	}
	err = json.Unmarshal(data, &mf)
	return mf, err
}

// checkModule compares the zip's go.mod module path with the configured source
// repo. Module paths and GitHub owners drift (pod's module is avereha/pod under
// LoopKit/pod), so only the repository name is compared, case-insensitively.
func checkModule(src string, sim config.Simulator, force bool) error {
	data, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		return fmt.Errorf("no go.mod at the root of the zip: is this the repository's \"Download ZIP\"? (%v)", err)
	}
	mod := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "module ") {
			mod = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	if sim.Source.GitHub == "" || force || mod == "" {
		return nil
	}
	want := strings.ToLower(filepath.Base(sim.Source.GitHub))
	got := strings.ToLower(filepath.Base(mod))
	if want != got {
		return fmt.Errorf("zip is module %q but simulator %q expects repository %s (use force to override)", mod, sim.Name, sim.Source.GitHub)
	}
	return nil
}

// unzip extracts into dst, stripping a single top-level directory, and returns
// that directory's name (the GitHub zip convention: repo-branch or repo-sha).
func unzip(zipPath, dst string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	top := ""
	single := true
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				return "", fmt.Errorf("unsafe path in zip: %q", f.Name)
			}
		}
		first, _, _ := strings.Cut(name, "/")
		if top == "" {
			top = first
		} else if first != top {
			single = false
		}
		if !strings.Contains(name, "/") && !f.FileInfo().IsDir() {
			single = false
		}
	}
	if !single {
		top = ""
	}
	var total uint64
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "./")
		if top != "" {
			name = strings.TrimPrefix(strings.TrimPrefix(name, top), "/")
		}
		if name == "" {
			continue
		}
		clean := filepath.Clean(name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return "", fmt.Errorf("unsafe path in zip: %q", f.Name)
		}
		p := filepath.Join(dst, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return "", err
			}
			continue
		}
		total += f.UncompressedSize64
		if total > maxZipBytes {
			return "", fmt.Errorf("zip exceeds %d MB uncompressed", maxZipBytes>>20)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if f.Mode()&os.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			target, _ := io.ReadAll(rc)
			rc.Close()
			os.Remove(p)
			if err := os.Symlink(string(target), p); err != nil {
				return "", err
			}
			continue
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := extractFile(f, p, mode|0o444); err != nil {
			return "", err
		}
	}
	return top, nil
}

func extractFile(f *zip.File, p string, mode os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	w, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, rc)
	return err
}

// run executes the build, then activates it on success.
func (m *Manager) run(sim config.Simulator, b *Build) {
	defer func() {
		m.mu.Lock()
		m.running = ""
		m.mu.Unlock()
	}()
	dir := m.buildDir(sim.Name, b.ID)
	src := filepath.Join(dir, "src")
	logf, err := os.Create(filepath.Join(dir, "build.log"))
	if err != nil {
		m.finish(sim.Name, b, err)
		return
	}
	defer logf.Close()
	fmt.Fprintf(logf, "== %s: build %s (%s) from %s\n", sim.Name, b.ID, b.Label, b.ZipName)
	var berr error
	switch b.Kind {
	case "prebuilt":
		if b.Binary == "" {
			b.Binary = sim.Build.Output
		}
		berr = ensureExecutable(filepath.Join(src, b.Binary))
	case "go":
		b.Binary = sim.Build.Output
		berr = m.goBuild(src, sim, logf)
	case "script":
		b.Binary = sim.Build.Output
		berr = m.exec(src, logf, nil, "/bin/sh", "-e", sim.Build.Script)
		if berr == nil {
			berr = ensureExecutable(filepath.Join(src, b.Binary))
		}
	default:
		berr = fmt.Errorf("unknown build kind %q", b.Kind)
	}
	if berr == nil {
		berr = m.activate(sim.Name, b.ID)
	}
	if berr != nil {
		fmt.Fprintf(logf, "== FAILED: %v\n", berr)
	} else {
		fmt.Fprintf(logf, "== OK: %s is now the active build of %s\n", b.ID, sim.Name)
	}
	m.finish(sim.Name, b, berr)
	if berr == nil {
		m.prune(sim.Name)
	}
}

func (m *Manager) finish(sim string, b *Build, err error) {
	b.Running = false
	b.Finished = time.Now()
	b.Seconds = b.Finished.Sub(b.Started).Seconds()
	b.OK = err == nil
	if err != nil {
		b.Error = err.Error()
	}
	_ = m.save(sim, b)
}

func ensureExecutable(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("binary %s not found after build", filepath.Base(p))
	}
	return os.Chmod(p, st.Mode()|0o755)
}

func (m *Manager) goBuild(src string, sim config.Simulator, logw io.Writer) error {
	goBin := m.GoBin
	if m.Mock {
		if p, err := exec.LookPath("go"); err == nil {
			goBin = p
		}
	}
	jobs := m.Jobs
	if jobs < 1 {
		jobs = 1
	}
	env := []string{
		"GOOS=linux", "GOARCH=arm", "GOARM=6", "CGO_ENABLED=0",
		"GOTOOLCHAIN=local",
		"GOMAXPROCS=" + strconv.Itoa(jobs),
		"GOCACHE=" + filepath.Join(m.Paths.StateDir, "gocache"),
		"GOPATH=" + filepath.Join(m.Paths.StateDir, "gopath"),
		"HOME=" + m.Paths.StateDir,
		"PATH=" + filepath.Dir(goBin) + ":/usr/local/bin:/usr/bin:/bin",
	}
	if _, err := os.Stat(filepath.Join(src, "vendor")); err == nil {
		env = append(env, "GOFLAGS=-mod=vendor")
	}
	out := filepath.Join(src, sim.Build.Output)
	fmt.Fprintf(logw, "== go build -p %d -o %s %s\n", jobs, sim.Build.Output, sim.Build.Package)
	if err := m.exec(src, logw, env, goBin, "build", "-p", strconv.Itoa(jobs), "-trimpath", "-o", out, sim.Build.Package); err != nil {
		return err
	}
	return ensureExecutable(out)
}

// exec runs argv in dir with env, inside a transient memory-capped unit on the Pi.
func (m *Manager) exec(dir string, logw io.Writer, env []string, argv ...string) error {
	var cmd *exec.Cmd
	if m.Mock {
		cmd = exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
	} else {
		args := []string{"--wait", "--pipe", "--collect", "--quiet",
			"--unit=osaid-build-" + strconv.FormatInt(time.Now().UnixNano(), 36),
			"-p", "MemoryMax=" + buildMemoryMax, "-p", "MemorySwapMax=" + buildSwapMax,
			"-p", "OOMScoreAdjust=1000", "-p", "CPUWeight=50", "-p", "Nice=10",
			"--working-directory=" + dir}
		for _, e := range env {
			args = append(args, "--setenv="+e)
		}
		args = append(args, "--")
		args = append(args, argv...)
		cmd = exec.Command("systemd-run", args...)
	}
	cmd.Stdout = logw
	cmd.Stderr = logw
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build command failed: %w", err)
	}
	return nil
}

// activate points current at a build, atomically.
func (m *Manager) activate(sim, id string) error {
	src := filepath.Join(m.buildDir(sim, id), "src")
	if _, err := os.Stat(src); err != nil {
		return err
	}
	link := m.Paths.Current(sim)
	tmp := link + ".tmp"
	os.Remove(tmp)
	if err := os.Symlink(src, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// Activate switches to an existing successful build (rollback / roll forward).
func (m *Manager) Activate(sim, id string) error {
	b, err := m.Get(sim, id)
	if err != nil {
		return err
	}
	if !b.OK {
		return errors.New("that build did not succeed")
	}
	return m.activate(sim, id)
}

func (m *Manager) Delete(sim, id string) error {
	if !idRE.MatchString(id) {
		return errors.New("bad build id")
	}
	if id == m.currentID(sim) {
		return errors.New("cannot delete the active build")
	}
	if strings.HasPrefix(m.Running(), sim+"/"+id) {
		return errors.New("build is running")
	}
	return os.RemoveAll(m.buildDir(sim, id))
}

func (m *Manager) prune(sim string) {
	list, _ := m.List(sim)
	kept := 0
	for _, b := range list {
		if b.Current || b.Running {
			continue
		}
		kept++
		if kept > keepBuilds {
			os.RemoveAll(m.buildDir(sim, b.ID))
			os.Remove(filepath.Join(m.simDir(sim), "uploads", b.SHA256+".zip"))
		}
	}
}

// LogPath is the build log file for streaming.
func (m *Manager) LogPath(sim, id string) string {
	return filepath.Join(m.buildDir(sim, id), "build.log")
}

var refRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,128}$`)

// Fetch downloads <owner>/<repo>/archive/<ref>.zip from GitHub (needs the Wi-Fi
// uplink) and feeds it to Upload.
func (m *Manager) Fetch(ctx context.Context, cfg *config.Config, sim config.Simulator, ref string, force bool) (*Build, error) {
	if sim.Source.GitHub == "" {
		return nil, errors.New("simulator has no source.github configured")
	}
	if ref == "" {
		ref = sim.Source.Ref
	}
	if ref == "" {
		ref = "main"
	}
	if !refRE.MatchString(ref) || strings.Contains(ref, "..") {
		return nil, errors.New("bad ref")
	}
	url := fmt.Sprintf("https://github.com/%s/archive/%s.zip", sim.Source.GitHub, ref)
	client := &http.Client{Timeout: 15 * time.Minute}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w (is the Wi-Fi uplink up?)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: HTTP %s", url, resp.Status)
	}
	name := filepath.Base(sim.Source.GitHub) + "-" + strings.ReplaceAll(ref, "/", "-") + ".zip"
	return m.Upload(ctx, cfg, sim, name, resp.Body, force)
}
