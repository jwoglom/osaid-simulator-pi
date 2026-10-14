package system

import (
	"net"
	"os"
	"time"
)

// Notify sends a message to systemd's notify socket, if any. Used for
// Type=notify readiness and WatchdogSec= heartbeats.
func Notify(state string) {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(state))
}

// Watchdog sends READY=1 then WATCHDOG=1 at half the configured interval.
func Watchdog(stop <-chan struct{}) {
	Notify("READY=1")
	usec := os.Getenv("WATCHDOG_USEC")
	if usec == "" {
		return
	}
	var n int64
	for _, c := range usec {
		if c < '0' || c > '9' {
			return
		}
		n = n*10 + int64(c-'0')
	}
	if n <= 0 {
		return
	}
	t := time.NewTicker(time.Duration(n/2) * time.Microsecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			Notify("WATCHDOG=1")
		}
	}
}
