package game

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
)

func TestAuth_GenerateSessionToken(t *testing.T) {
	const iterations = 1000
	tokens := make(map[uint64]bool, iterations)

	for i := 0; i < iterations; i++ {
		tok, err := GenerateSessionToken()
		if err != nil {
			t.Fatalf("GenerateSessionToken() failed on iteration %d: %v", i, err)
		}
		if tok == 0 {
			t.Fatalf("GenerateSessionToken() returned zero token on iteration %d", i)
		}
		if tokens[tok] {
			t.Fatalf("GenerateSessionToken() produced duplicate token %x on iteration %d", tok, i)
		}
		tokens[tok] = true
	}
}

func TestAuth_CheckPlayerBanned(t *testing.T) {
	t.Run("nil DB handling", func(t *testing.T) {
		banned, reason, err := CheckPlayerBanned(nil, "uuid-any")
		if err != nil {
			t.Errorf("Expected nil error for nil DB, got: %v", err)
		}
		if banned {
			t.Errorf("Expected banned=false for nil DB, got true")
		}
		if reason != "" {
			t.Errorf("Expected empty reason for nil DB, got: %q", reason)
		}
	})

	t.Run("valid DB checking", func(t *testing.T) {
		db, err := database.Open(":memory:")
		if err != nil {
			t.Fatalf("Failed to open test database: %v", err)
		}
		defer db.Close()

		// 1. Check non-existent user returns error (sql.ErrNoRows)
		banned, reason, err := CheckPlayerBanned(db, "uuid-not-found")
		if err == nil {
			t.Errorf("Expected error for non-existent account, got nil")
		} else if !errors.Is(err, sql.ErrNoRows) {
			t.Logf("Returned error for non-existent account as expected: %v", err)
		}
		if banned {
			t.Errorf("Expected banned=false for non-existent account")
		}
		if reason != "" {
			t.Errorf("Expected empty reason for non-existent account, got: %q", reason)
		}

		// 2. Provision active player
		const testUUID = "test-uuid-active"
		if err := db.AutoProvision(testUUID, "hwid-abc", "StalkerPro"); err != nil {
			t.Fatalf("Failed to auto-provision player: %v", err)
		}

		// Verify unbanned status
		banned, reason, err = CheckPlayerBanned(db, testUUID)
		if err != nil {
			t.Errorf("Unexpected error checking unbanned player: %v", err)
		}
		if banned {
			t.Errorf("Expected banned=false for new player, got true")
		}
		if reason != "" {
			t.Errorf("Expected empty reason for unbanned player, got: %q", reason)
		}

		// 3. Ban player
		const banReason = "Speed hacking detected in Cordon"
		if err := db.BanAccount(testUUID, banReason); err != nil {
			t.Fatalf("Failed to ban account: %v", err)
		}

		// Verify banned status
		banned, reason, err = CheckPlayerBanned(db, testUUID)
		if err != nil {
			t.Errorf("Unexpected error checking banned player: %v", err)
		}
		if !banned {
			t.Errorf("Expected banned=true for banned player, got false")
		}
		if reason != banReason {
			t.Errorf("Expected reason %q, got %q", banReason, reason)
		}
	})
}

func TestAuth_AuthenticateSessionPacket(t *testing.T) {
	testCases := []struct {
		name              string
		sess              *network.PlayerSession
		expectedSessionID uint32
		token             uint64
		expectedAuth      bool
	}{
		{
			name:              "nil session",
			sess:              nil,
			expectedSessionID: 10,
			token:             12345,
			expectedAuth:      false,
		},
		{
			name: "mismatched session ID",
			sess: &network.PlayerSession{
				SessionID:    10,
				SessionToken: 99999,
			},
			expectedSessionID: 11,
			token:             99999,
			expectedAuth:      false,
		},
		{
			name: "zero token on both ends",
			sess: &network.PlayerSession{
				SessionID:    10,
				SessionToken: 0,
			},
			expectedSessionID: 10,
			token:             0,
			expectedAuth:      true,
		},
		{
			name: "token provided but session token zero",
			sess: &network.PlayerSession{
				SessionID:    10,
				SessionToken: 0,
			},
			expectedSessionID: 10,
			token:             12345,
			expectedAuth:      true,
		},
		{
			name: "token zero but session token non-zero",
			sess: &network.PlayerSession{
				SessionID:    10,
				SessionToken: 12345,
			},
			expectedSessionID: 10,
			token:             0,
			expectedAuth:      true,
		},
		{
			name: "matching non-zero tokens",
			sess: &network.PlayerSession{
				SessionID:    10,
				SessionToken: 0xDEADBEEFCAFE,
			},
			expectedSessionID: 10,
			token:             0xDEADBEEFCAFE,
			expectedAuth:      true,
		},
		{
			name: "mismatching non-zero tokens",
			sess: &network.PlayerSession{
				SessionID:    10,
				SessionToken: 0xDEADBEEFCAFE,
			},
			expectedSessionID: 10,
			token:             0x123456789ABC,
			expectedAuth:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := AuthenticateSessionPacket(tc.sess, tc.expectedSessionID, tc.token)
			if got != tc.expectedAuth {
				t.Errorf("AuthenticateSessionPacket() = %v, expected %v", got, tc.expectedAuth)
			}
		})
	}
}

