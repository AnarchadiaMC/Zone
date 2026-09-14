package game

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/network"
)

func TestSleeper_CreateFromSession(t *testing.T) {
	mgr := NewSleeperManager(zap.NewNop())

	// 1. Create from a valid session with explicit SessionID
	sess := &network.PlayerSession{
		SessionID:    1042,
		AccountID:    "stalker-uuid-42",
		CurrentLevel: "l01_escape",
		Position:     [3]float32{123.4, 5.6, -78.9},
		Rotation:     [2]float32{180.0, -15.0},
		Health:       85.5,
	}

	duration := 30 * time.Second
	before := time.Now()
	s := mgr.CreateSleeper(sess, duration)
	after := time.Now()

	if s == nil {
		t.Fatal("expected non-nil sleeper")
	}

	if s.EntityID != 1042 {
		t.Errorf("expected EntityID 1042, got %d", s.EntityID)
	}
	if s.AccountID != "stalker-uuid-42" {
		t.Errorf("expected AccountID stalker-uuid-42, got %s", s.AccountID)
	}
	if s.CurrentLevel != "l01_escape" {
		t.Errorf("expected CurrentLevel l01_escape, got %s", s.CurrentLevel)
	}
	if s.Position != [3]float32{123.4, 5.6, -78.9} {
		t.Errorf("expected Position [123.4, 5.6, -78.9], got %v", s.Position)
	}
	if s.Rotation != [2]float32{180.0, -15.0} {
		t.Errorf("expected Rotation [180.0, -15.0], got %v", s.Rotation)
	}
	if math.Abs(float64(s.Health-85.5)) > 0.001 {
		t.Errorf("expected Health 85.5, got %f", s.Health)
	}
	if s.IsDead {
		t.Error("expected IsDead to be false")
	}
	if s.ExpiresAt.Before(before.Add(duration)) || s.ExpiresAt.After(after.Add(duration)) {
		t.Errorf("ExpiresAt out of bounds: %v", s.ExpiresAt)
	}

	// 2. Create from a session with SessionID == 0 (should auto-generate non-zero EntityID)
	sessZero := &network.PlayerSession{
		SessionID:    0,
		AccountID:    "stalker-uuid-zero",
		CurrentLevel: "l02_garbage",
		Health:       100.0,
	}
	sZero := mgr.CreateSleeper(sessZero, duration)
	if sZero == nil {
		t.Fatal("expected non-nil sleeper for zero session id")
	}
	if sZero.EntityID == 0 {
		t.Error("expected auto-assigned non-zero EntityID")
	}

	// 3. Nil session should return nil
	if nilSleeper := mgr.CreateSleeper(nil, duration); nilSleeper != nil {
		t.Errorf("expected nil sleeper for nil session, got %v", nilSleeper)
	}
}

func TestSleeper_GetAndLookup(t *testing.T) {
	mgr := NewSleeperManager()

	sess1 := &network.PlayerSession{
		SessionID:    201,
		AccountID:    "account-alpha",
		CurrentLevel: "l01_escape",
		Health:       100.0,
	}
	sess2 := &network.PlayerSession{
		SessionID:    202,
		AccountID:    "account-beta",
		CurrentLevel: "l02_garbage",
		Health:       90.0,
	}

	s1 := mgr.CreateSleeper(sess1, 30*time.Second)
	s2 := mgr.CreateSleeper(sess2, 30*time.Second)

	// Lookup by entity ID
	if got := mgr.GetSleeper(201); got != s1 {
		t.Errorf("expected s1, got %v", got)
	}
	if got := mgr.GetSleeper(202); got != s2 {
		t.Errorf("expected s2, got %v", got)
	}
	if got := mgr.GetSleeper(9999); got != nil {
		t.Errorf("expected nil for unknown entity, got %v", got)
	}

	// Lookup by account ID
	if got := mgr.GetSleeperByAccount("account-alpha"); got != s1 {
		t.Errorf("expected s1, got %v", got)
	}
	if got := mgr.GetSleeperByAccount("account-beta"); got != s2 {
		t.Errorf("expected s2, got %v", got)
	}
	if got := mgr.GetSleeperByAccount("unknown-account"); got != nil {
		t.Errorf("expected nil for unknown account, got %v", got)
	}

	// GetAll
	all := mgr.GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 sleepers in GetAll, got %d", len(all))
	}
	if mgr.Count() != 2 {
		t.Fatalf("expected Count() == 2, got %d", mgr.Count())
	}

	// Remove s1
	removed := mgr.RemoveSleeper(201)
	if removed != s1 {
		t.Errorf("expected removed s1, got %v", removed)
	}
	if got := mgr.GetSleeper(201); got != nil {
		t.Errorf("expected nil after removal, got %v", got)
	}
	if got := mgr.GetSleeperByAccount("account-alpha"); got != nil {
		t.Errorf("expected nil account lookup after removal, got %v", got)
	}
	if mgr.Count() != 1 {
		t.Errorf("expected Count() == 1, got %d", mgr.Count())
	}

	// Removing non-existent returns nil
	if removedNonExistent := mgr.RemoveSleeper(9999); removedNonExistent != nil {
		t.Errorf("expected nil when removing non-existent sleeper, got %v", removedNonExistent)
	}
}

