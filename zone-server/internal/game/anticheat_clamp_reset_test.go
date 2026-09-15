package game

import (
	"testing"

	"zone-online/zone-server/internal/network"
)

// Teleport issued 2.1s after the last packet must be rejected: dt is
// clamped to 2.0s for the speed computation instead of skipping validation.
func TestAntiCheat_TeleportAfter2_1s_Rejected(t *testing.T) {
	ac := NewAntiCheatManager()

	sess := &network.PlayerSession{
		SessionID: 9001,
		Position:  [3]float32{0, 0, 0},
	}

	// 100m jump with dt=2.1s -> clamped to 2.0s -> 50 m/s > 25 m/s.
	valid, reason := ac.ValidateMove(sess, [3]float32{100, 0, 0}, 2.1)
	if valid {
		t.Fatalf("expected teleport-after-2.1s to be rejected, got valid")
	}
	if reason != "speed violation" {
		t.Fatalf("expected 'speed violation', got %q", reason)
	}

	// Sanity: small move over the same gap stays valid (10m / 2.0s = 5 m/s).
	valid, reason = ac.ValidateMove(sess, [3]float32{10, 0, 0}, 2.1)
	if !valid {
		t.Fatalf("expected small move after 2.1s to be valid, got invalid: %q", reason)
	}
}

// ViolationReset must clear accumulated violations for the session.
func TestAntiCheat_ViolationReset_ClearsViolations(t *testing.T) {
	ac := NewAntiCheatManager()
	sessionID := uint32(9002)

	ac.RecordViolation(sessionID, "speed violation")
	ac.RecordViolation(sessionID, "vertical teleport")
	if count := ac.ViolationCount(sessionID); count != 2 {
		t.Fatalf("expected 2 violations before reset, got %d", count)
	}

	ac.ViolationReset(sessionID)
	if count := ac.ViolationCount(sessionID); count != 0 {
		t.Fatalf("expected 0 violations after ViolationReset, got %d", count)
	}
}
