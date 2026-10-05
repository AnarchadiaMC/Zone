package game

import (
	"math"
	"sync"
	"time"

	"zone-online/zone-server/internal/network"

	"go.uber.org/zap"
)

const (
	acMaxHorizontalSpeed      = 25.0 // m/s — generous cap above X-Ray sprint (~8 m/s)
	acMaxVerticalDelta        = 15.0 // metres — permits falls but blocks fly hacks
	MaxViolationsBeforeAction = 5

	// LagswitchStrikeWindow is the rolling window over which repeated
	// stall signatures count toward a kick.
	LagswitchStrikeWindow = 60 * time.Second

	// violationLogInterval rate-limits the per-violation warning so a spamming
	// cheater cannot flood the log; the counter itself is unaffected.
	violationLogInterval = time.Second
)

// AntiCheatManager tracks per-session violation and lag-switch strike counts
// and validates movement.
type AntiCheatManager struct {
	mu         sync.Mutex
	violations map[uint32]int
	lagswitch  map[uint32][]time.Time
	lastLog    map[uint32]time.Time
	logger     *zap.Logger
}

// NewAntiCheatManager creates an initialised AntiCheatManager.
func NewAntiCheatManager(logger ...*zap.Logger) *AntiCheatManager {
	ac := &AntiCheatManager{
		violations: make(map[uint32]int),
		lagswitch:  make(map[uint32][]time.Time),
		lastLog:    make(map[uint32]time.Time),
	}
	if len(logger) > 0 && logger[0] != nil {
		ac.logger = logger[0]
	}
	return ac
}

// ValidateMove checks whether a position delta is physically plausible.
//
// dt is the elapsed time since the session last packet, in seconds.
// Returns (valid, reason). When dt <= 0 the check is skipped (first packet).
// When dt > 2.0 the delta is clamped to dt = 2.0 for the speed computation
// so huge jumps after a pause still fail instead of bypassing validation.
func (ac *AntiCheatManager) ValidateMove(sess *network.PlayerSession, newPos [3]float32, dt float32) (bool, string) {
	if dt <= 0 {
		return true, ""
	}

	if sess == nil {
		return false, "invalid session"
	}

	if dt > 2.0 {
		dt = 2.0
	}

	sess.Lock()
	oldPos := sess.Position
	sess.Unlock()

	// Vertical delta check (Y axis in X-Ray).
	vertDelta := math.Abs(float64(newPos[1] - oldPos[1]))
	if vertDelta > acMaxVerticalDelta {
		return false, "vertical teleport"
	}

	// Total 3D speed magnitude check (S-13).
	vx := float64(newPos[0]-oldPos[0]) / float64(dt)
	vy := float64(newPos[1]-oldPos[1]) / float64(dt)
	vz := float64(newPos[2]-oldPos[2]) / float64(dt)
	totalSpeed := math.Sqrt(float64(vx*vx + vy*vy + vz*vz))
	if totalSpeed > acMaxHorizontalSpeed {
		return false, "speed violation"
	}

	return true, ""
}

// ShouldRubberband returns true if violations > 0.
func (ac *AntiCheatManager) ShouldRubberband(violations int) bool {
	return violations > 0
}

// ShouldKick returns true if violations >= MaxViolationsBeforeAction.
func (ac *AntiCheatManager) ShouldKick(violations int) bool {
	return violations >= MaxViolationsBeforeAction
}

// RecordViolation increments the violation count for sessionID and returns the
// new total. Logging is rate-limited to one warning per second per session so
// a violation spammer cannot flood the log; the count stays exact.
func (ac *AntiCheatManager) RecordViolation(sessionID uint32, reason string) int {
	now := time.Now()
	ac.mu.Lock()
	ac.violations[sessionID]++
	count := ac.violations[sessionID]
	shouldLog := ac.logger != nil
	if shouldLog {
		if last, ok := ac.lastLog[sessionID]; ok && now.Sub(last) < violationLogInterval {
			shouldLog = false
		} else {
			ac.lastLog[sessionID] = now
		}
	}
	ac.mu.Unlock()

	if shouldLog {
		ac.logger.Warn("anticheat violation recorded",
			zap.Uint32("session", sessionID),
			zap.String("reason", reason),
			zap.Int("count", count),
		)
	}
	return count
}

// RecordLagswitchStrike appends a strike timestamp for sessionID and returns
// the number of strikes still inside the rolling 60 s window. Call only when
// the stall signature is detected (long transform gap while heartbeats/ACKs
// remain fresh); plain packet loss below the gap threshold never records one.
func (ac *AntiCheatManager) RecordLagswitchStrike(sessionID uint32, now time.Time) int {
	cutoff := now.Add(-LagswitchStrikeWindow)
	ac.mu.Lock()
	kept := ac.lagswitch[sessionID][:0]
	for _, t := range ac.lagswitch[sessionID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	ac.lagswitch[sessionID] = kept
	count := len(kept)
	ac.mu.Unlock()

	if ac.logger != nil {
		ac.logger.Warn("lag-switch strike recorded",
			zap.Uint32("session", sessionID),
			zap.Int("strikes", count),
		)
	}
	return count
}

// LagswitchStrikeCount returns the live strike count inside the rolling window.
func (ac *AntiCheatManager) LagswitchStrikeCount(sessionID uint32, now time.Time) int {
	cutoff := now.Add(-LagswitchStrikeWindow)
	ac.mu.Lock()
	defer ac.mu.Unlock()
	n := 0
	for _, t := range ac.lagswitch[sessionID] {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

// HasRecentLagswitchStrike reports whether a strike happened within the last
// `within` duration, used to reject damage in the post-stall kill window.
func (ac *AntiCheatManager) HasRecentLagswitchStrike(sessionID uint32, now time.Time, within time.Duration) bool {
	cutoff := now.Add(-within)
	ac.mu.Lock()
	defer ac.mu.Unlock()
	for _, t := range ac.lagswitch[sessionID] {
		if t.After(cutoff) {
			return true
		}
	}
	return false
}

// LagswitchReset clears the strike history for a session.
func (ac *AntiCheatManager) LagswitchReset(sessionID uint32) {
	ac.mu.Lock()
	delete(ac.lagswitch, sessionID)
	ac.mu.Unlock()
}

// ViolationCount returns the accumulated violation count for sessionID.
func (ac *AntiCheatManager) ViolationCount(sessionID uint32) int {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.violations[sessionID]
}

// Reset clears the violation, lag-switch and log-throttle records for a
// session (call on disconnect).
func (ac *AntiCheatManager) Reset(sessionID uint32) {
	ac.mu.Lock()
	delete(ac.violations, sessionID)
	delete(ac.lagswitch, sessionID)
	delete(ac.lastLog, sessionID)
	ac.mu.Unlock()
}

// ViolationReset clears the violation record for a session.
//
// Intended call sites (not wired here to keep this change scoped):
// Server.KickSession and the stale-session timeout cleanup path in
// Server.Tick (server.go) should call ViolationReset when a session is
// removed so a reconnecting client starts with a clean slate.
func (ac *AntiCheatManager) ViolationReset(sessionID uint32) {
	ac.Reset(sessionID)
}
