package network

import (
	"net"
	"testing"
	"time"
)

func TestSessionManager(t *testing.T) {
	sm := NewSessionManager()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")

	sess := &PlayerSession{
		SessionID: 1,
		AccountID: "acc-1",
		UDPAddr:   addr,
		LastSeen:  time.Now(),
	}

	sm.AddSession(sess)

	if sm.Count() != 1 {
		t.Fatalf("Expected session count 1, got %d", sm.Count())
	}

	if s := sm.GetByID(1); s == nil || s.AccountID != "acc-1" {
		t.Errorf("GetByID failed")
	}

	if s := sm.GetByAddr(addr.String()); s == nil || s.SessionID != 1 {
		t.Errorf("GetByAddr failed")
	}

	sm.RemoveSession(1)
	if sm.Count() != 0 {
		t.Errorf("Expected session count 0 after remove, got %d", sm.Count())
	}
}

func TestAckQueue(t *testing.T) {
	aq := NewAckQueue()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")

	aq.EnqueueReliable(100, addr, []byte{1, 2, 3})

	if len(aq.pending) != 1 {
		t.Errorf("Expected 1 pending ACK entry, got %d", len(aq.pending))
	}

	aq.AckReceived(100)

	if len(aq.pending) != 0 {
		t.Errorf("Expected 0 pending ACK entries after ACK, got %d", len(aq.pending))
	}
}
