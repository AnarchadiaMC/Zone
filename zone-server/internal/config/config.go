package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"zone-online/zone-server/internal/protocol"
)

type Config struct {
	Port                int    `yaml:"port"`
	TickRateHz          int    `yaml:"tick_rate_hz"`
	MaxPlayers          int    `yaml:"max_players"`
	DBPath              string `yaml:"db_path"`
	LogLevel            string `yaml:"log_level"`
	EmissionIntervalMin int    `yaml:"emission_interval_min"`
	AdminPipe           string `yaml:"admin_pipe"`
	ServerName          string `yaml:"server_name"`
	MapName             string `yaml:"map_name"`
	Mode                int    `yaml:"mode"`
	Locked              bool   `yaml:"locked"`
	GroupMaxPlayers     int    `yaml:"group_max_players"`
	InviteTTLSec        int    `yaml:"invite_ttl_sec"`
}

// SetDefaults sets sensible defaults for optional or missing fields.
func (c *Config) SetDefaults() {
	if c.DBPath == "" {
		c.DBPath = "zone_world.db"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.EmissionIntervalMin <= 0 {
		c.EmissionIntervalMin = 120
	}
	if c.ServerName == "" {
		c.ServerName = "Zone Online"
	}
	if c.MapName == "" {
		c.MapName = "l01_escape"
	}
	// group_max_players is clamped to the protocol wire capacity: values below
	// 1 fall back to the default 4 and values above protocol.MaxGroupMembers
	// are capped, so the configured group can never exceed what OpGroupState
	// can represent.
	if c.GroupMaxPlayers <= 0 {
		c.GroupMaxPlayers = 4
	}
	if c.GroupMaxPlayers > protocol.MaxGroupMembers {
		c.GroupMaxPlayers = protocol.MaxGroupMembers
	}
	if c.InviteTTLSec <= 0 {
		c.InviteTTLSec = 60
	}
}

// Validate validates that configuration values are within acceptable bounds
// and sets sensible defaults for missing fields.
func (c *Config) Validate() error {
	c.SetDefaults()

	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
	}
	if c.TickRateHz <= 0 {
		return fmt.Errorf("tick_rate_hz must be > 0, got %d", c.TickRateHz)
	}
	if c.MaxPlayers <= 0 {
		return fmt.Errorf("max_players must be > 0, got %d", c.MaxPlayers)
	}

	return nil
}

func Load(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(file, cfg); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}
