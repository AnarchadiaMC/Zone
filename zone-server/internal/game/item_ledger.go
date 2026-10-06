package game

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

const (
	// itemActionCacheSize bounds the per-session LRU of processed ActionIDs.
	itemActionCacheSize = 256
	// itemPickupRangeM is how close the picker's last server position must be
	// to the world item to pick it up.
	itemPickupRangeM = 3.0
	// itemPosBound mirrors the transform coordinate bound: drops outside the
	// playable box are clamped instead of trusted.
	itemPosBound = 10000.0
)

// cachedItemResult is one entry of the bounded ActionID idempotency ring.
type cachedItemResult struct {
	actionID uint32
	packet   protocol.ItemUpdatePacket
}

// cachedContainerResult is the OpContainerUpdate twin of cachedItemResult.
// Both caches share the per-session monotonic ActionID floor and rate window,
// so 0x007D and 0x007F cannot replay each other's action IDs.
type cachedContainerResult struct {
	actionID uint32
	packet   protocol.ContainerUpdatePacket
}

// cachedStashResult is the OpStashResult twin of cachedItemResult. It shares
// the same floor and rate window, so 0x0081 cannot replay a 0x007D/0x007F ID.
type cachedStashResult struct {
	actionID uint32
	packet   protocol.StashResultPacket
}

// cachedTradeResult is the OpTradeResult twin of cachedItemResult. It shares
// the same floor and rate window.
type cachedTradeResult struct {
	actionID uint32
	packet   protocol.TradeResultPacket
}

// itemActionState is the per-session ledger bookkeeping: monotonic ActionID
// floor, fixed-size LRUs of recent item and container results, and the
// rate-limit window.
type itemActionState struct {
	lastActionID   uint32
	hasLast        bool
	cache          [itemActionCacheSize]cachedItemResult
	cachePos       int
	cacheLen       int
	containerCache [itemActionCacheSize]cachedContainerResult
	containerPos   int
	containerLen   int
	stashCache     [itemActionCacheSize]cachedStashResult
	stashPos       int
	stashLen       int
	tradeCache     [itemActionCacheSize]cachedTradeResult
	tradePos       int
	tradeLen       int
	rate           []time.Time
}

// recordFloor advances the shared monotonic ActionID floor.
func (st *itemActionState) recordFloor(actionID uint32) {
	st.lastActionID = actionID
	st.hasLast = true
}

// ItemLedger provides dupe-proof idempotency and per-session rate limiting for
// OpItemAction. A replayed ActionID echoes its cached OpItemUpdate instead of
// touching the database again.
type ItemLedger struct {
	mu       sync.Mutex
	sessions map[uint32]*itemActionState
	maxPerS  float64
}

// NewItemLedger builds a ledger with the given per-second action budget.
func NewItemLedger(maxPerS float64) *ItemLedger {
	if maxPerS <= 0 {
		maxPerS = 5
	}
	return &ItemLedger{
		sessions: make(map[uint32]*itemActionState),
		maxPerS:  maxPerS,
	}
}

func (l *ItemLedger) stateLocked(sessionID uint32) *itemActionState {
	st := l.sessions[sessionID]
	if st == nil {
		st = &itemActionState{}
		l.sessions[sessionID] = st
	}
	return st
}

// Lookup returns the cached update for a replayed ActionID.
func (l *ItemLedger) Lookup(sessionID, actionID uint32) (protocol.ItemUpdatePacket, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.sessions[sessionID]
	if st == nil {
		return protocol.ItemUpdatePacket{}, false
	}
	for i := 0; i < st.cacheLen; i++ {
		if st.cache[i].actionID == actionID {
			return st.cache[i].packet, true
		}
	}
	return protocol.ItemUpdatePacket{}, false
}

// IsStale reports whether actionID is at or below the session's monotonic
// floor: such an action was either already processed (and therefore cached) or
// is an out-of-order forgery.
func (l *ItemLedger) IsStale(sessionID, actionID uint32) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.sessions[sessionID]
	return st != nil && st.hasLast && actionID <= st.lastActionID
}

