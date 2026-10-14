// osaid-admin: the web console of the OSAID simulator Pi.
//
// It renders systemd units from /boot/firmware/osaid.yaml, starts/stops/kills
// simulators, builds uploaded repository zips, keeps the clock right and
// optionally brings up a Wi-Fi uplink. Run with -mock on a workstation.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jwoglom/osaid-simulator-pi/admin/internal/api"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/builds"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/config"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/sysinfo"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/system"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/units"
	"github.com/jwoglom/osaid-simulator-pi/admin/web"
)

var version = "dev" // set with -ldflags "-X main.version=…"

func main() {
	var (
		listen   = flag.String("listen", ":80", "address to serve on")
		cfgPath  = flag.String("config", envOr("OSAID_CONFIG", "/boot/firmware/osaid.yaml"), "config YAML (lives on the boot volume)")
		stateDir = flag.String("state", envOr("OSAID_STATE", "/var/lib/osaid"), "state directory")
		unitDir  = flag.String("units", envOr("OSAID_UNITS", "/run/systemd/system"), "where generated units go")
		goBin    = flag.String("go", envOr("OSAID_GO", "/opt/osaid/go/bin/go"), "Go toolchain used for on-device builds")
		jarsDir  = flag.String("jars", envOr("OSAID_JARS", "/opt/osaid/jars"), "directory holding the pumpX2 jar")
		adminBin = flag.String("self", envOr("OSAID_SELF", "/opt/osaid/admin/osaid-admin"), "path of this binary, for self-update")
		mock     = flag.Bool("mock", false, "developer mode: no systemd, builds run directly, state under ./dev-state")
		printDef = flag.Bool("print-default-config", false, "print the default osaid.yaml and exit")
	)
	flag.Parse()
	if *printDef {
		fmt.Print(config.Default)
		return
	}

	var run system.Runner = system.Exec{}
	if *mock {
		run = system.NewMock()
		if !isSet("state") {
			*stateDir, _ = filepath.Abs("dev-state")
		}
		if !isSet("units") {
			*unitDir = filepath.Join(*stateDir, "units")
		}
		if !isSet("config") {
			*cfgPath = filepath.Join(*stateDir, "osaid.yaml")
		}
		if !isSet("listen") {
			*listen = "127.0.0.1:8080"
		}
		if !isSet("jars") {
			*jarsDir = filepath.Join(*stateDir, "jars")
		}
		if !isSet("self") {
			*adminBin = filepath.Join(*stateDir, "osaid-admin")
		}
	}
	paths := units.Paths{UnitDir: *unitDir, StateDir: *stateDir}
	board := sysinfo.Board(sysinfo.Model())
	webFS, _ := fs.Sub(web.Files, ".")
	srv := &api.Server{
		Version:    version,
		ConfigPath: *cfgPath,
		JarsDir:    *jarsDir,
		AdminBin:   *adminBin,
		Run:        run,
		Units:      &units.Manager{Run: run, Paths: paths},
		Builds:     &builds.Manager{Paths: paths, GoBin: *goBin, Mock: *mock, Jobs: sysinfo.BuildParallelism(board)},
		Web:        webFS,
	}
	ctx := context.Background()
	if err := srv.Load(ctx); err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("osaid-admin %s on %s (%s), config %s, state %s", version, sysinfo.Model(), board, *cfgPath, *stateDir)
	srv.Autostart(ctx)

	stop := make(chan struct{})
	go system.Watchdog(stop)
	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on %s", *listen)
	if err := hs.ListenAndServe(); err != nil {
		close(stop)
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func isSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
