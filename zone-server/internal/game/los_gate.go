package game

import (
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/los"
)

// Multi-sample vertical offsets for the line-of-sight test: the attacker eye
// and the victim's head/chest/pelvis. If ALL three segments are blocked the hit
// is rejected; any clear segment allows it.
const (
	losEyeOffset    float32 = 1.6
	losHeadOffset   float32 = 1.6
	losChestOffset  float32 = 1.0
	losPelvisOffset float32 = 0.5
)

// losManager caches one occluder set per level, loading <level>.occl from the
// configured directory on first use. Missing, unreadable or corrupt files fail
// OPEN (damage allowed) and are logged once per level. All methods are safe for
// concurrent use from UDP workers.
type losManager struct {
	enabled bool
	dir     string
	logger  *zap.Logger

	mu      sync.Mutex
	loaded  map[string]*los.Occluders
	missing map[string]bool
}

// newLOSManager builds the per-level occluder cache. Dir defaults to
// config.DefaultLOSDataDir when empty.
func newLOSManager(enabled bool, dir string, logger *zap.Logger) *losManager {
	if dir == "" {
		dir = config.DefaultLOSDataDir
	}
	return &losManager{
		enabled: enabled,
		dir:     dir,
		logger:  logger,
		loaded:  make(map[string]*los.Occluders),
		missing: make(map[string]bool),
	}
}

// safeOccluderLevelName reports whether a client-reported level name may be
// used as a file name. Only [A-Za-z0-9_.-] is accepted, and ".." is rejected so
// a crafted level name can never traverse outside los_data_dir.
func safeOccluderLevelName(level string) bool {
	if level == "" || len(level) > 64 {
		return false
	}
	for i := 0; i < len(level); i++ {
		c := level[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
		default:
			return false
		}
	}
	return !strings.Contains(level, "..")
}

// occluderFor returns the cached occluder for level. ok=false means no usable
// occluder (disabled, unsafe name, missing or corrupt file): callers fail open.
func (m *losManager) occluderFor(level string) (*los.Occluders, bool) {
	if m == nil || !m.enabled {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if occ, ok := m.loaded[level]; ok {
		return occ, occ != nil
	}
	if m.missing[level] {
		return nil, false
	}
	if !safeOccluderLevelName(level) {
		m.missing[level] = true
		m.logf(level, "unsafe level name; line-of-sight checks disabled for this level (fail open)")
		return nil, false
	}
	path := filepath.Join(m.dir, level+".occl")
	occ, err := los.Load(path)
	if err != nil {
		m.missing[level] = true
		m.logf(level, "occluder load failed; line-of-sight checks fail open for this level")
		return nil, false
	}
	m.loaded[level] = occ
	return occ, occ != nil
}

// logf emits one warning per level (the caller only calls it on the first
// failed lookup, which is cached in m.missing).
func (m *losManager) logf(level, msg string) {
	if m.logger == nil {
		return
	}
	m.logger.Warn("LOS: "+msg,
		zap.String("level", level),
		zap.String("dir", m.dir),
	)
}

// damageAllowed is the DamageHandler gate. checked=false means no occluder data
// was available (the hit is allowed, fail open). When geometry exists, the hit
// is rejected only when all three sample segments are blocked.
func (m *losManager) damageAllowed(level string, from, to [3]float32) bool {
	occ, ok := m.occluderFor(level)
	if !ok || occ == nil {
		return true
	}
	eye := [3]float32{from[0], from[1] + losEyeOffset, from[2]}
	samples := [3][3]float32{
		{to[0], to[1] + losHeadOffset, to[2]},
		{to[0], to[1] + losChestOffset, to[2]},
		{to[0], to[1] + losPelvisOffset, to[2]},
	}
	for i := range samples {
		if !occ.SegmentBlocked(eye, samples[i]) {
			return true
		}
	}
	return false
}
