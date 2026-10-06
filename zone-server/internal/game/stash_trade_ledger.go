package game

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"time"

	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// Stash actions (OpStashAction 0x0081) address a server stash by
// (level, rounded position) instead of a numeric id: the client sends raw X/Y/Z
// floats for the stash it is interacting with, the server rounds each
// coordinate onto the WorldStashGridM grid, and the first store at an empty key
// creates an ownerless world stash reachable within MaxStashInteractDistance by
// anyone. ActionID idempotency, the monotonic replay floor and the per-second
// action budget are shared with OpItemAction/OpContainerAction through the
// ItemLedger, so a 0x0081 replay echoes its cached OpStashResult and can never
// re-apply, and its ActionID space cannot cross-replay another opcode.

// handleStashAction is the OpStashAction (0x0081) path.
func (s *Server) handleStashAction(addr *net.UDPAddr, buf *bytes.Reader) {
	var pkt protocol.StashActionPacket
	if err := binary.Read(buf, binary.LittleEndian, &pkt); err != nil {
		return
	}
	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil || s.itemLedger == nil {
		return
	}

	// reject records the negative result on the shared ledger (so a retransmit
	// echoes it), replies reliably to the sender, then forces the authoritative
	// inventory + wallet so an optimistic local mutation cannot survive.
	reject := func(result uint8, count int16) {
		res := protocol.StashResultPacket{
			ActionID:  pkt.ActionID,
			Result:    result,
			Action:    pkt.Action,
			Count:     count,
			Section:   pkt.Section,
			Condition: pkt.Condition,
		}
		s.itemLedger.RecordStash(sess.SessionID, pkt.ActionID, res)
		s.replyStashResult(sess, res)
		s.forceInventorySync(sess)
		s.sendWalletUpdate(sess)
	}

	if pkt.Action != protocol.StashActionStore && pkt.Action != protocol.StashActionTake {
		reject(protocol.ItemResultRejected, 0)
		return
	}

	// Retransmit: echo the cached result without re-applying or re-charging.
	if cached, ok := s.itemLedger.LookupStash(sess.SessionID, pkt.ActionID); ok {
		s.replyStashResult(sess, cached)
		s.sendWalletUpdate(sess)
		return
	}
	if s.itemLedger.IsStale(sess.SessionID, pkt.ActionID) {
		reject(protocol.ItemResultRejected, 0)
		return
	}
	if !s.itemLedger.AllowAction(sess.SessionID, time.Now()) {
		reject(protocol.ItemResultRejected, 0)
		return
	}

	sess.Lock()
	uuid := sess.AccountID
	level := sess.CurrentLevel
	playerPos := sess.Position
	sess.Unlock()
	if uuid == "" || s.db == nil || s.stashMgr == nil {
		reject(protocol.ItemResultRejected, 0)
		return
	}

	section := nullTermString(pkt.Section[:])
	if !validItemSection(section) || pkt.Count == 0 {
		reject(protocol.ItemResultRejected, 0)
		return
	}

	// Position key: raw client floats rounded onto the 0.5 m world-stash grid.
	keyX := roundWorldStashCoord(clampItemCoord(pkt.X))
	keyY := roundWorldStashCoord(clampItemCoord(pkt.Y))
	keyZ := roundWorldStashCoord(clampItemCoord(pkt.Z))
	keyPos := [3]float32{keyX, keyY, keyZ}

	// Gate before any create: an out-of-reach key must never materialise a
	// stash nobody can use. Existing stashes are re-checked by ValidateAccess.
	if Displacement(keyPos, playerPos) > MaxStashInteractDistance {
		reject(protocol.ItemResultRejected, 0)
		s.audit(uuid, "stash_rejected",
			fmt.Sprintf("action_id=%d action=%d section=%s count=%d err=too far", pkt.ActionID, pkt.Action, section, pkt.Count))
		return
	}

	switch pkt.Action {
	case protocol.StashActionStore:
		rec, err := s.stashMgr.FindOrCreateWorldStash(level, keyX, keyY, keyZ)
		if err != nil {
			reject(protocol.ItemResultRejected, 0)
			s.audit(uuid, "stash_rejected",
				fmt.Sprintf("action_id=%d section=%s count=%d err=%v", pkt.ActionID, section, pkt.Count, err))
			return
		}
		if verr := s.stashMgr.ValidateAccess(rec, playerPos, level, "", uuid); verr != nil {
			reject(protocol.ItemResultRejected, 0)
			s.audit(uuid, "stash_rejected",
				fmt.Sprintf("action_id=%d stash=%d section=%s count=%d err=%v", pkt.ActionID, rec.StashID, section, pkt.Count, verr))
			return
		}
		bucket, err := s.stashMgr.DepositItem(rec.StashID, uuid, level, section, int(pkt.Count), int(pkt.Condition))
		if err != nil {
			if errors.Is(err, database.ErrInsufficientItems) {
				corr := int16(0)
				if avail, aerr := s.db.AvailableItemCount(uuid, section); aerr == nil {
					corr = clampCountToInt16(-avail)
				}
				reject(protocol.ItemResultCorrected, corr)
			} else {
				reject(protocol.ItemResultRejected, 0)
			}
			s.audit(uuid, "stash_rejected",
				fmt.Sprintf("action_id=%d stash=%d section=%s count=%d err=%v", pkt.ActionID, rec.StashID, section, pkt.Count, err))
			return
		}
		res := protocol.StashResultPacket{
			ActionID:  pkt.ActionID,
			Result:    protocol.ItemResultOK,
			Action:    pkt.Action,
			Count:     clampCountToInt16(-int(pkt.Count)),
			Section:   pkt.Section,
			Condition: bucket,
		}
		s.itemLedger.RecordStash(sess.SessionID, pkt.ActionID, res)
		s.replyStashResult(sess, res)
		s.audit(uuid, "stash_store",
			fmt.Sprintf("action_id=%d stash=%d section=%s count=%d", pkt.ActionID, rec.StashID, section, pkt.Count))

	case protocol.StashActionTake:
		rec, err := s.stashMgr.FindWorldStash(level, keyX, keyY, keyZ)
		if err != nil {
			reject(protocol.ItemResultRejected, 0)
			s.audit(uuid, "stash_rejected",
				fmt.Sprintf("action_id=%d section=%s count=%d err=%v", pkt.ActionID, section, pkt.Count, err))
			return
		}
		if verr := s.stashMgr.ValidateAccess(rec, playerPos, level, "", uuid); verr != nil {
			reject(protocol.ItemResultRejected, 0)
			s.audit(uuid, "stash_rejected",
				fmt.Sprintf("action_id=%d stash=%d section=%s count=%d err=%v", pkt.ActionID, rec.StashID, section, pkt.Count, verr))
			return
		}
		bucket, err := s.stashMgr.WithdrawItem(rec.StashID, uuid, level, section, int(pkt.Count), int(pkt.Condition))
		if err != nil {
			reject(protocol.ItemResultRejected, 0)
			s.audit(uuid, "stash_rejected",
				fmt.Sprintf("action_id=%d stash=%d section=%s count=%d err=%v", pkt.ActionID, rec.StashID, section, pkt.Count, err))
			return
		}
		res := protocol.StashResultPacket{
			ActionID:  pkt.ActionID,
			Result:    protocol.ItemResultOK,
			Action:    pkt.Action,
			Count:     clampCountToInt16(int(pkt.Count)),
			Section:   pkt.Section,
			Condition: bucket,
		}
		s.itemLedger.RecordStash(sess.SessionID, pkt.ActionID, res)
		s.replyStashResult(sess, res)
		s.audit(uuid, "stash_take",
			fmt.Sprintf("action_id=%d stash=%d section=%s count=%d", pkt.ActionID, rec.StashID, section, pkt.Count))
	}
}

