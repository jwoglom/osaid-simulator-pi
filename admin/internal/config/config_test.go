package config

import (
	"strings"
	"testing"
)

func TestDefaultParses(t *testing.T) {
	c, err := Parse([]byte(Default))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Simulators) != 4 {
		t.Fatalf("want 4 simulators, got %d", len(c.Simulators))
	}
	ft, ok := c.Find("faketandem")
	if !ok || ft.MemoryMax != "320M" || ft.User != "osaid" || ft.Build.Output != "faketandem" {
		t.Fatalf("effective faketandem wrong: %+v", ft)
	}
	if got := c.Peers(ft); strings.Join(got, ",") != "g7sim,pod-dash,pod-five" {
		t.Fatalf("peers = %v", got)
	}
}

func TestValidation(t *testing.T) {
	bad := []string{
		"version: 2\n",
		"version: 1\nsimulators:\n  - name: Bad\n    exec: x\n    build: {kind: go}\n",
		"version: 1\nsimulators:\n  - name: a\n    exec: x\n    build: {kind: nope}\n",
		"version: 1\nsimulators:\n  - name: a\n    exec: x\n    build: {kind: go}\n    ui_port: 80\n",
		"version: 1\nsimulators:\n  - name: a\n    exec: x\n    build: {kind: go}\n    ui_port: 8081\n  - name: b\n    exec: x\n    build: {kind: go}\n    ui_port: 8081\n",
		"version: 1\nunknown_key: 1\n",
		"version: 1\nwifi: {enabled: true}\n",
	}
	for _, b := range bad {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("expected error for %q", b)
		}
	}
}

func TestSetWifiPreservesComments(t *testing.T) {
	out, err := SetWifi([]byte(Default), Wifi{Enabled: true, SSID: "Net", PSK: "pw", Country: "DE"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# OSAID simulator Pi configuration.", "ssid: \"Net\"", "psk: \"pw\"", "enabled: true", "country: DE", "  - name: g7sim"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "    enabled") {
		t.Errorf("re-indented to 4 spaces:\n%s", s)
	}
}
