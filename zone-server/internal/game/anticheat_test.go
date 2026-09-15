package game

import (
	"testing"

	"zone-online/zone-server/internal/network"
)

func TestAntiCheat_ValidateMove_Speedhack(t *testing.T) {
	ac := NewAntiCheatManager()

	sess := &network.PlayerSession{
		SessionID: 100,
		Position:  [3]float32{0, 0, 0},
	}

	// 1. Normal horizontal movement: 10m in 1.0s = 10 m/s <= 25 m/s -> valid
	valid, reason := ac.ValidateMove(sess, [3]float32{10, 0, 0}, 1.0)
	if !valid {
		t.Fatalf("expected valid movement, got invalid: %s", reason)
	}
	if reason != "" {
		t.Fatalf("expected empty reason, got: %s", reason)
	}

	// 2. Normal 3D movement: dx=10, dy=5, dz=10, dt=1.0s -> 15 m/s <= 25 m/s -> valid
	valid, reason = ac.ValidateMove(sess, [3]float32{10, 5, 10}, 1.0)
	if !valid {
		t.Fatalf("expected valid 3D movement, got invalid: %s", reason)
	}

	// 3. Excessive horizontal speed: 30m in 1.0s = 30 m/s > 25 m/s -> invalid
	valid, reason = ac.ValidateMove(sess, [3]float32{30, 0, 0}, 1.0)
	if valid {
		t.Fatalf("expected speed violation to be rejected")
	}
	if reason != "speed violation" {
		t.Fatalf("expected 'speed violation', got: %q", reason)
	}

	// 4. Excessive 3D diagonal speed: dx=18, dy=10, dz=18 in 1.0s -> ~27.35 m/s > 25 m/s -> invalid
	valid, reason = ac.ValidateMove(sess, [3]float32{18, 10, 18}, 1.0)
	if valid {
		t.Fatalf("expected 3D speed violation to be rejected")
	}
	if reason != "speed violation" {
		t.Fatalf("expected 'speed violation', got: %q", reason)
	}
}

func TestAntiCheat_ValidateMove_VerticalTeleport(t *testing.T) {
	ac := NewAntiCheatManager()

	sess := &network.PlayerSession{
		SessionID: 101,
		Position:  [3]float32{0, 50, 0},
	}

	// 1. Normal vertical move: 10m delta <= 15m -> valid
	valid, reason := ac.ValidateMove(sess, [3]float32{0, 60, 0}, 1.0)
	if !valid {
		t.Fatalf("expected vertical move of 10m to be valid, got: %s", reason)
	}

	// 2. Upward vertical teleport: 20m delta > 15m -> invalid
	valid, reason = ac.ValidateMove(sess, [3]float32{0, 71, 0}, 1.0)
	if valid {
		t.Fatalf("expected upward vertical teleport to be rejected")
	}
	if reason != "vertical teleport" {
		t.Fatalf("expected 'vertical teleport', got: %q", reason)
	}

	// 3. Downward vertical teleport: 25m drop > 15m -> invalid
	valid, reason = ac.ValidateMove(sess, [3]float32{0, 20, 0}, 1.0)
	if valid {
		t.Fatalf("expected downward vertical teleport to be rejected")
	}
	if reason != "vertical teleport" {
		t.Fatalf("expected 'vertical teleport', got: %q", reason)
	}
}

func TestAntiCheat_ValidateMove_DtBounds(t *testing.T) {
	ac := NewAntiCheatManager()

	sess := &network.PlayerSession{
		SessionID: 102,
		Position:  [3]float32{0, 0, 0},
	}

	// dt <= 0 should be skipped (e.g. first packet) even with huge position delta
	valid, reason := ac.ValidateMove(sess, [3]float32{1000, 1000, 1000}, 0.0)
	if !valid || reason != "" {
		t.Fatalf("expected dt=0 to skip check, got valid=%v, reason=%q", valid, reason)
	}

	valid, reason = ac.ValidateMove(sess, [3]float32{1000, 1000, 1000}, -1.0)
	if !valid || reason != "" {
		t.Fatalf("expected dt<0 to skip check, got valid=%v, reason=%q", valid, reason)
	}

	// dt > 2.0 is clamped to dt = 2.0 for the speed computation (no bypass):
	// a huge jump after a pause must still fail.
	valid, reason = ac.ValidateMove(sess, [3]float32{500, 50, 500}, 2.5)
	if valid {
		t.Fatalf("expected dt>2.0 huge jump to be rejected via clamp, got valid")
	}
	// Small drift after a pause stays valid: 10m clamped to 2.0s = 5 m/s.
	valid, reason = ac.ValidateMove(sess, [3]float32{10, 0, 0}, 2.5)
	if !valid {
		t.Fatalf("expected dt>2.0 small move to stay valid, got invalid: %q", reason)
	}
}

func TestAntiCheat_ValidateMove_NilSession(t *testing.T) {
	ac := NewAntiCheatManager()
	valid, reason := ac.ValidateMove(nil, [3]float32{10, 0, 0}, 1.0)
	if valid || reason != "invalid session" {
		t.Fatalf("expected invalid session for nil sess, got valid=%v, reason=%q", valid, reason)
	}
}

func TestAntiCheat_ViolationThresholds(t *testing.T) {
	ac := NewAntiCheatManager()

	if MaxViolationsBeforeAction != 5 {
		t.Fatalf("expected MaxViolationsBeforeAction to be 5, got %d", MaxViolationsBeforeAction)
	}

	// ShouldRubberband
	if ac.ShouldRubberband(0) {
		t.Errorf("ShouldRubberband(0) should be false")
	}
	if !ac.ShouldRubberband(1) {
		t.Errorf("ShouldRubberband(1) should be true")
	}
	if !ac.ShouldRubberband(4) {
		t.Errorf("ShouldRubberband(4) should be true")
	}
	if !ac.ShouldRubberband(5) {
		t.Errorf("ShouldRubberband(5) should be true")
	}

	// ShouldKick
	if ac.ShouldKick(0) {
		t.Errorf("ShouldKick(0) should be false")
	}
	if ac.ShouldKick(4) {
		t.Errorf("ShouldKick(4) should be false")
	}
	if !ac.ShouldKick(5) {
		t.Errorf("ShouldKick(5) should be true at MaxViolationsBeforeAction")
	}
	if !ac.ShouldKick(6) {
		t.Errorf("ShouldKick(6) should be true")
	}
}

func TestAntiCheat_RecordAndReset(t *testing.T) {
	ac := NewAntiCheatManager()
	sessionID := uint32(505)

	if count := ac.ViolationCount(sessionID); count != 0 {
		t.Fatalf("expected 0 initial violations, got %d", count)
	}

	c1 := ac.RecordViolation(sessionID, "speed violation")
	if c1 != 1 || ac.ViolationCount(sessionID) != 1 {
		t.Fatalf("expected 1 violation, got c1=%d, count=%d", c1, ac.ViolationCount(sessionID))
	}

	c2 := ac.RecordViolation(sessionID, "vertical teleport")
	if c2 != 2 || ac.ViolationCount(sessionID) != 2 {
		t.Fatalf("expected 2 violations, got c2=%d, count=%d", c2, ac.ViolationCount(sessionID))
	}

	ac.Reset(sessionID)
	if count := ac.ViolationCount(sessionID); count != 0 {
		t.Fatalf("expected 0 violations after reset, got %d", count)
	}
}