// replyStashResult sends an OpStashResult reliably to the sender only; stash
// state is never broadcast to other sessions.
func (s *Server) replyStashResult(sess *network.PlayerSession, res protocol.StashResultPacket) {
	s.SendToSession(sess, protocol.OpStashResult, protocol.FlagReliable, res)
}

// handleTradeAction is the OpTradeAction (0x0083) path. The client-asserted
// MoneyDelta is trusted only up to trade_max_money_delta; every accepted or
// rejected action is audited and, on acceptance, the authoritative balance is
// pushed with OpWalletUpdate.
func (s *Server) handleTradeAction(addr *net.UDPAddr, buf *bytes.Reader) {
	var pkt protocol.TradeActionPacket
	if err := binary.Read(buf, binary.LittleEndian, &pkt); err != nil {
		return
	}
	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil || s.itemLedger == nil {
		return
	}

	reject := func(reason string) {
		res := protocol.TradeResultPacket{
			ActionID:   pkt.ActionID,
			Result:     protocol.ItemResultRejected,
			Action:     pkt.Action,
			MoneyDelta: 0,
			Section:    pkt.Section,
			Condition:  pkt.Condition,
		}
		s.itemLedger.RecordTrade(sess.SessionID, pkt.ActionID, res)
		s.replyTradeResult(sess, res)
		s.sendWalletUpdate(sess)
		sess.Lock()
		uuid := sess.AccountID
		sess.Unlock()
		if uuid != "" && reason != "" {
			s.audit(uuid, "trade_rejected",
				fmt.Sprintf("action_id=%d action=%d section=%s count=%d money=%d err=%s",
					pkt.ActionID, pkt.Action, nullTermString(pkt.Section[:]), pkt.Count, pkt.MoneyDelta, reason))
		}
	}

	if pkt.Action != protocol.TradeActionBuy && pkt.Action != protocol.TradeActionSell {
		reject("invalid action")
		return
	}
	if cached, ok := s.itemLedger.LookupTrade(sess.SessionID, pkt.ActionID); ok {
		s.replyTradeResult(sess, cached)
		s.sendWalletUpdate(sess)
		return
	}
	if s.itemLedger.IsStale(sess.SessionID, pkt.ActionID) {
		reject("stale action id")
		return
	}
	if !s.itemLedger.AllowAction(sess.SessionID, time.Now()) {
		reject("rate limited")
		return
	}

	sess.Lock()
	uuid := sess.AccountID
	sess.Unlock()
	if uuid == "" || s.db == nil || s.economy == nil {
		reject("session not ready")
		return
	}

	section := nullTermString(pkt.Section[:])
	if !validItemSection(section) {
		reject("invalid section")
		return
	}
	if pkt.Count == 0 {
		reject("zero count")
		return
	}
	if pkt.MoneyDelta == 0 {
		reject("zero money delta")
		return
	}

	// Cap the client-asserted delta; a clamped action is echoed as Corrected
	// so the client can see the server actually moved less than requested.
	delta := int(pkt.MoneyDelta)
	capped := false
	if maxDelta := s.tradeMaxMoneyDelta(); delta > maxDelta {
		delta = maxDelta
		capped = true
	}

	switch pkt.Action {
	case protocol.TradeActionSell:
		condition, ok, err := s.economy.SellItems(uuid, section, int(pkt.Count), delta)
		if err != nil {
			reject(fmt.Sprintf("sell error: %v", err))
			return
		}
		if !ok {
			reject("item not held")
			return
		}
		result := uint8(protocol.ItemResultOK)
		if capped {
			result = protocol.ItemResultCorrected
		}
		res := protocol.TradeResultPacket{
			ActionID:   pkt.ActionID,
			Result:     result,
			Action:     pkt.Action,
			MoneyDelta: int32(delta),
			Section:    pkt.Section,
			Condition:  condition,
		}
		s.itemLedger.RecordTrade(sess.SessionID, pkt.ActionID, res)
		s.replyTradeResult(sess, res)
		s.sendWalletUpdate(sess)
		s.audit(uuid, "trade_sell",
			fmt.Sprintf("action_id=%d section=%s count=%d money=%d capped=%t", pkt.ActionID, section, pkt.Count, delta, capped))

	case protocol.TradeActionBuy:
		condition := pkt.Condition
		if condition > 100 {
			condition = 100
		}
		ok, err := s.economy.BuyItems(uuid, section, int(pkt.Count), condition, delta)
		if err != nil {
			reject(fmt.Sprintf("buy error: %v", err))
			return
		}
		if !ok {
			reject("insufficient funds")
			return
		}
		result := uint8(protocol.ItemResultOK)
		if capped {
			result = protocol.ItemResultCorrected
		}
		res := protocol.TradeResultPacket{
			ActionID:   pkt.ActionID,
			Result:     result,
			Action:     pkt.Action,
			MoneyDelta: int32(-delta),
			Section:    pkt.Section,
			Condition:  condition,
		}
		s.itemLedger.RecordTrade(sess.SessionID, pkt.ActionID, res)
		s.replyTradeResult(sess, res)
		s.sendWalletUpdate(sess)
		s.audit(uuid, "trade_buy",
			fmt.Sprintf("action_id=%d section=%s count=%d money=%d capped=%t", pkt.ActionID, section, pkt.Count, delta, capped))
	}
}

