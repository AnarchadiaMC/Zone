package game

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

func damageSession(id uint32, port int, uuid string, pos [3]float32) *network.PlayerSession {
	return &network.PlayerSession{
		SessionID:    id,
		AccountID:    uuid,
		UDPAddr:      &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port},
		CurrentLevel: "l01_escape",
		Position:     pos,
		Health:       100.0,
	}
}

// A validated hit is relayed to the victim only, carries the attacker's real
// session id and the clamped damage, and is audited with both session ids.
func TestDamageNotify_RelaysToVictimOnly(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	attacker := damageSession(1001, 47001, "uuid-attacker", [3]float32{0, 0, 0})
	target := damageSession(1002, 47002, "uuid-target", [3]float32{10, 0, 0})
	uninvolved := damageSession(1003, 47003, "uuid-uninvolved", [3]float32{20, 0, 0})
	s.sessions.AddSession(attacker)
	s.sessions.AddSession(target)
	s.sessions.AddSession(uninvolved)
	sink.Reset()

	// The packet must carry the attacker's own session id (rule a); a forged
	// value is rejected before any relay.
	dmg := protocol.DamageNotify{TargetID: 1002, AttackerID: 1001, Damage: 40}
	raw := buildTestPacket(t, protocol.OpDamageNotify, 1, protocol.FlagReliable, dmg)
	s.HandlePacket(raw, attacker.UDPAddr)

	relays := packetsByOpcode(sink, protocol.OpDamageNotify)
	if len(relays) != 1 {
		t.Fatalf("OpDamageNotify packets = %d, want exactly 1 relay to the victim", len(relays))
	}
	var out protocol.DamageNotify
	if err := binary.Read(bytes.NewReader(packetPayload(t, relays[0])), binary.LittleEndian, &out); err != nil {
		t.Fatalf("decode relayed DamageNotify: %v", err)
	}
	if out.AttackerID != 1001 || out.TargetID != 1002 {
		t.Fatalf("relayed ids = attacker %d target %d, want 1001/1002", out.AttackerID, out.TargetID)
	}
	if out.Damage != 40 {
		t.Fatalf("relayed damage = %v, want 40", out.Damage)
	}

	target.Lock()
	health := target.Health
	target.Unlock()
	if health != 60 {
		t.Fatalf("victim health = %v, want 60", health)
	}
	uninvolved.Lock()
	otherHealth := uninvolved.Health
	uninvolved.Unlock()
	if otherHealth != 100 {
		t.Fatalf("uninvolved session health changed to %v, want 100", otherHealth)
	}

	var accepted int
	if err := db.RawDB().QueryRow(
		"SELECT COUNT(*) FROM audit_log WHERE event_type='damage_applied'").Scan(&accepted); err != nil {
		t.Fatalf("query damage_applied audit: %v", err)
	}
	if accepted != 1 {
		t.Fatalf("damage_applied audit rows = %d, want 1", accepted)
	}
}

// Rejected damage is audited with its reason, rate-limited per attacker so a
// forged-packet flood cannot fill audit_log; nothing is relayed.
func TestDamageNotify_RejectAuditRateLimited(t *testing.T) {
	s, sink, db := setupTestServerWithDB(t)
	attacker := damageSession(2001, 47101, "uuid-reject", [3]float32{0, 0, 0})
	attacker.InSafeZone = true
	target := damageSession(2002, 47102, "uuid-reject-target", [3]float32{10, 0, 0})
	s.sessions.AddSession(attacker)
	s.sessions.AddSession(target)
	sink.Reset()

	dmg := protocol.DamageNotify{TargetID: 2002, AttackerID: 2001, Damage: 25}
	for seq := uint32(1); seq <= 3; seq++ {
		s.HandlePacket(buildTestPacket(t, protocol.OpDamageNotify, seq, protocol.FlagReliable, dmg), attacker.UDPAddr)
	}

	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 0 {
		t.Fatalf("rejected damage relayed %d packets, want 0", got)
	}
	target.Lock()
	health := target.Health
	target.Unlock()
	if health != 100 {
		t.Fatalf("rejected damage changed victim health to %v, want 100", health)
	}

	var rejected int
	if err := db.RawDB().QueryRow(
		"SELECT COUNT(*) FROM audit_log WHERE event_type='damage_rejected'").Scan(&rejected); err != nil {
		t.Fatalf("query damage_rejected audit: %v", err)
	}
	if rejected != 1 {
		t.Fatalf("damage_rejected audit rows = %d, want 1 (rate-limited)", rejected)
	}
}

// Range is validated against the last known server positions, not any value
// supplied by the client packet.
func TestDamageNotify_RangeUsesLastKnownPositions(t *testing.T) {
	s, sink, _ := setupTestServerWithDB(t)
	attacker := damageSession(3001, 47201, "uuid-range", [3]float32{0, 0, 0})
	target := damageSession(3002, 47202, "uuid-range-target", [3]float32{301, 0, 0})
	s.sessions.AddSession(attacker)
	s.sessions.AddSession(target)
	sink.Reset()

	dmg := protocol.DamageNotify{TargetID: 3002, AttackerID: 3001, Damage: 30}
	s.HandlePacket(buildTestPacket(t, protocol.OpDamageNotify, 1, protocol.FlagReliable, dmg), attacker.UDPAddr)

	if got := len(packetsByOpcode(sink, protocol.OpDamageNotify)); got != 0 {
		t.Fatalf("out-of-range hit relayed %d packets, want 0", got)
	}
	target.Lock()
	health := target.Health
	target.Unlock()
	if health != 100 {
		t.Fatalf("out-of-range hit changed victim health to %v, want 100", health)
	}
}
