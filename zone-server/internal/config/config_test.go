package config

import (
	"os"
	"testing"

	"zone-online/zone-server/internal/protocol"
)

func TestLoadConfig(t *testing.T) {
	content := `
port: 27015
tick_rate_hz: 30
max_players: 64
db_path: "test_zone_world.db"
log_level: "info"
emission_interval_min: 45
admin_pipe: "\\\\.\\pipe\\zone_server_admin"
`
	tmpfile, err := os.CreateTemp("", "config_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	tmpfile.Close()

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Port != 27015 {
		t.Errorf("Expected port 27015, got %d", cfg.Port)
	}
	if cfg.TickRateHz != 30 {
		t.Errorf("Expected TickRateHz 30, got %d", cfg.TickRateHz)
	}
	if cfg.MaxPlayers != 64 {
		t.Errorf("Expected MaxPlayers 64, got %d", cfg.MaxPlayers)
	}
	if cfg.DBPath != "test_zone_world.db" {
		t.Errorf("Expected DBPath test_zone_world.db, got %s", cfg.DBPath)
	}
	if cfg.ServerName != "Zone Online" {
		t.Errorf("Expected default ServerName 'Zone Online', got %q", cfg.ServerName)
	}
	if cfg.MapName != "l01_escape" {
		t.Errorf("Expected default MapName 'l01_escape', got %q", cfg.MapName)
	}
	if cfg.GroupMaxPlayers != 4 {
		t.Errorf("Expected default GroupMaxPlayers 4, got %d", cfg.GroupMaxPlayers)
	}
	if cfg.InviteTTLSec != 60 {
		t.Errorf("Expected default InviteTTLSec 60, got %d", cfg.InviteTTLSec)
	}
}

func TestConfigV2Keys(t *testing.T) {
	content := `
port: 27015
tick_rate_hz: 30
max_players: 64
server_name: "Custom Zone"
map_name: "l05_bar_rostok"
mode: 3
locked: true
group_max_players: 6
invite_ttl_sec: 30
`
	tmpfile, err := os.CreateTemp("", "config_v2_test_*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	tmpfile.Close()

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ServerName != "Custom Zone" || cfg.MapName != "l05_bar_rostok" {
		t.Errorf("unexpected server/map: %q/%q", cfg.ServerName, cfg.MapName)
	}
	if cfg.Mode != 3 {
		t.Errorf("Expected Mode 3, got %d", cfg.Mode)
	}
	if !cfg.Locked {
		t.Error("Expected Locked true")
	}
	if cfg.GroupMaxPlayers != 6 {
		t.Errorf("Expected GroupMaxPlayers 6, got %d", cfg.GroupMaxPlayers)
	}
	if cfg.InviteTTLSec != 30 {
		t.Errorf("Expected InviteTTLSec 30, got %d", cfg.InviteTTLSec)
	}
}

// group_max_players is clamped to [1, protocol.MaxGroupMembers], default 4.
func TestGroupMaxPlayersClampedToProtocolCap(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{-5, 4},
		{0, 4},
		{1, 1},
		{4, 4},
		{6, 6},
		{protocol.MaxGroupMembers, protocol.MaxGroupMembers},
		{protocol.MaxGroupMembers + 1, protocol.MaxGroupMembers},
		{100, protocol.MaxGroupMembers},
	}
	for _, tc := range cases {
		cfg := &Config{GroupMaxPlayers: tc.in}
		cfg.SetDefaults()
		if cfg.GroupMaxPlayers != tc.want {
			t.Errorf("GroupMaxPlayers %d clamped to %d, want %d", tc.in, cfg.GroupMaxPlayers, tc.want)
		}
	}
}

// Validate accepts the supported 48/64 player caps and rejects non-positive
// values, so the handshake full-path can rely on MaxPlayers being sane.
func TestValidate_MaxPlayers48And64(t *testing.T) {
	for _, max := range []int{48, 64} {
		cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: max}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("max_players %d: Validate returned %v, want nil", max, err)
		}
	}

	for _, max := range []int{0, -1} {
		cfg := &Config{Port: 27015, TickRateHz: 30, MaxPlayers: max}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("max_players %d: Validate returned nil, want error", max)
		}
	}
}
