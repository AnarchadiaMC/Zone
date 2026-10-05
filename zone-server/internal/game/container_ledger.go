package game

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// ContainerID mapping: the wire u32 OpContainerAction/OpContainerUpdate
// ContainerID is the world_stashes.stash_id, which is an INTEGER PRIMARY KEY
// (SQLite rowid alias). No secondary mapping table is needed and the existing
// uint32 stash APIs keep working unchanged. Stash ids above uint32 cannot
// exist in practice (AUTOINCREMENT starts at 1 per database file).

// handleContainerAction is the OpContainerAction (0x007F) path. It shares the
// item ledger's ActionID idempotency cache, monotonic replay floor and
// per-session rate limit with OpItemAction, so a retransmitted 0x007F packet
// echoes its cached OpContainerUpdate and never re-applies. Every action runs
// the full ValidateAccess pass (level, distance, owner, passcode), so no
// separate open-stash session state is required.
func (s *Server) handleContainerAction(addr *net.UDPAddr, buf *bytes.Reader) {
	var pkt protocol.ContainerActionPacket
	if err := binary.Read(buf, binary.LittleEndian, &pkt); err != nil {
		return
	}
	sess := s.sessions.GetByAddr(addr.String())
	if sess == nil || s.itemLedger == nil {
		return
	}

	reject := func(result uint8, count int16) {
		update := protocol.ContainerUpdatePacket{
			ActionID:    pkt.ActionID,
			Result:      result,
			Action:      pkt.Action,
			ContainerID: pkt.ContainerID,
			Count:       count,
			Section:     pkt.Section,
			Condition:   pkt.Condition,
		}
		s.itemLedger.RecordContainer(sess.SessionID, pkt.ActionID, update)
		s.replyContainerUpdate(sess, update)
	}

	if pkt.Action != protocol.ContainerActionDeposit && pkt.Action != protocol.ContainerActionWithdraw {
		reject(protocol.ItemResultRejected, 0)
		return
	}

	// Retransmit: echo the cached result without re-applying or re-charging.
	if cached, ok := s.itemLedger.LookupContainer(sess.SessionID, pkt.ActionID); ok {
		s.replyContainerUpdate(sess, cached)
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
	if !validItemSection(section) {
		reject(protocol.ItemResultRejected, 0)
		return
	}
	if pkt.Count == 0 {
		reject(protocol.ItemResultRejected, 0)
		return
	}

	rec, err := s.db.GetStash(pkt.ContainerID)
	if err != nil {
		s.audit(uuid, "item_container_rejected",
			fmt.Sprintf("action_id=%d container=%d section=%s count=%d err=stash not found", pkt.ActionID, pkt.ContainerID, section, pkt.Count))
		reject(protocol.ItemResultRejected, 0)
		return
	}
	// Same level, <= 5 m from the player's last server position, owner match
	// and the fail-closed passcode semantics (the wire carries no passcode, so
	// protected stashes reject).
	if verr := s.stashMgr.ValidateAccess(rec, playerPos, level, "", uuid); verr != nil {
		s.audit(uuid, "item_container_rejected",
			fmt.Sprintf("action_id=%d container=%d section=%s count=%d err=%v", pkt.ActionID, pkt.ContainerID, section, pkt.Count, verr))
		reject(protocol.ItemResultRejected, 0)
		return
	}

	switch pkt.Action {
	case protocol.ContainerActionDeposit:
		bucket, err := s.stashMgr.DepositItem(pkt.ContainerID, uuid, level, section, int(pkt.Count), int(pkt.Condition))
		if err != nil {
			result := protocol.ItemResultRejected
			var corr int16
			if errors.Is(err, database.ErrInsufficientItems) {
				result = protocol.ItemResultCorrected
				if avail, aerr := s.db.AvailableItemCount(uuid, section); aerr == nil {
					corr = clampCountToInt16(-avail)
				}
			}
			update := protocol.ContainerUpdatePacket{
				ActionID:    pkt.ActionID,
				Result:      result,
				Action:      pkt.Action,
				ContainerID: pkt.ContainerID,
				Count:       corr,
				Section:     pkt.Section,
				Condition:   pkt.Condition,
			}
			s.itemLedger.RecordContainer(sess.SessionID, pkt.ActionID, update)
			s.replyContainerUpdate(sess, update)
			// A rejected deposit may have been applied locally by the client;
			// force a full resync so no ghost stack survives.
			s.forceInventorySync(sess)
			s.audit(uuid, "item_deposit_rejected",
				fmt.Sprintf("action_id=%d container=%d section=%s count=%d err=%v", pkt.ActionID, pkt.ContainerID, section, pkt.Count, err))
			return
		}
		update := protocol.ContainerUpdatePacket{
			ActionID:    pkt.ActionID,
			Result:      protocol.ItemResultOK,
			Action:      pkt.Action,
			ContainerID: pkt.ContainerID,
			Count:       clampCountToInt16(-int(pkt.Count)),
			Section:     pkt.Section,
			Condition:   bucket,
		}
		s.itemLedger.RecordContainer(sess.SessionID, pkt.ActionID, update)
		s.replyContainerUpdate(sess, update)
		s.audit(uuid, "item_deposit",
			fmt.Sprintf("action_id=%d container=%d section=%s count=%d", pkt.ActionID, pkt.ContainerID, section, pkt.Count))

	case protocol.ContainerActionWithdraw:
		bucket, err := s.stashMgr.WithdrawItem(pkt.ContainerID, uuid, level, section, int(pkt.Count), int(pkt.Condition))
		if err != nil {
			s.audit(uuid, "item_withdraw_rejected",
				fmt.Sprintf("action_id=%d container=%d section=%s count=%d err=%v", pkt.ActionID, pkt.ContainerID, section, pkt.Count, err))
			reject(protocol.ItemResultRejected, 0)
			return
		}
		update := protocol.ContainerUpdatePacket{
			ActionID:    pkt.ActionID,
			Result:      protocol.ItemResultOK,
			Action:      pkt.Action,
			ContainerID: pkt.ContainerID,
			Count:       clampCountToInt16(int(pkt.Count)),
			Section:     pkt.Section,
			Condition:   bucket,
		}
		s.itemLedger.RecordContainer(sess.SessionID, pkt.ActionID, update)
		s.replyContainerUpdate(sess, update)
		s.audit(uuid, "item_withdraw",
			fmt.Sprintf("action_id=%d container=%d section=%s count=%d", pkt.ActionID, pkt.ContainerID, section, pkt.Count))
	}
}

// replyContainerUpdate sends an OpContainerUpdate reliably to the sender only;
// container state is never broadcast to other sessions.
func (s *Server) replyContainerUpdate(sess *network.PlayerSession, update protocol.ContainerUpdatePacket) {
	s.SendToSession(sess, protocol.OpContainerUpdate, protocol.FlagReliable, update)
}

// handleItemConsume is OpItemAction action=3: remove Count of Section from the
// character inventory inside one transaction, echo OpItemUpdate with a negative
// delta, and let the client apply the local effect (healing, ammo, etc.).
// Insufficient stock is answered with result=2 plus an authoritative inventory
// sync, mirroring the drop correction path.
func (s *Server) handleItemConsume(sess *network.PlayerSession, uuid, section string, pkt protocol.ItemActionPacket) {
	if pkt.Count == 0 {
		update := protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		}
		s.itemLedger.Record(sess.SessionID, pkt.ActionID, update)
		s.replyItemUpdate(sess, update)
		s.forceInventorySync(sess)
		return
	}

	_, err := s.db.ConsumeItem(uuid, section, int(pkt.Count))
	if err != nil {
		update := protocol.ItemUpdatePacket{
			ActionID: pkt.ActionID,
			Result:   protocol.ItemResultRejected,
			Action:   pkt.Action,
			Section:  pkt.Section,
		}
		if errors.Is(err, database.ErrInsufficientItems) {
			update.Result = protocol.ItemResultCorrected
			if avail, aerr := s.db.AvailableItemCount(uuid, section); aerr == nil {
				update.Count = clampCountToInt16(-avail)
			}
		}
		s.itemLedger.Record(sess.SessionID, pkt.ActionID, update)
		s.replyItemUpdate(sess, update)
		// Rejected/corrected consumes are always client-local mutations: resync
		// so the authoritative stack is restored and no ghost effect survives.
		s.forceInventorySync(sess)
		s.audit(uuid, "item_consume_rejected",
			fmt.Sprintf("action_id=%d section=%s count=%d err=%v", pkt.ActionID, section, pkt.Count, err))
		return
	}

	update := protocol.ItemUpdatePacket{
		ActionID:  pkt.ActionID,
		Result:    protocol.ItemResultOK,
		Action:    pkt.Action,
		Count:     clampCountToInt16(-int(pkt.Count)),
		Section:   pkt.Section,
		Condition: pkt.Condition,
	}
	s.itemLedger.Record(sess.SessionID, pkt.ActionID, update)
	s.replyItemUpdate(sess, update)
	s.audit(uuid, "item_consume",
		fmt.Sprintf("action_id=%d section=%s count=%d", pkt.ActionID, section, pkt.Count))
}