// replyTradeResult sends an OpTradeResult reliably to the sender only.
func (s *Server) replyTradeResult(sess *network.PlayerSession, res protocol.TradeResultPacket) {
	s.SendToSession(sess, protocol.OpTradeResult, protocol.FlagReliable, res)
}

// sendWalletUpdate pushes the character's absolute authoritative ruble balance
// with OpWalletUpdate (0x0085). Called after every money change and on
// join/character-select so the client's db.actor money reconciles.
func (s *Server) sendWalletUpdate(sess *network.PlayerSession) {
	if sess == nil || s.db == nil || s.economy == nil {
		return
	}
	sess.Lock()
	uuid := sess.AccountID
	sess.Unlock()
	if uuid == "" {
		return
	}
	balance, err := s.economy.GetBalance(uuid)
	if err != nil {
		return
	}
	if balance < 0 {
		balance = 0
	}
	if balance > math.MaxUint32 {
		balance = math.MaxUint32
	}
	s.SendToSession(sess, protocol.OpWalletUpdate, protocol.FlagReliable, protocol.WalletUpdatePacket{Money: uint32(balance)})
}

// tradeMaxMoneyDelta returns the configured per-action ruble cap, defaulting
// to config.DefaultTradeMaxMoneyDelta when unset.
func (s *Server) tradeMaxMoneyDelta() int {
	if s.cfg == nil {
		return config.DefaultTradeMaxMoneyDelta
	}
	return s.cfg.TradeMaxMoneyDeltaOrDefault()
}
