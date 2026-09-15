package game

import (
	"bytes"
	"sync"
	"sync/atomic"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

const AoIRadius float32 = 220.0

type EntityInfo struct {
	ID      uint32
	Type    uint8
	Section string
	Pos     [3]float32
	Faction uint8
	Health  uint16
}

type AoIManager struct {
	mu      sync.Mutex
	entries map[uint32]map[uint32]bool
}

func NewAoIManager() *AoIManager {
	return &AoIManager{
		entries: make(map[uint32]map[uint32]bool),
	}
}

func (a *AoIManager) BroadcastSnapshots(sessions *network.SessionManager, grid *SpatialGrid, udp *network.UDPListener, seq *atomic.Uint32) {
	allSessions := sessions.GetAll()
	for _, sess := range allSessions {
		sess.Lock()
		x, z := sess.Position[0], sess.Position[2]
		sessID := sess.SessionID
		udpAddr := sess.UDPAddr
		sess.Unlock()

		if udpAddr == nil {
			continue
		}

		neighbors := grid.GetNeighbors(x, z, AoIRadius)

		var snapshot protocol.ServerSnapshot
		count := 0

		for _, nID := range neighbors {
			if nID == sessID {
				continue
			}
			// PRODUCTION FIX: Entries is [32]; the old >=64 cap panicked on
			// index 32..63 with >32 players in radius. Cap at wire capacity.
			if count >= len(snapshot.Entries) {
				break
			}

			peer := sessions.GetByID(nID)
			if peer == nil {
				continue
			}

			peer.Lock()
			snapshot.Entries[count] = protocol.SnapshotEntry{
				SessionID: peer.SessionID,
				PosX:      peer.Position[0],
				PosY:      peer.Position[1],
				PosZ:      peer.Position[2],
				Yaw:       int16(peer.Rotation[0] * 100.0),
				Pitch:     int16(peer.Rotation[1] * 100.0),
				AnimFlags: peer.AnimFlags,
				Health:    uint8(peer.Health),
			}
			peer.Unlock()
			count++
		}

		if count > 0 {
			snapshot.Count = uint8(count)
			var buf bytes.Buffer
			packetSeq := seq.Add(1)
			err := protocol.WritePacket(&buf, protocol.OpServerSnapshot, packetSeq, protocol.FlagUnreliable, &snapshot)
			if err == nil {
				_ = udp.Send(udpAddr, buf.Bytes())
			}
		}
	}
}

func (a *AoIManager) NotifyEntityEnter(session *network.PlayerSession, info EntityInfo, udp *network.UDPListener, seq *atomic.Uint32) {
	if session == nil {
		return
	}
	session.Lock()
	sessID := session.SessionID
	udpAddr := session.UDPAddr
	session.Unlock()

	if udpAddr == nil {
		return
	}

	a.mu.Lock()
	if _, ok := a.entries[sessID]; !ok {
		a.entries[sessID] = make(map[uint32]bool)
	}
	if a.entries[sessID][info.ID] {
		a.mu.Unlock()
		return
	}
	a.entries[sessID][info.ID] = true
	a.mu.Unlock()

	var sec [32]byte
	copy(sec[:], info.Section)
	var fac [16]byte
	fac[0] = info.Faction

	pkt := protocol.EntityEnterAoI{
		EntityID:   info.ID,
		EntityType: info.Type,
		Section:    sec,
		PosX:       info.Pos[0],
		PosY:       info.Pos[1],
		PosZ:       info.Pos[2],
		Faction:    fac,
		Health:     uint8(info.Health),
	}

	var buf bytes.Buffer
	seqID := seq.Add(1)
	if err := protocol.WritePacket(&buf, protocol.OpEntityEnterAoI, seqID, protocol.FlagReliable, pkt); err == nil {
		_ = udp.Send(udpAddr, buf.Bytes())
	}
}

func (a *AoIManager) NotifyEntityLeave(session *network.PlayerSession, entityID uint32, udp *network.UDPListener, seq *atomic.Uint32) {
	if session == nil {
		return
	}
	session.Lock()
	sessID := session.SessionID
	udpAddr := session.UDPAddr
	session.Unlock()

	if udpAddr == nil {
		return
	}

	a.mu.Lock()
	if set, ok := a.entries[sessID]; ok {
		delete(set, entityID)
	}
	a.mu.Unlock()

	var buf bytes.Buffer
	pkt := protocol.EntityLeaveAoI{EntityID: entityID}
	seqID := seq.Add(1)
	if err := protocol.WritePacket(&buf, protocol.OpEntityLeaveAoI, seqID, protocol.FlagReliable, pkt); err == nil {
		_ = udp.Send(udpAddr, buf.Bytes())
	}
}

func (a *AoIManager) RemoveSession(sessID uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.entries, sessID)
	for _, set := range a.entries {
		delete(set, sessID)
	}
}

