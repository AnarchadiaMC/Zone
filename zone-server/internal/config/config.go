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
	// SessionTimeoutSec is the inactivity timeout for live sessions. Omitted
	// or non-positive values fall back to DefaultSessionTimeoutSec; values
	// below MinSessionTimeoutSec are clamped up.
	SessionTimeoutSec int `yaml:"session_timeout_sec"`

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

	// AI combat FSM knobs. AICombatEnabled is a pointer so an omitted key keeps
	// the documented default (true) while an explicit `ai_combat_enabled:
	// false` leaves patrol replication running but disables aggro/attacks.
	// The numeric knobs use <= 0 as "unset" and resolve to the documented
	// defaults in the game package.
	AICombatEnabled    *bool   `yaml:"ai_combat_enabled"`
	AIAggroRadiusM     float64 `yaml:"ai_aggro_radius_m"`
	AIAttackRangeM     float64 `yaml:"ai_attack_range_m"`
	AIAttackCooldownMS int     `yaml:"ai_attack_cooldown_ms"`
	AIMeleeDamage      float64 `yaml:"ai_melee_damage"`
	AICorpseSeconds    int     `yaml:"ai_corpse_seconds"`
	AIPatrolResumeS    int     `yaml:"ai_patrol_resume_s"`

	// Line-of-sight gating. LosEnabled is a pointer so an omitted key keeps the
	// default (true); explicit `los_enabled: false` disables server-side
	// occluder checks entirely. LosDataDir holds the per-level <name>.occl
	// files produced by tools/levelgeom.
	LosEnabled *bool  `yaml:"los_enabled"`
	LosDataDir string `yaml:"los_data_dir"`

	// WorldItemTTLMin is the world-item lifetime in minutes. Pointer semantics:
	// an omitted key keeps the 60-minute default, an explicit 0 disables the
	// TTL sweep (items persist until picked up).
	WorldItemTTLMin *int `yaml:"world_item_ttl_min"`
	// WorldItemMaxPerLevel caps persisted world_items rows per level; new drops
	// beyond the cap are rejected. Values <= 0 fall back to the default 500.
	WorldItemMaxPerLevel int `yaml:"world_item_max_per_level"`

	// TradeMaxMoneyDelta caps the absolute ruble amount one OpTradeAction may
	// move. Values <= 0 fall back to DefaultTradeMaxMoneyDelta (200000).
	TradeMaxMoneyDelta int `yaml:"trade_max_money_delta"`
}

// DefaultWorldItemTTLMin is the world-item TTL applied when the key is absent.
const DefaultWorldItemTTLMin = 60

// Session timeout defaults and floor: an omitted key means 30 s of silence
// evicts a session, and a configured value below the floor is raised to it.
const (
	DefaultSessionTimeoutSec = 30
	MinSessionTimeoutSec     = 5
)

// DefaultWorldItemMaxPerLevel is the per-level world item cap applied when the
// key is absent or non-positive.
const DefaultWorldItemMaxPerLevel = 500

// DefaultTradeMaxMoneyDelta is the per-action ruble cap applied when
// trade_max_money_delta is absent or non-positive.
const DefaultTradeMaxMoneyDelta = 200000

// AI combat FSM defaults, applied when the corresponding key is absent.
const (
	DefaultAIAggroRadiusM     = 40.0
	DefaultAIAttackRangeM     = 2.0
	DefaultAIAttackCooldownMS = 1500
	DefaultAIMeleeDamage      = 10.0
	DefaultAICorpseSeconds    = 5
	DefaultAIPatrolResumeS    = 10
)

// DefaultLOSDataDir is the directory scanned for <level>.occl files when the
// key is absent or empty.
const DefaultLOSDataDir = "zone_los"

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

// AICombatEnabledOrDefault reports whether the puppet combat FSM is enabled,
// defaulting to true when the key is absent (or the config is nil, as in
// tests).
func (c *Config) AICombatEnabledOrDefault() bool {
	if c == nil || c.AICombatEnabled == nil {
		return true
	}
	return *c.AICombatEnabled
}

// LosEnabledOrDefault reports whether server-side line-of-sight gating is
// enabled, defaulting to true when the key is absent (or the config is nil).
func (c *Config) LosEnabledOrDefault() bool {
	if c == nil || c.LosEnabled == nil {
		return true
	}
	return *c.LosEnabled
}

// LOSDataDirOrDefault returns the occluder directory, defaulting to "zone_los"
// when the key is absent or empty.
func (c *Config) LOSDataDirOrDefault() string {
	if c == nil || c.LosDataDir == "" {
		return "zone_los"
	}
	return c.LosDataDir
}

// TradeMaxMoneyDeltaOrDefault returns the per-action trade money cap,
// defaulting to DefaultTradeMaxMoneyDelta when the key is absent or
// non-positive (or the whole config is nil, as in tests).
func (c *Config) TradeMaxMoneyDeltaOrDefault() int {
	if c == nil || c.TradeMaxMoneyDelta <= 0 {
		return DefaultTradeMaxMoneyDelta
	}
	return c.TradeMaxMoneyDelta
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
	if c.SessionTimeoutSec <= 0 {
		c.SessionTimeoutSec = DefaultSessionTimeoutSec
	}
	if c.SessionTimeoutSec < MinSessionTimeoutSec {
		c.SessionTimeoutSec = MinSessionTimeoutSec
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
	if c.AIAggroRadiusM <= 0 {
		c.AIAggroRadiusM = DefaultAIAggroRadiusM
	}
	if c.AIAttackRangeM <= 0 {
		c.AIAttackRangeM = DefaultAIAttackRangeM
	}
	if c.AIAttackCooldownMS <= 0 {
		c.AIAttackCooldownMS = DefaultAIAttackCooldownMS
	}
	if c.AIMeleeDamage <= 0 {
		c.AIMeleeDamage = DefaultAIMeleeDamage
	}
	if c.AICorpseSeconds <= 0 {
		c.AICorpseSeconds = DefaultAICorpseSeconds
	}
	if c.AIPatrolResumeS <= 0 {
		c.AIPatrolResumeS = DefaultAIPatrolResumeS
	}
	if c.LosDataDir == "" {
		c.LosDataDir = DefaultLOSDataDir
	}
	if c.WorldItemMaxPerLevel <= 0 {
		c.WorldItemMaxPerLevel = DefaultWorldItemMaxPerLevel
	}
	if c.TradeMaxMoneyDelta <= 0 {
		c.TradeMaxMoneyDelta = DefaultTradeMaxMoneyDelta
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
