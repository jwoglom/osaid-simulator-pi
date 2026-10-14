// Package sysinfo reports the hardware and host state shown on the admin page.
package sysinfo

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jwoglom/osaid-simulator-pi/admin/internal/system"
)

type Info struct {
	Model       string    `json:"model"`
	Board       string    `json:"board"` // zero-w | zero-2w | other
	Hostname    string    `json:"hostname"`
	Uptime      int64     `json:"uptime_s"`
	Load1       float64   `json:"load1"`
	MemTotalKB  int64     `json:"mem_total_kb"`
	MemAvailKB  int64     `json:"mem_avail_kb"`
	SwapTotalKB int64     `json:"swap_total_kb"`
	SwapFreeKB  int64     `json:"swap_free_kb"`
	HCI0        bool      `json:"hci0"`
	USB0        string    `json:"usb0"` // up | down | missing
	Now         time.Time `json:"now"`
	Timezone    string    `json:"timezone"`
	NTPSynced   bool      `json:"ntp_synced"`
	Mock        bool      `json:"mock"`
	Arch        string    `json:"arch"`
}

func Model() string {
	b, err := os.ReadFile("/proc/device-tree/model")
	if err != nil {
		return "unknown (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	}
	return strings.TrimRight(string(b), "\x00\n")
}

// Board classifies the model string.
func Board(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "zero 2"):
		return "zero-2w"
	case strings.Contains(m, "zero"):
		return "zero-w"
	default:
		return "other"
	}
}

// BuildParallelism is the go build -p value appropriate for the board: memory,
// not cores, is the limit on both Zeros.
func BuildParallelism(board string) int {
	if board == "zero-2w" {
		return 2
	}
	return 1
}

func Collect(ctx context.Context, run system.Runner) Info {
	in := Info{Model: Model(), Hostname: system.Hostname(), Now: time.Now(), Mock: run.Mock(), Arch: runtime.GOARCH}
	in.Board = Board(in.Model)
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		f, _ := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
		in.Uptime = int64(f)
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		in.Load1, _ = strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			n, _ := strconv.ParseInt(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				in.MemTotalKB = n
			case "MemAvailable:":
				in.MemAvailKB = n
			case "SwapTotal:":
				in.SwapTotalKB = n
			case "SwapFree:":
				in.SwapFreeKB = n
			}
		}
	}
	if _, err := os.Stat("/sys/class/bluetooth/hci0"); err == nil {
		in.HCI0 = true
	}
	in.USB0 = "missing"
	if b, err := os.ReadFile("/sys/class/net/usb0/operstate"); err == nil {
		in.USB0 = strings.TrimSpace(string(b))
	}
	tz, _ := time.Now().Zone()
	in.Timezone = tz
	if out, err := run.Run(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "-p", "Timezone"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "NTPSynchronized":
				in.NTPSynced = v == "yes"
			case "Timezone":
				if v != "" {
					in.Timezone = v
				}
			}
		}
	}
	return in
}

// SetTime sets the system clock from the browser unless NTP already has it.
func SetTime(ctx context.Context, run system.Runner, unix int64, tz string) error {
	if _, err := run.Run(ctx, "date", "-u", "-s", "@"+strconv.FormatInt(unix, 10)); err != nil {
		return err
	}
	if tz != "" && !strings.ContainsAny(tz, " ;&|$`") {
		_, _ = run.Run(ctx, "timedatectl", "set-timezone", tz)
	}
	_, _ = run.Run(ctx, "fake-hwclock", "save")
	return nil
}