func TestSleeper_ApplyDamage(t *testing.T) {
	mgr := NewSleeperManager()

	sess := &network.PlayerSession{
		SessionID:    301,
		AccountID:    "victim-uuid",
		CurrentLevel: "l03_agroprom",
		Health:       100.0,
	}
	s := mgr.CreateSleeper(sess, 30*time.Second)

	// 1. Partial damage
	remaining, isDead := mgr.ApplyDamage(s.EntityID, 30.0)
	if isDead {
		t.Error("sleeper should not be dead after 30 damage")
	}
	if math.Abs(float64(remaining-70.0)) > 0.001 {
		t.Errorf("expected 70.0 remaining, got %f", remaining)
	}

	// 2. Another partial hit
	remaining, isDead = mgr.ApplyDamage(s.EntityID, 40.0)
	if isDead {
		t.Error("sleeper should not be dead after another 40 damage")
	}
	if math.Abs(float64(remaining-30.0)) > 0.001 {
		t.Errorf("expected 30.0 remaining, got %f", remaining)
	}

	// 3. Fatal overkill hit
	remaining, isDead = mgr.ApplyDamage(s.EntityID, 50.0)
	if !isDead {
		t.Error("sleeper should be marked dead after overkill damage")
	}
	if remaining != 0.0 {
		t.Errorf("expected 0.0 health, got %f", remaining)
	}

	// 4. Hit already dead sleeper
	remaining, isDead = mgr.ApplyDamage(s.EntityID, 20.0)
	if !isDead {
		t.Error("already dead sleeper should remain dead")
	}
	if remaining != 0.0 {
		t.Errorf("expected 0.0 remaining on dead sleeper, got %f", remaining)
	}

	// 5. Apply damage to non-existent entity
	remaining, isDead = mgr.ApplyDamage(999999, 50.0)
	if isDead || remaining != 0.0 {
		t.Errorf("expected (0, false) for non-existent sleeper, got (%f, %v)", remaining, isDead)
	}
}

func TestSleeper_TickExpire(t *testing.T) {
	mgr := NewSleeperManager()

	duration := 100 * time.Millisecond
	sess := &network.PlayerSession{
		SessionID:    401,
		AccountID:    "expiring-stalker",
		CurrentLevel: "l01_escape",
		Health:       100.0,
	}
	s := mgr.CreateSleeper(sess, duration)

	var expired []*Sleeper
	var killed []*Sleeper

	onExpire := func(sl *Sleeper) {
		expired = append(expired, sl)
	}
	onDeath := func(sl *Sleeper) {
		killed = append(killed, sl)
	}

	// Tick immediately -> should not expire
	mgr.Tick(time.Now(), onExpire, onDeath)
	if len(expired) != 0 || len(killed) != 0 {
		t.Fatalf("no sleepers should expire immediately, got %d expired, %d killed", len(expired), len(killed))
	}
	if mgr.GetSleeper(s.EntityID) == nil {
		t.Fatal("sleeper should still exist")
	}

	// Tick in the future past expiration
	future := time.Now().Add(200 * time.Millisecond)
	mgr.Tick(future, onExpire, onDeath)

	if len(expired) != 1 {
		t.Fatalf("expected 1 expired sleeper, got %d", len(expired))
	}
	if expired[0].EntityID != 401 {
		t.Errorf("expected expired entityID 401, got %d", expired[0].EntityID)
	}
	if len(killed) != 0 {
		t.Errorf("expected 0 killed sleepers, got %d", len(killed))
	}

	// Sleeper must be removed from manager
	if mgr.GetSleeper(401) != nil {
		t.Error("sleeper should be removed after expiration")
	}
	if mgr.GetSleeperByAccount("expiring-stalker") != nil {
		t.Error("account mapping should be removed after expiration")
	}
}

