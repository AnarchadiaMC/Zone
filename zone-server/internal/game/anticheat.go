package game

import (
	"fmt"
	"math"
	"sync"

	"zone-online/zone-server/internal/network"

	"go.uber.org/zap"
)

const (
	acMaxHorizontalSpeed = 25.0 // m/s — generous cap above X-Ray sprint (~8 m/s)
	acMaxVerticalDelta   = 15.0 // metres — permits falls but blocks fly hacks
)

// AntiCheatManager tracks per-session violation counts and validates movement.
type AntiCheatManager struct {
	mu         sync.Mutex
	violations map[uint32]int
	logger     *zap.Logger
}

// NewAntiCheatManager creates an initialised AntiCheatManager.
func NewAntiCheatManager(logger ...*zap.Logger) *AntiCheatManager {
	ac := &AntiCheatManager{
		violations: make(map[uint32]int),
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
func (ac *AntiCheatManager) ValidateMove(sess *network.PlayerSession, newPos [3]float32, dt float32) (bool, string) {
	if dt <= 0 {
		return true, ""
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
		return false, fmt.Sprintf("speed violation: %.2f m/s", totalSpeed)
	}

	return true, ""
}

// RecordViolation increments the violation count for sessionID and returns the new total.
func (ac *AntiCheatManager) RecordViolation(sessionID uint32, reason string) int {
	ac.mu.Lock()
	ac.violations[sessionID]++
	count := ac.violations[sessionID]
	ac.mu.Unlock()

	if ac.logger != nil {
		ac.logger.Warn("anticheat violation recorded",
			zap.Uint32("session", sessionID),
			zap.String("reason", reason),
			zap.Int("count", count),
		)
	}
	return count
}

// ViolationCount returns the accumulated violation count for sessionID.
func (ac *AntiCheatManager) ViolationCount(sessionID uint32) int {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.violations[sessionID]
}

// Reset clears the violation record for a session (call on disconnect).
func (ac *AntiCheatManager) Reset(sessionID uint32) {
	ac.mu.Lock()
	delete(ac.violations, sessionID)
	ac.mu.Unlock()
}