// AllowAction consumes one rate-limit token for the session inside a rolling
// one-second window. It returns false when the session is over budget.
func (l *ItemLedger) AllowAction(sessionID uint32, now time.Time) bool {
	allowed := int(math.Floor(l.maxPerS))
	if allowed < 1 {
		allowed = 1
	}
	cutoff := now.Add(-time.Second)
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(sessionID)
	kept := st.rate[:0]
	for _, t := range st.rate {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	st.rate = kept
	if len(st.rate) >= allowed {
		return false
	}
	st.rate = append(st.rate, now)
	return true
}

// Record advances the monotonic floor and caches the result so a retransmit of
// the same ActionID echoes it.
func (l *ItemLedger) Record(sessionID, actionID uint32, result protocol.ItemUpdatePacket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(sessionID)
	st.recordFloor(actionID)
	st.cache[st.cachePos] = cachedItemResult{actionID: actionID, packet: result}
	st.cachePos = (st.cachePos + 1) % itemActionCacheSize
	if st.cacheLen < itemActionCacheSize {
		st.cacheLen++
	}
}

// LookupContainer returns the cached OpContainerUpdate for a replayed
// ActionID. The cache shares the item ledger's monotonic floor.
func (l *ItemLedger) LookupContainer(sessionID, actionID uint32) (protocol.ContainerUpdatePacket, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.sessions[sessionID]
	if st == nil {
		return protocol.ContainerUpdatePacket{}, false
	}
	for i := 0; i < st.containerLen; i++ {
		if st.containerCache[i].actionID == actionID {
			return st.containerCache[i].packet, true
		}
	}
	return protocol.ContainerUpdatePacket{}, false
}

// RecordContainer advances the shared monotonic floor and caches an
// OpContainerUpdate result for retransmit echo.
func (l *ItemLedger) RecordContainer(sessionID, actionID uint32, result protocol.ContainerUpdatePacket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(sessionID)
	st.recordFloor(actionID)
	st.containerCache[st.containerPos] = cachedContainerResult{actionID: actionID, packet: result}
	st.containerPos = (st.containerPos + 1) % itemActionCacheSize
	if st.containerLen < itemActionCacheSize {
		st.containerLen++
	}
}

// LookupStash returns the cached OpStashResult for a replayed ActionID. The
// cache shares the item ledger's monotonic floor.
func (l *ItemLedger) LookupStash(sessionID, actionID uint32) (protocol.StashResultPacket, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.sessions[sessionID]
	if st == nil {
		return protocol.StashResultPacket{}, false
	}
	for i := 0; i < st.stashLen; i++ {
		if st.stashCache[i].actionID == actionID {
			return st.stashCache[i].packet, true
		}
	}
	return protocol.StashResultPacket{}, false
}

// RecordStash advances the shared monotonic floor and caches an OpStashResult
// for retransmit echo.
func (l *ItemLedger) RecordStash(sessionID, actionID uint32, result protocol.StashResultPacket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(sessionID)
	st.recordFloor(actionID)
	st.stashCache[st.stashPos] = cachedStashResult{actionID: actionID, packet: result}
	st.stashPos = (st.stashPos + 1) % itemActionCacheSize
	if st.stashLen < itemActionCacheSize {
		st.stashLen++
	}
}

// LookupTrade returns the cached OpTradeResult for a replayed ActionID. The
// cache shares the item ledger's monotonic floor.
func (l *ItemLedger) LookupTrade(sessionID, actionID uint32) (protocol.TradeResultPacket, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.sessions[sessionID]
	if st == nil {
		return protocol.TradeResultPacket{}, false
	}
	for i := 0; i < st.tradeLen; i++ {
		if st.tradeCache[i].actionID == actionID {
			return st.tradeCache[i].packet, true
		}
	}
	return protocol.TradeResultPacket{}, false
}

// RecordTrade advances the shared monotonic floor and caches an OpTradeResult
// for retransmit echo.
func (l *ItemLedger) RecordTrade(sessionID, actionID uint32, result protocol.TradeResultPacket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.stateLocked(sessionID)
	st.recordFloor(actionID)
	st.tradeCache[st.tradePos] = cachedTradeResult{actionID: actionID, packet: result}
	st.tradePos = (st.tradePos + 1) % itemActionCacheSize
	if st.tradeLen < itemActionCacheSize {
		st.tradeLen++
	}
}

// Remove drops all ledger state for a departed session.
func (l *ItemLedger) Remove(sessionID uint32) {
	l.mu.Lock()
	delete(l.sessions, sessionID)
	l.mu.Unlock()
}

// validItemSection mirrors the loadout section validation: non-empty, at most
// 63 bytes (64-byte wire field) and printable characters only, so an item
// section always matches a valid inventory row format.
func validItemSection(section string) bool {
	if section == "" || len(section) > 63 || !utf8.ValidString(section) {
		return false
	}
	for _, r := range section {
		if !unicode.IsPrint(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// clampItemCoord bounds a client-supplied drop coordinate, mapping NaN/Inf to
// the origin instead of persisting garbage.
func clampItemCoord(v float32) float32 {
	f := float64(v)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	if f > itemPosBound {
		return itemPosBound
	}
	if f < -itemPosBound {
		return -itemPosBound
	}
	return v
}

// clampCountToInt16 narrows a stack delta onto the wire int16 field.
func clampCountToInt16(n int) int16 {
	if n > math.MaxInt16 {
		return math.MaxInt16
	}
	if n < math.MinInt16 {
		return math.MinInt16
	}
	return int16(n)
}

// conditionToWire converts a DB REAL 0..1 condition into the wire 0-100 u8.
func conditionToWire(c float32) uint8 {
	pct := float64(c) * 100.0
	if math.IsNaN(pct) || pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return uint8(math.Round(pct))
}

// handleItemAction is the OpItemAction (0x007D) path. Replay gating and ACKing
// already happened generically in HandlePacket; this layer adds ActionID
// idempotency, rate limiting and the transactional ledger operations.
func (s *Server) handleItemAction(addr *net.UDPAddr, buf *bytes.Reader) {
	var pkt protocol.ItemActionPacket
	if err := binary.Read(buf, binary.LittleEndian, &pkt); err != nil {
		return
	}
	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil || s.itemLedger == nil {
		return
	}

	if pkt.Action != protocol.ItemActionDrop && pkt.Action != protocol.ItemActionPickup && pkt.Action != protocol.ItemActionConsume {
		s.replyItemUpdate(sess, protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		})
		return
	}

	// Retransmit: echo the cached result without re-applying or re-charging
	// the rate limit.
	if cached, ok := s.itemLedger.Lookup(sess.SessionID, pkt.ActionID); ok {
		s.SendToSession(sess, protocol.OpItemUpdate, protocol.FlagReliable, cached)
		return
	}
	if s.itemLedger.IsStale(sess.SessionID, pkt.ActionID) {
		s.replyItemUpdate(sess, protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		})
		// A stale/out-of-order action may have been applied locally; resync.
		s.forceInventorySync(sess)
		return
	}
	if !s.itemLedger.AllowAction(sess.SessionID, time.Now()) {
		s.replyItemUpdate(sess, protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		})
		return
	}

	sess.Lock()
	uuid := sess.AccountID
	level := sess.CurrentLevel
	playerPos := sess.Position
	sess.Unlock()
	if uuid == "" || s.db == nil {
		s.replyItemUpdate(sess, protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		})
		return
	}

	section := nullTermString(pkt.Section[:])
	if !validItemSection(section) {
		s.replyItemUpdate(sess, protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		})
		s.forceInventorySync(sess)
		return
	}

	if pkt.Action == protocol.ItemActionDrop {
		s.handleItemDrop(sess, uuid, level, section, pkt)
		return
	}
	if pkt.Action == protocol.ItemActionConsume {
		s.handleItemConsume(sess, uuid, section, pkt)
		return
	}
	s.handleItemPickup(sess, uuid, level, playerPos, pkt)
}

