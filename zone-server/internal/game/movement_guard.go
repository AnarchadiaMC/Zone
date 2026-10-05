package game

import (
	"math"
	"time"

	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/network"
)

// MovementGuard enforces the server-authoritative movement budget and detects
// the lag-switch signature (a long transform gap while the session is still
// alive through heartbeats/ACKs). It is intentionally stateless per session:
// history lives in the session's TransformHistory ring.
type MovementGuard struct {
	maxSpeed     float64
	tolerance    float64
	lagGap       time.Duration
	strikesLimit int
	burstWindow  time.Duration
}

// NewMovementGuard builds a guard from config, falling back to the frozen
// defaults when the config is nil or a value is unset.
func NewMovementGuard(cfg *config.Config) *MovementGuard {
	g := &MovementGuard{
		maxSpeed:     25.0,
		tolerance:    2.0,
		lagGap:       1500 * time.Millisecond,
		strikesLimit: 3,
		burstWindow:  time.Second,
	}
	if cfg != nil {
		if cfg.MaxSpeedMPS > 0 {
			g.maxSpeed = cfg.MaxSpeedMPS
		}
		if cfg.CorrectionToleranceM > 0 {
			g.tolerance = cfg.CorrectionToleranceM
		}
		if cfg.LagswitchGapMS > 0 {
			g.lagGap = time.Duration(cfg.LagswitchGapMS) * time.Millisecond
		}
		if cfg.LagswitchStrikes > 0 {
			g.strikesLimit = cfg.LagswitchStrikes
		}
	}
	return g
}

// MaxSpeedMPS is the configured movement speed budget in metres/second.
func (g *MovementGuard) MaxSpeedMPS() float64 { return g.maxSpeed }

// ToleranceM is the configured per-correction tolerance in metres.
func (g *MovementGuard) ToleranceM() float64 { return g.tolerance }

// LagswitchGap is the transform-gap threshold that starts looking like a
// deliberate stall.
func (g *MovementGuard) LagswitchGap() time.Duration { return g.lagGap }

// StrikesLimit is the number of rolling-window strikes that triggers a kick.
func (g *MovementGuard) StrikesLimit() int { return g.strikesLimit }

// BurstWindow is the rolling interval used for the damage-side movement burst
// check.
func (g *MovementGuard) BurstWindow() time.Duration { return g.burstWindow }

// ValidDisplacement reports whether a straight-line move of dist metres is
// within the speed budget for dt seconds plus tolerance. dt <= 0 skips the
// check (first packet after session start / same-tick update).
func (g *MovementGuard) ValidDisplacement(dist float64, dt float64) bool {
	if g == nil || dt <= 0 {
		return true
	}
	allowed := g.maxSpeed*dt + g.tolerance
	return dist <= allowed
}

// Displacement returns the 3D distance between two positions.
func Displacement(a, b [3]float32) float64 {
	dx := float64(a[0] - b[0])
	dy := float64(a[1] - b[1])
	dz := float64(a[2] - b[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// IsLagswitchSignature reports whether a transform arrives after a gap longer
// than the configured threshold while heartbeats/ACKs kept the session alive.
// hasPriorTransform separates a genuine stall from the first transform of a
// fresh session (whose LastTransformTime is zero).
func (g *MovementGuard) IsLagswitchSignature(gap time.Duration, hasPriorTransform bool, sinceAlive time.Duration) bool {
	if g == nil || !hasPriorTransform {
		return false
	}
	if gap <= g.lagGap {
		return false
	}
	// Alive if any non-transform traffic arrived since the previous transform.
	return sinceAlive < gap
}

// BurstAfterStall reports whether the session's accepted positions over the
// last burst window imply a displacement above the speed budget: the
// "stall then teleport" kill pattern. It reads the session under its lock.
func (g *MovementGuard) BurstAfterStall(sess *network.PlayerSession, now time.Time) bool {
	if g == nil || sess == nil {
		return false
	}
	sess.Lock()
	from, ok := sess.TransformHistory.OldestWithin(g.burstWindow, now)
	cur := sess.Position
	sess.Unlock()
	if !ok {
		return false
	}
	dist := Displacement([3]float32{from.X, from.Y, from.Z}, cur)
	allowed := g.maxSpeed*g.burstWindow.Seconds() + g.tolerance
	return dist > allowed
}
