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

	// Hit-registration knobs. DamageBudgetPerS clamps the rolling 1 s damage
	// an attacker may deal (single hits are also clamped); ItemRatePerS limits
	// item ledger actions per second.
	DamageBudgetPerS float64 `yaml:"damage_budget_per_s"`
	ItemRatePerS     float64 `yaml:"item_rate_per_s"`

	// AI replication knobs. AIEnabled is a pointer so an omitted key keeps the
	// documented default (true) while an explicit `ai_enabled: false` disables
	// every AI entity and packet.
	AIEnabled *bool `yaml:"ai_enabled"`
	// AIOnlineRadiusM is the legacy single-radius alias. When ai_enter_radius_m
	// and ai_leave_radius_m are omitted, an explicitly set legacy radius pins
	// both (no hysteresis); otherwise the defaults are enter 180 / leave 220.
	AIOnlineRadiusM float64 `yaml:"ai_online_radius_m"`
	// AIEnterRadiusM / AILeaveRadiusM implement AoI hysteresis: a puppet enters
	// replication inside the enter radius and only leaves past the (larger)
	// leave radius, so boundary oscillation cannot thrash ENTITY_ENTER/LEAVE.
	AIEnterRadiusM float64 `yaml:"ai_enter_radius_m"`
	AILeaveRadiusM float64 `yaml:"ai_leave_radius_m"`
	// AIMaxEntities caps how many puppet squads are registered for replication;
	// registrations beyond the cap are logged and skipped.
	AIMaxEntities int `yaml:"ai_max_entities"`

	// WorldItemTTLMin is the world-item lifetime in minutes. Pointer semantics:
	// an omitted key keeps the 60-minute default, an explicit 0 disables the
	// TTL sweep (items persist until picked up).
	WorldItemTTLMin *int `yaml:"world_item_ttl_min"`
	// WorldItemMaxPerLevel caps persisted world_items rows per level; new drops
	// beyond the cap are rejected. Values <= 0 fall back to the default 500.
	WorldItemMaxPerLevel int `yaml:"world_item_max_per_level"`
}

// DefaultWorldItemTTLMin is the world-item TTL applied when the key is absent.
const DefaultWorldItemTTLMin = 60

// DefaultWorldItemMaxPerLevel is the per-level world item cap applied when the
// key is absent or non-positive.
const DefaultWorldItemMaxPerLevel = 500

// WorldItemTTLMinutes returns the configured world-item TTL in minutes: the
// default when the key is absent, the explicit value otherwise (0 = disabled).
func (c *Config) WorldItemTTLMinutes() int {
	if c == nil || c.WorldItemTTLMin == nil {
		return DefaultWorldItemTTLMin
	}
	return *c.WorldItemTTLMin
}

// AIEnabledOrDefault reports whether AI replication is enabled, defaulting to
// true when the key is absent (or the whole config is nil, as in tests).
func (c *Config) AIEnabledOrDefault() bool {
	if c == nil || c.AIEnabled == nil {
		return true
	}
	return *c.AIEnabled
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
	if c.DamageBudgetPerS <= 0 {
		c.DamageBudgetPerS = 400
	}
	if c.ItemRatePerS <= 0 {
		c.ItemRatePerS = 5
	}
	if c.AIOnlineRadiusM <= 0 {
		c.AIOnlineRadiusM = 220
	}
	// Legacy alias: an explicitly tuned ai_online_radius_m pins both enter and
	// leave radii (no hysteresis) so old configs keep their exact boundary.
	if c.AIEnterRadiusM <= 0 {
		if c.AIOnlineRadiusM != 220 {
			c.AIEnterRadiusM = c.AIOnlineRadiusM
		} else {
			c.AIEnterRadiusM = 180
		}
	}
	if c.AILeaveRadiusM <= 0 {
		if c.AIOnlineRadiusM != 220 {
			c.AILeaveRadiusM = c.AIOnlineRadiusM
		} else {
			c.AILeaveRadiusM = 220
		}
	}
	if c.AILeaveRadiusM < c.AIEnterRadiusM {
		c.AILeaveRadiusM = c.AIEnterRadiusM
	}
	if c.AIMaxEntities <= 0 {
		c.AIMaxEntities = 64
	}
	if c.WorldItemMaxPerLevel <= 0 {
		c.WorldItemMaxPerLevel = DefaultWorldItemMaxPerLevel
	}
}

// Validate validates that configuration values are within acceptable bounds
// and sets sensible defaults for missing fields.
func (c *Config) Validate() error {
	c.SetDefaults()

	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
	}
	// 1..240 Hz: below 1 Hz the tick loop stalls and above 240 Hz the server
	// burns CPU serialising snapshots faster than any client consumes them.
	if c.TickRateHz < 1 || c.TickRateHz > 240 {
		return fmt.Errorf("tick_rate_hz must be between 1 and 240, got %d", c.TickRateHz)
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
