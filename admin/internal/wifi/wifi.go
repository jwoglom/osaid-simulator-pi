// Package wifi applies the optional uplink from the config through NetworkManager.
package wifi

import (
	"context"
	"strings"

	"github.com/jwoglom/osaid-simulator-pi/admin/internal/config"
	"github.com/jwoglom/osaid-simulator-pi/admin/internal/system"
)

const conn = "osaid-wifi"

type Status struct {
	Enabled   bool   `json:"enabled"`
	SSID      string `json:"ssid"`
	State     string `json:"state"`
	IP        string `json:"ip"`
	Connected bool   `json:"connected"`
}

// Apply reconciles NetworkManager with cfg.Wifi. Errors from nmcli are returned
// but a disabled uplink never fails the caller.
func Apply(ctx context.Context, run system.Runner, w config.Wifi) error {
	if !w.Enabled {
		_, _ = run.Run(ctx, "nmcli", "connection", "down", conn)
		_, _ = run.Run(ctx, "nmcli", "connection", "delete", conn)
		_, _ = run.Run(ctx, "rfkill", "block", "wifi")
		return nil
	}
	if w.Country != "" {
		_, _ = run.Run(ctx, "raspi-config", "nonint", "do_wifi_country", w.Country)
	}
	if _, err := run.Run(ctx, "rfkill", "unblock", "wifi"); err != nil {
		return err
	}
	_, _ = run.Run(ctx, "nmcli", "connection", "delete", conn)
	args := []string{"connection", "add", "type", "wifi", "ifname", "wlan0", "con-name", conn, "ssid", w.SSID,
		"connection.autoconnect", "yes", "ipv4.never-default", "no"}
	if w.PSK != "" {
		args = append(args, "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", w.PSK)
	}
	if _, err := run.Run(ctx, "nmcli", args...); err != nil {
		return err
	}
	_, err := run.Run(ctx, "nmcli", "connection", "up", conn)
	return err
}

func Get(ctx context.Context, run system.Runner, w config.Wifi) Status {
	st := Status{Enabled: w.Enabled, SSID: w.SSID, State: "off"}
	if !w.Enabled {
		return st
	}
	out, err := run.Run(ctx, "nmcli", "-t", "-f", "GENERAL.STATE,GENERAL.CONNECTION,IP4.ADDRESS", "device", "show", "wlan0")
	if err != nil {
		st.State = "error: " + err.Error()
		return st
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch {
		case k == "GENERAL.STATE":
			st.State = v
			st.Connected = strings.Contains(v, "connected") && !strings.Contains(v, "disconnected")
		case strings.HasPrefix(k, "IP4.ADDRESS"):
			if st.IP == "" {
				st.IP = v
			}
		}
	}
	return st
}