// handleItemDrop removes N copies from the character's inventory and creates a
// world_items row in one transaction, then broadcasts the new world item to
// the level. A missing/mismatched stack yields result=2 plus an inventory
// resync so the client can reconcile.
func (s *Server) handleItemDrop(sess *network.PlayerSession, uuid, level, section string, pkt protocol.ItemActionPacket) {
	if pkt.Count == 0 {
		s.replyItemUpdate(sess, protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		})
		s.forceInventorySync(sess)
		return
	}

	x := clampItemCoord(pkt.X)
	y := clampItemCoord(pkt.Y)
	z := clampItemCoord(pkt.Z)
	// The client condition only chooses which inventory stack is consumed
	// first; the world item's condition is derived from the removed row.
	preferredBucket := int(pkt.Condition)
	if preferredBucket > 100 {
		preferredBucket = 100
	}

	newID, remaining, droppedCondition, err := s.db.DropItemToWorld(uuid, level, section, int(pkt.Count), x, y, z, preferredBucket, s.worldItemMaxPerLevel())
	if err != nil {
		update := protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		}
		insufficient := errors.Is(err, database.ErrInsufficientItems)
		if insufficient {
			update.Result = protocol.ItemResultCorrected
			update.Count = clampCountToInt16(-remaining)
		}
		s.itemLedger.Record(sess.SessionID, pkt.ActionID, update)
		s.replyItemUpdate(sess, update)
		// An insufficient-stock drop changed nothing server-side, so the
		// correction alone is authoritative. Other failures may follow a
		// partial client-local mutation and still force a resync.
		if !insufficient {
			s.forceInventorySync(sess)
		}
		s.audit(uuid, "item_drop_rejected",
			fmt.Sprintf("action_id=%d section=%s count=%d err=%v", pkt.ActionID, section, pkt.Count, err))
		return
	}

	var sec [64]byte
	copyNulTerm(sec[:], section)
	update := protocol.ItemUpdatePacket{
		ActionID:  pkt.ActionID,
		Result:    protocol.ItemResultOK,
		Action:    protocol.ItemActionDrop,
		ItemID:    uint32(newID),
		Count:     clampCountToInt16(-int(pkt.Count)),
		Section:   sec,
		X:         x,
		Y:         y,
		Z:         z,
		Condition: conditionToWire(droppedCondition),
	}
	s.itemLedger.Record(sess.SessionID, pkt.ActionID, update)
	s.broadcastItemUpdate(sess, level, x, z, update)
	s.audit(uuid, "item_drop",
		fmt.Sprintf("action_id=%d section=%s count=%d world_item=%d", pkt.ActionID, section, pkt.Count, newID))
}

