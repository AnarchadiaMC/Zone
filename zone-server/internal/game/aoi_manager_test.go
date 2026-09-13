package game

import (
	"net"
	"sync/atomic"
	"testing"

	"zone-online/zone-server/internal/network"
)

func TestAoIManager_NotifyEntity(t *testing.T) {
	aoi := NewAoIManager()
	udp, err := network.NewUDPListener(0, func([]byte, *net.UDPAddr) {})
	if err != nil {
		t.Fatalf("Failed to create UDP listener: %v", err)
	}
	defer udp.Close()

	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:9999")
	session := &network.PlayerSession{
		SessionID: 100,
		UDPAddr:   addr,
	}

	info := EntityInfo{
		ID:      42,
		Type:    2,
		Section: "stalker_regular",
		Pos:     [3]float32{10, 20, 30},
		Faction: 1,
		Health:  100,
	}

	var seq atomic.Uint32

	// First enter should succeed and register entry
	aoi.NotifyEntityEnter(session, info, udp, &seq)
	if !aoi.entries[100][42] {
		t.Errorf("Expected entity 42 to be registered in entries")
	}
	if seq.Load() != 1 {
		t.Errorf("Expected sequence 1, got %d", seq.Load())
	}

	// Duplicate enter should be ignored and not increment sequence
	aoi.NotifyEntityEnter(session, info, udp, &seq)
	if seq.Load() != 1 {
		t.Errorf("Expected sequence to remain 1 on duplicate enter, got %d", seq.Load())
	}

	// Entity leave should remove entry and increment sequence
	aoi.NotifyEntityLeave(session, 42, udp, &seq)
	if aoi.entries[100][42] {
		t.Errorf("Expected entity 42 to be removed from entries")
	}
	if seq.Load() != 2 {
		t.Errorf("Expected sequence 2 after leave, got %d", seq.Load())
	}
}