func TestSleeper_TickDeath(t *testing.T) {
	mgr := NewSleeperManager()

	sess := &network.PlayerSession{
		SessionID:    501,
		AccountID:    "doomed-stalker",
		CurrentLevel: "l05_bar_rostok",
		Health:       100.0,
	}
	s := mgr.CreateSleeper(sess, 1*time.Hour)

	// Apply lethal damage
	rem, isDead := mgr.ApplyDamage(s.EntityID, 120.0)
	if !isDead || rem != 0.0 {
		t.Fatalf("expected sleeper to be dead, got rem=%f isDead=%v", rem, isDead)
	}

	var expired []*Sleeper
	var killed []*Sleeper

	onExpire := func(sl *Sleeper) {
		expired = append(expired, sl)
	}
	onDeath := func(sl *Sleeper) {
		killed = append(killed, sl)
	}

	// Tick now
	mgr.Tick(time.Now(), onExpire, onDeath)

	if len(killed) != 1 {
		t.Fatalf("expected 1 killed sleeper, got %d", len(killed))
	}
	if killed[0].EntityID != 501 {
		t.Errorf("expected killed entityID 501, got %d", killed[0].EntityID)
	}
	if len(expired) != 0 {
		t.Errorf("expected 0 expired sleepers, got %d", len(expired))
	}

	// Sleeper must be removed from manager
	if mgr.GetSleeper(501) != nil {
		t.Error("sleeper should be removed after death")
	}
	if mgr.GetSleeperByAccount("doomed-stalker") != nil {
		t.Error("account mapping should be removed after death")
	}
}

func TestSleeper_TickDeadAndExpired(t *testing.T) {
	mgr := NewSleeperManager()

	sess := &network.PlayerSession{
		SessionID:    601,
		AccountID:    "dead-and-expired-stalker",
		CurrentLevel: "l04_darkvalley",
		Health:       50.0,
	}
	s := mgr.CreateSleeper(sess, 50*time.Millisecond)

	// Kill it
	mgr.ApplyDamage(s.EntityID, 100.0)

	var expiredCount atomic.Int32
	var killedCount atomic.Int32

	onExpire := func(sl *Sleeper) {
		expiredCount.Add(1)
	}
	onDeath := func(sl *Sleeper) {
		killedCount.Add(1)
	}

	// Advance past expiration
	mgr.Tick(time.Now().Add(1*time.Second), onExpire, onDeath)

	if killedCount.Load() != 1 {
		t.Errorf("expected 1 killed callback, got %d", killedCount.Load())
	}
	if expiredCount.Load() != 0 {
		t.Errorf("expected 0 expired callback for dead sleeper, got %d", expiredCount.Load())
	}
}

func TestSleeper_ReconnectionOrDuplicate(t *testing.T) {
	mgr := NewSleeperManager()

	sessA := &network.PlayerSession{
		SessionID:    701,
		AccountID:    "same-stalker",
		CurrentLevel: "l01_escape",
		Health:       80.0,
	}
	s1 := mgr.CreateSleeper(sessA, 30*time.Second)
	if mgr.GetSleeper(701) != s1 {
		t.Fatal("expected s1 for 701")
	}

	// Same account disconnects again with a new session ID
	sessB := &network.PlayerSession{
		SessionID:    702,
		AccountID:    "same-stalker",
		CurrentLevel: "l02_garbage",
		Health:       95.0,
	}
	s2 := mgr.CreateSleeper(sessB, 30*time.Second)

	if mgr.GetSleeperByAccount("same-stalker") != s2 {
		t.Fatal("expected s2 for same-stalker account")
	}
	// Old entity ID 701 should be evicted
	if mgr.GetSleeper(701) != nil {
		t.Error("expected old session 701 to be evicted when new sleeper created for account")
	}
	if mgr.GetSleeper(702) != s2 {
		t.Error("expected s2 for entity 702")
	}
	if mgr.Count() != 1 {
		t.Errorf("expected Count() == 1, got %d", mgr.Count())
	}
}

func TestSleeper_ConcurrentAccess(t *testing.T) {
	mgr := NewSleeperManager(zap.NewNop())

	const (
		numGoroutines = 8
		numOps        = 100
	)

	var wg sync.WaitGroup

	// Creators
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				id := uint32(gid*1000 + i + 1)
				sess := &network.PlayerSession{
					SessionID:    id,
					AccountID:    fmt.Sprintf("stalker-%d-%d", gid, i),
					CurrentLevel: "l01_escape",
					Position:     [3]float32{float32(i), float32(gid), 0},
					Health:       100.0,
				}
				mgr.CreateSleeper(sess, time.Duration(10+i)*time.Millisecond)
			}
		}(g)
	}

	// Damage Dealers
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				targetID := uint32(gid*1000 + i + 1)
				mgr.ApplyDamage(targetID, 25.0)
			}
		}(g)
	}

	// Query / Readers
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				targetID := uint32(gid*1000 + i + 1)
				_ = mgr.GetSleeper(targetID)
				_ = mgr.GetSleeperByAccount(fmt.Sprintf("stalker-%d-%d", gid, i))
				_ = mgr.GetAll()
				_ = mgr.Count()
			}
		}(g)
	}

	// Tickers
	for g := 0; g < numGoroutines/2; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				mgr.Tick(time.Now().Add(time.Duration(i*5)*time.Millisecond), nil, nil)
				time.Sleep(1 * time.Millisecond)
			}
		}(g)
	}

	// Removers
	for g := 0; g < numGoroutines/2; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				targetID := uint32(gid*1000 + i + 1)
				mgr.RemoveSleeper(targetID)
			}
		}(g)
	}

	wg.Wait()
}