// handleItemPickup validates level/range against the persisted row, then wins
// or loses the atomic delete-and-credit race. Section/count/condition always
// come from the DB row, never from the client.
func (s *Server) handleItemPickup(sess *network.PlayerSession, uuid, level string, playerPos [3]float32, pkt protocol.ItemActionPacket) {
	if pkt.ItemID == 0 {
		s.replyItemUpdate(sess, protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		})
		return
	}

	wi, err := s.db.GetWorldItem(int64(pkt.ItemID))
	if err != nil {
		s.rejectedItemPickup(sess, uuid, pkt, err)
		return
	}
	if wi.LevelName != level {
		s.rejectedItemPickup(sess, uuid, pkt, fmt.Errorf("level mismatch: item=%s picker=%s", wi.LevelName, level))
		return
	}
	if Displacement([3]float32{wi.PosX, wi.PosY, wi.PosZ}, playerPos) > itemPickupRangeM {
		s.rejectedItemPickup(sess, uuid, pkt, fmt.Errorf("out of pickup range"))
		return
	}

	picked, err := s.db.PickupWorldItem(uuid, int64(pkt.ItemID))
	if err != nil {
		s.rejectedItemPickup(sess, uuid, pkt, err)
		return
	}

	var sec [64]byte
	copyNulTerm(sec[:], picked.Section)
	update := protocol.ItemUpdatePacket{
		ActionID:  pkt.ActionID,
		Result:    protocol.ItemResultOK,
		Action:    protocol.ItemActionPickup,
		ItemID:    uint32(picked.ID),
		Count:     clampCountToInt16(picked.Count),
		Section:   sec,
		X:         picked.PosX,
		Y:         picked.PosY,
		Z:         picked.PosZ,
		Condition: conditionToWire(picked.Condition),
	}
	s.itemLedger.Record(sess.SessionID, pkt.ActionID, update)
	s.broadcastItemUpdate(sess, level, picked.PosX, picked.PosZ, update)
	s.audit(uuid, "item_pickup",
		fmt.Sprintf("action_id=%d section=%s count=%d world_item=%d", pkt.ActionID, picked.Section, picked.Count, picked.ID))
}