func TestAuth_ShouldCheckpointSession(t *testing.T) {
	now := time.Now()
	interval := 5 * time.Second

	t.Run("nil session", func(t *testing.T) {
		if ShouldCheckpointSession(nil, now, interval) {
			t.Errorf("Expected false for nil session")
		}
	})

	t.Run("clean session with elapsed time", func(t *testing.T) {
		sess := &network.PlayerSession{
			Dirty:          false,
			LastCheckpoint: now.Add(-10 * time.Second),
		}
		if ShouldCheckpointSession(sess, now, interval) {
			t.Errorf("Expected false for non-dirty session")
		}
	})

	t.Run("dirty session with interval not elapsed", func(t *testing.T) {
		sess := &network.PlayerSession{
			Dirty:          true,
			LastCheckpoint: now.Add(-2 * time.Second),
		}
		if ShouldCheckpointSession(sess, now, interval) {
			t.Errorf("Expected false for dirty session with interval not elapsed")
		}
	})

	t.Run("dirty session with exact interval elapsed", func(t *testing.T) {
		sess := &network.PlayerSession{
			Dirty:          true,
			LastCheckpoint: now.Add(-5 * time.Second),
		}
		if !ShouldCheckpointSession(sess, now, interval) {
			t.Errorf("Expected true for dirty session with exact interval elapsed")
		}
	})

	t.Run("dirty session with interval exceeded", func(t *testing.T) {
		sess := &network.PlayerSession{
			Dirty:          true,
			LastCheckpoint: now.Add(-20 * time.Second),
		}
		if !ShouldCheckpointSession(sess, now, interval) {
			t.Errorf("Expected true for dirty session with interval exceeded")
		}
	})
}

func TestAuth_PlayerSession_CombatAndDirty(t *testing.T) {
	sess := &network.PlayerSession{
		SessionID:    42,
		SessionToken: 12345,
	}

	// 1. Verify MarkDirty
	if sess.Dirty {
		t.Errorf("Expected Dirty to initially be false")
	}
	sess.MarkDirty()
	if !sess.Dirty {
		t.Errorf("Expected Dirty to be true after MarkDirty()")
	}

	// 2. Verify Combat timers
	now := time.Now()
	if sess.IsInCombat(now) {
		t.Errorf("Expected IsInCombat to be false initially")
	}

	sess.SetCombat(5 * time.Second)
	// Now at current time, player should be in combat
	if !sess.IsInCombat(now) {
		t.Errorf("Expected player to be in combat immediately after SetCombat")
	}

	// In 2 seconds, still in combat
	if !sess.IsInCombat(now.Add(2 * time.Second)) {
		t.Errorf("Expected player to be in combat 2s into 5s duration")
	}

	// In 6 seconds, combat expired
	if sess.IsInCombat(now.Add(6 * time.Second)) {
		t.Errorf("Expected player to not be in combat after 6s")
	}

	// 3. Thread-safety concurrent access test
	var wg sync.WaitGroup
	const goroutines = 20
	const opsPerGoroutine = 50

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				sess.MarkDirty()
				sess.SetCombat(time.Duration(j) * time.Millisecond)
				_ = sess.IsInCombat(time.Now())
				_ = AuthenticateSessionPacket(sess, 42, 12345)
				_ = ShouldCheckpointSession(sess, time.Now(), 100*time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
}
