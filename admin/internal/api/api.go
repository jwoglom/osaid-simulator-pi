// Package api is the HTTP surface of the admin service: JSON endpoints under
// /api and the embedded single-page UI at /.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jwoglom/osaid-simulator-pi/admin/internal/builds"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/config"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/sysinfo"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/system"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/units"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/wifi"
)

type Server struct {
	Version    string
	ConfigPath string
	JarsDir    string
	AdminBin   string
	Run        system.Runner
	Units      *units.Manager
	Builds     *builds.Manager
	Web        fs.FS

	mu  sync.RWMutex
	cfg *config.Config
	raw []byte
}

// Load reads the config (writing the default if absent) and applies it.
func (s *Server) Load(ctx context.Context) error {
	cfg, raw, err := config.Load(s.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		log.Printf("no config at %s; writing default", s.ConfigPath)
		if err := os.MkdirAll(filepath.Dir(s.ConfigPath), 0o755); err != nil {
			return err
		}
		if err := config.Save(s.ConfigPath, []byte(config.Default)); err != nil {
			return err
		}
		cfg, raw, err = config.Load(s.ConfigPath)
	}
	if err != nil {
		return err
	}
	return s.apply(ctx, cfg, raw, true)
}

// apply installs a validated config: units are re-rendered, Wi-Fi reconciled.
func (s *Server) apply(ctx context.Context, cfg *config.Config, raw []byte, wifiToo bool) error {
	if err := s.Units.Sync(ctx, cfg); err != nil {
		return fmt.Errorf("sync units: %w", err)
	}
	s.mu.Lock()
	s.cfg, s.raw = cfg, raw
	s.mu.Unlock()
	if wifiToo {
		if err := wifi.Apply(ctx, s.Run, cfg.Wifi); err != nil {
			log.Printf("wifi: %v", err)
		}
	}
	return nil
}

// Autostart starts simulators flagged autostart (boot only).
func (s *Server) Autostart(ctx context.Context) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	for _, sim := range cfg.Simulators {
		if sim.Autostart {
			if err := s.Units.Start(ctx, sim.Name); err != nil {
				log.Printf("autostart %s: %v", sim.Name, err)
			}
		}
	}
}

