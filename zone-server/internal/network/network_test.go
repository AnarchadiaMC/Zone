package network

import (
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// Same-addr reconnect must evict the previous session and return it so the
// caller can run full disconnect cleanup (group/grid/AoI/ack).
func TestTryAddCappedReturnsEvictedSameAddr(t *testing.T) {
	sm := NewSessionManager()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12345")

	old := &PlayerSession{SessionID: 1, Name: "Old", UDPAddr: addr, LastSeen: time.Now()}
	if evicted, added := sm.TryAddCapped(old, 5); !added || len(evicted) != 0 {
		t.Fatalf("initial insert: added=%v evicted=%d, want true/0", added, len(evicted))
	}

	newer := &PlayerSession{SessionID: 2, Name: "New", UDPAddr: addr, LastSeen: time.Now()}
	evicted, added := sm.TryAddCapped(newer, 1)
	if !added {
		t.Fatal("same-addr reconnect was rejected at cap")
	}
	if len(evicted) != 1 || evicted[0] != old {
		t.Fatalf("evicted = %v, want the previous same-addr session", evicted)
	}
	if sm.GetByID(1) != nil {
		t.Fatal("evicted session still resolvable by ID")
	}
	if sm.Count() != 1 {
		t.Fatalf("count = %d, want 1 after eviction+replace", sm.Count())
	}
	if sm.GetByAddr(addr.String()) != newer {
		t.Fatal("byAddr does not point at the new session")
	}
}

// Same-SessionID replacement must also be reported as evicted.
func TestTryAddCappedReturnsEvictedSameID(t *testing.T) {
	sm := NewSessionManager()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:12346")

	old := &PlayerSession{SessionID: 7, Name: "Old", UDPAddr: addr, LastSeen: time.Now()}
	sm.AddSession(old)
	replacement := &PlayerSession{SessionID: 7, Name: "New", UDPAddr: addr, LastSeen: time.Now()}

	evicted, added := sm.TryAddCapped(replacement, 1)
	if !added || len(evicted) != 1 || evicted[0] != old {
		t.Fatalf("same-ID replace: added=%v evicted=%v, want true and old session", added, evicted)
	}
	if sm.GetByID(7) != replacement {
		t.Fatal("session manager did not keep the replacement")
	}
}

// Handshake names are made unique case-insensitively with deterministic "~n"
// suffixes.
func TestEnsureUniqueName(t *testing.T) {
	sm := NewSessionManager()
	first := &PlayerSession{SessionID: 1, Name: "Ghost"}
	sm.AddSession(first)
	sm.EnsureUniqueName(first)
	if first.Name != "Ghost" {
		t.Fatalf("first name changed to %q, want Ghost", first.Name)
	}

	second := &PlayerSession{SessionID: 2, Name: "ghost"}
	sm.AddSession(second)
	sm.EnsureUniqueName(second)
	if second.Name != "ghost~2" {
		t.Fatalf("second name = %q, want ghost~2", second.Name)
	}

	third := &PlayerSession{SessionID: 3, Name: "Ghost"}
	sm.AddSession(third)
	sm.EnsureUniqueName(third)
	if third.Name != "Ghost~3" {
		t.Fatalf("third name = %q, want Ghost~3", third.Name)
	}
}

// Suffixed names never exceed the 31-byte wire cap and stay valid UTF-8.
func TestEnsureUniqueNameCapsAt31BytesUTF8Safe(t *testing.T) {
	sm := NewSessionManager()
	base := strings.Repeat("Ж", 15) // 30 bytes
	first := &PlayerSession{SessionID: 1, Name: base}
	sm.AddSession(first)
	sm.EnsureUniqueName(first)

	second := &PlayerSession{SessionID: 2, Name: base}
	sm.AddSession(second)
	sm.EnsureUniqueName(second)

	if len(second.Name) > 31 {
		t.Fatalf("unique name is %d bytes, want <= 31", len(second.Name))
	}
	if !utf8.ValidString(second.Name) {
		t.Fatalf("unique name %q is not valid UTF-8", second.Name)
	}
	if second.Name == base {
		t.Fatal("duplicate name was not suffixed")
	}
}

// GetByName is case-insensitive because names are unique case-insensitively.
func TestGetByNameCaseInsensitive(t *testing.T) {
	sm := NewSessionManager()
	sess := &PlayerSession{SessionID: 1, Name: "Alpha"}
	sm.AddSession(sess)
	if got := sm.GetByName("alpha"); got != sess {
		t.Fatalf("GetByName(alpha) = %v, want Alpha session", got)
	}
	if got := sm.GetByName("ALPHA"); got != sess {
		t.Fatalf("GetByName(ALPHA) = %v, want Alpha session", got)
	}
	if got := sm.GetByName("Bravo"); got != nil {
		t.Fatalf("GetByName(Bravo) = %v, want nil", got)
	}
}
