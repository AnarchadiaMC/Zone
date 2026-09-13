package config

import (
	"os"
	"testing"
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
}