func (s *Server) config() (*config.Config, []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg, s.raw
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("PUT /api/wifi", s.putWifi)
	mux.HandleFunc("POST /api/time", s.postTime)
	mux.HandleFunc("POST /api/system/{action}", s.systemAction)
	mux.HandleFunc("POST /api/admin/upload", s.adminUpload)
	mux.HandleFunc("POST /api/jars", s.jarUpload)
	mux.HandleFunc("POST /api/sims/{name}/{action}", s.simAction)
	mux.HandleFunc("PUT /api/sims/{name}/flags", s.putFlags)
	mux.HandleFunc("GET /api/sims/{name}/logs", s.simLogs)
	mux.HandleFunc("GET /api/sims/{name}/builds", s.listBuilds)
	mux.HandleFunc("POST /api/sims/{name}/upload", s.upload)
	mux.HandleFunc("POST /api/sims/{name}/fetch", s.fetch)
	mux.HandleFunc("GET /api/sims/{name}/builds/{id}/log", s.buildLog)
	mux.HandleFunc("POST /api/sims/{name}/builds/{id}/activate", s.activateBuild)
	mux.HandleFunc("DELETE /api/sims/{name}/builds/{id}", s.deleteBuild)
	mux.Handle("GET /", http.FileServerFS(s.Web))
	return logRequests(mux)
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasSuffix(r.URL.Path, "/status") {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

type simStatus struct {
	config.Simulator
	Status  units.Status  `json:"status"`
	Flags   string        `json:"flags"`
	Current *builds.Build `json:"current_build"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.config()
	ctx := r.Context()
	var sims []simStatus
	for _, raw := range cfg.Simulators {
		sim := cfg.Effective(raw)
		sims = append(sims, simStatus{
			Simulator: sim,
			Status:    s.Units.Status(ctx, sim.Name),
			Flags:     s.Units.Flags(sim.Name),
			Current:   s.Builds.Current(sim.Name),
		})
	}
	writeJSON(w, 200, map[string]any{
		"version":       s.Version,
		"config_path":   s.ConfigPath,
		"system":        sysinfo.Collect(ctx, s.Run),
		"wifi":          wifi.Get(ctx, s.Run, cfg.Wifi),
		"build_running": s.Builds.Running(),
		"simulators":    sims,
	})
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	_, raw := s.config()
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	_, _ = w.Write(raw)
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.saveAndApply(r.Context(), cfg, raw); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "applied"})
}

func (s *Server) saveAndApply(ctx context.Context, cfg *config.Config, raw []byte) error {
	old, _ := s.config()
	if err := config.Save(s.ConfigPath, raw); err != nil {
		return err
	}
	wifiChanged := old == nil || old.Wifi != cfg.Wifi
	return s.apply(ctx, cfg, raw, wifiChanged)
}

func (s *Server) putWifi(w http.ResponseWriter, r *http.Request) {
	var in config.Wifi
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	_, raw := s.config()
	out, err := config.SetWifi(raw, in)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg, err := config.Parse(out)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.saveAndApply(r.Context(), cfg, out); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, wifi.Get(r.Context(), s.Run, cfg.Wifi))
}

func (s *Server) postTime(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Unix int64  `json:"unix"`
		TZ   string `json:"tz"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Unix < 1_600_000_000 {
		writeErr(w, 400, errors.New("need {unix, tz}"))
		return
	}
	info := sysinfo.Collect(r.Context(), s.Run)
	if info.NTPSynced {
		writeJSON(w, 200, map[string]any{"ok": "ntp", "now": time.Now()})
		return
	}
	drift := time.Since(time.Unix(in.Unix, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift < 2*time.Second {
		writeJSON(w, 200, map[string]any{"ok": "in sync", "now": time.Now()})
		return
	}
	if err := sysinfo.SetTime(r.Context(), s.Run, in.Unix, in.TZ); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": "set", "was_off_by_s": drift.Seconds(), "now": time.Now()})
}

func (s *Server) systemAction(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("action") {
	case "reboot", "poweroff":
		writeJSON(w, 200, map[string]string{"ok": r.PathValue("action")})
		go func() {
			time.Sleep(500 * time.Millisecond)
			_, _ = s.Run.Run(context.Background(), "systemctl", r.PathValue("action"))
		}()
	default:
		writeErr(w, 404, errors.New("unknown action"))
	}
}

func (s *Server) sim(r *http.Request) (config.Simulator, bool) {
	cfg, _ := s.config()
	return cfg.Find(r.PathValue("name"))
}

func (s *Server) simAction(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	var err error
	switch r.PathValue("action") {
	case "start":
		err = s.Units.Start(r.Context(), sim.Name)
	case "stop":
		err = s.Units.Stop(r.Context(), sim.Name)
	case "restart":
		err = s.Units.Restart(r.Context(), sim.Name)
	case "kill":
		err = s.Units.Kill(r.Context(), sim.Name)
	case "upload", "fetch":
		// routed separately; the mux prefers the literal patterns
		writeErr(w, 405, errors.New("method"))
		return
	default:
		writeErr(w, 404, errors.New("unknown action"))
		return
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s.Units.Status(r.Context(), sim.Name))
}

func (s *Server) putFlags(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	var in struct {
		Flags   string `json:"flags"`
		Restart bool   `json:"restart"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.Units.SetFlags(sim.Name, in.Flags); err != nil {
		writeErr(w, 400, err)
		return
	}
	if in.Restart && s.Units.Status(r.Context(), sim.Name).Active == "active" {
		_ = s.Units.Restart(r.Context(), sim.Name)
	}
	writeJSON(w, 200, map[string]string{"flags": s.Units.Flags(sim.Name)})
}

// sse prepares a Server-Sent Events response.
func sse(w http.ResponseWriter) (http.Flusher, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()
	return fl, true
}

type sseWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func (s sseWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		fmt.Fprintf(s.w, "data: %s\n\n", line)
	}
	s.fl.Flush()
	return len(p), nil
}

func (s *Server) simLogs(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	fl, ok := sse(w)
	if !ok {
		writeErr(w, 500, errors.New("streaming unsupported"))
		return
	}
	_ = s.Run.Stream(r.Context(), sseWriter{w, fl}, "journalctl", "-u", units.UnitName(sim.Name), "-n", "300", "-f", "-o", "short-iso", "--no-pager")
}

func (s *Server) listBuilds(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	list, err := s.Builds.List(sim.Name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if list == nil {
		list = []builds.Build{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg, _ := s.config()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		if part.FormName() != "zip" {
			continue
		}
		b, err := s.Builds.Upload(r.Context(), cfg, sim, part.FileName(), part, r.URL.Query().Get("force") == "1")
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 202, b)
		return
	}
	writeErr(w, 400, errors.New("multipart field \"zip\" missing"))
}

func (s *Server) fetch(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	var in struct {
		Ref   string `json:"ref"`
		Force bool   `json:"force"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	cfg, _ := s.config()
	b, err := s.Builds.Fetch(context.Background(), cfg, sim, in.Ref, in.Force)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 202, b)
}

// buildLog follows build.log until the build has finished.
func (s *Server) buildLog(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	id := r.PathValue("id")
	if _, err := s.Builds.Get(sim.Name, id); err != nil {
		writeErr(w, 404, err)
		return
	}
	fl, ok := sse(w)
	if !ok {
		return
	}
	out := sseWriter{w, fl}
	path := s.Builds.LogPath(sim.Name, id)
	var f *os.File
	for f == nil {
		var err error
		if f, err = os.Open(path); err != nil {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
	defer f.Close()
	rd := bufio.NewReader(f)
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			_, _ = out.Write([]byte(line))
		}
		if err == nil {
			continue
		}
		b, _ := s.Builds.Get(sim.Name, id)
		if !b.Running {
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", map[bool]string{true: "ok", false: "failed"}[b.OK])
			fl.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Server) activateBuild(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	if err := s.Builds.Activate(sim.Name, r.PathValue("id")); err != nil {
		writeErr(w, 400, err)
		return
	}
	if s.Units.Status(r.Context(), sim.Name).Active == "active" {
		_ = s.Units.Restart(r.Context(), sim.Name)
	}
	writeJSON(w, 200, s.Builds.Current(sim.Name))
}

func (s *Server) deleteBuild(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.sim(r)
	if !ok {
		writeErr(w, 404, errors.New("no such simulator"))
		return
	}
	if err := s.Builds.Delete(sim.Name, r.PathValue("id")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "deleted"})
}

// jarUpload replaces the pumpX2 cliparser jar used by faketandem.
func (s *Server) jarUpload(w http.ResponseWriter, r *http.Request) {
	if err := s.saveUpload(r, "jar", filepath.Join(s.JarsDir, "pumpx2-cliparser.jar"), 0o644); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "jar replaced; restart faketandem to use it"})
}

// adminUpload replaces this very binary and restarts the service a moment later.
func (s *Server) adminUpload(w http.ResponseWriter, r *http.Request) {
	if s.AdminBin == "" {
		writeErr(w, 400, errors.New("admin binary path unknown"))
		return
	}
	if err := s.saveUpload(r, "binary", s.AdminBin, 0o755); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "admin replaced; restarting"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if s.Run.Mock() {
			log.Printf("mock: would restart osaid-admin")
			return
		}
		_, _ = s.Run.Run(context.Background(), "systemd-run", "--on-active=1", "--collect", "systemctl", "restart", "osaid-admin.service")
	}()
}

func (s *Server) saveUpload(r *http.Request, field, dst string, mode os.FileMode) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return err
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return fmt.Errorf("multipart field %q missing", field)
		}
		if err != nil {
			return err
		}
		if part.FormName() != field {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".new"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, part)
		f.Close()
		if err != nil || n == 0 {
			os.Remove(tmp)
			if err == nil {
				err = errors.New("empty upload")
			}
			return err
		}
		return os.Rename(tmp, dst)
	}
}