func (s *Server) rejectedItemPickup(sess *network.PlayerSession, uuid string, pkt protocol.ItemActionPacket, cause error) {
	update := protocol.ItemUpdatePacket{
		ActionID: pkt.ActionID,
		Result:   protocol.ItemResultRejected,
		Action:   pkt.Action,
		Section:  pkt.Section,
	}
	s.itemLedger.Record(sess.SessionID, pkt.ActionID, update)
	s.replyItemUpdate(sess, update)
	// A rejected pickup is a client-local mutation (the client removes the
	// ground item optimistically): force a full resync so the rejected world
	// item reappears instead of becoming a ghost.
	s.forceInventorySync(sess)
	s.audit(uuid, "item_pickup_rejected",
		fmt.Sprintf("action_id=%d world_item=%d err=%v", pkt.ActionID, pkt.ItemID, cause))
}

// replyItemUpdate sends an OpItemUpdate reliably to a single session.
func (s *Server) replyItemUpdate(sess *network.PlayerSession, update protocol.ItemUpdatePacket) {
	s.SendToSession(sess, protocol.OpItemUpdate, protocol.FlagReliable, update)
}

// broadcastItemUpdate delivers the update to the actor (always, so the ledger
// ack/echo is reliable) and to same-level sessions inside the AoI radius of
// the item position instead of every same-level session. The old
// O(players-per-level) scan is only used as a fallback before the actor is
// registered in the spatial grid (early tests, pre-transform state); normal
// gameplay uses the grid index. Distances are horizontal (x/z), matching the
// snapshot AoI.
func (s *Server) broadcastItemUpdate(sender *network.PlayerSession, level string, x, z float32, update protocol.ItemUpdatePacket) {
	senderID := uint32(0)
	if sender != nil {
		senderID = sender.SessionID
		s.SendToSession(sender, protocol.OpItemUpdate, protocol.FlagReliable, update)
	}

	if s.grid == nil || sender == nil || !s.grid.Contains(senderID) {
		// Fallback: direct distance filter so tests and the pre-grid window
		// still get correct AoI semantics.
		for _, sess := range s.sessions.GetAll() {
			if sess.SessionID == senderID {
				continue
			}
			sess.Lock()
			same := sess.CurrentLevel == level
			sx, sz := sess.Position[0], sess.Position[2]
			sess.Unlock()
			if !same {
				continue
			}
			dx, dz := sx-x, sz-z
			if dx*dx+dz*dz <= AoIRadius*AoIRadius {
				s.SendToSession(sess, protocol.OpItemUpdate, protocol.FlagReliable, update)
			}
		}
		return
	}

	for _, id := range s.grid.GetNeighbors(x, z, AoIRadius) {
		if id == senderID {
			continue
		}
		sess := s.sessions.GetByID(id)
		if sess == nil {
			continue
		}
		sess.Lock()
		same := sess.CurrentLevel == level
		sess.Unlock()
		if same {
			s.SendToSession(sess, protocol.OpItemUpdate, protocol.FlagReliable, update)
		}
	}
}

// forceInventorySync clears the per-level sync marker and resends the full
// inventory so a corrected client reconciles from server state.
func (s *Server) forceInventorySync(sess *network.PlayerSession) {
	if sess == nil {
		return
	}
	sess.Lock()
	sess.InventorySyncedLevel = ""
	sess.Unlock()
	s.syncInventoryForLevel(sess)
}

// audit writes one audit_log row when the database is present and always emits
// a structured log line.
func (s *Server) audit(clientUUID, eventType, detail string) {
	if s.db != nil {
		if err := s.db.InsertAudit(clientUUID, eventType, detail); err != nil && s.logger != nil {
			s.logger.Warn("audit write failed", zap.String("event", eventType), zap.Error(err))
		}
	}
	if s.logger != nil {
		s.logger.Info("audit",
			zap.String("uuid", clientUUID),
			zap.String("event", eventType),
			zap.String("detail", detail),
		)
	}
}
