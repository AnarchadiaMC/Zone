package game

import (
	"bytes"
	"net"
	"slices"
	"sync"
	"sync/atomic"

	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

const AoIRadius float32 = 220.0

// MaxSnapshotEntriesPerClientPerTick optionally caps how many AoI entries one
// client may receive per tick. 0 means unlimited (default), which is safe for
// the supported player caps.
//
// Worst-case bandwidth at 48 players all inside one AoI (47 neighbours):
//   entries per tick   = 47
//   packets per tick   = ceil(47 / 32) = 2
//   bytes per tick     = 47*22 (entries) + 2*1 (counts) + 2*12 (headers) = 1060
//   bytes per second   = 1060 * 30 = 31,800 B/s ~= 31.1 KiB/s
//
// That is well under a 100 KiB/s budget, so no cap is applied by default.
// Raising MaxPlayers past ~150 in a single AoI would warrant setting this to
// ~150 entries (~103 KiB/s with headers) and relying on the nearest-first
// ordering below. Keep it a compile-time constant so the hot path stays
// allocation-free; wire it into config if a deployment ever needs tuning.
const MaxSnapshotEntriesPerClientPerTick = 0

type EntityInfo struct {
	ID      uint32
	Type    uint8
	Section string
	Pos     [3]float32
	Faction string
	Health  uint16
	Gvid    uint16
	Name    string
}

// SnapshotSender is the minimal send surface the AoI broadcaster needs.
// *network.UDPListener satisfies it; tests substitute a capturing sender.
type SnapshotSender interface {
	Send(addr *net.UDPAddr, data []byte) error
}

// neighborRef is one AoI candidate with its payload pre-copied under the peer
// session's lock and its squared distance to the receiver for nearest-first
// ordering.
type neighborRef struct {
	id        uint32
	distSq    float32
	x, y, z   float32
	yaw       int16
	pitch     int16
	animFlags uint8
	health    uint8
}

// aoiScratch holds the per-broadcast reusable buffers. One scratch is checked
// out of the pool per BroadcastSnapshots call (per tick), so snapshot
// generation performs no per-client heap allocation after warmup.
type aoiScratch struct {
	ids  []uint32
	refs []neighborRef
	buf  bytes.Buffer
}

var aoiScratchPool = sync.Pool{
	New: func() interface{} {
		s := &aoiScratch{
			ids:  make([]uint32, 0, 64),
			refs: make([]neighborRef, 0, 64),
		}
		s.buf.Grow(protocol.MaxSafeUDPPacketSize)
		return s
	},
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

// BroadcastSnapshots sends each connected session a nearest-first snapshot of
// its AoI neighbours. When a client has more than MaxSnapshotEntries
// neighbours, the set is split across several OpServerSnapshot packets of at
// most MaxSnapshotEntries entries each so no peer is silently dropped; the Lua
// client parses every packet independently (zone_dummy.script on_snapshot).
// Session locks are held only while copying peer fields (and are never held
// across a socket send).
func (a *AoIManager) BroadcastSnapshots(sessions *network.SessionManager, grid *SpatialGrid, udp SnapshotSender, seq *atomic.Uint32) {
	if udp == nil {
		return
	}
	allSessions := sessions.GetAll()

	scratch := aoiScratchPool.Get().(*aoiScratch)
	defer func() {
		scratch.ids = scratch.ids[:0]
		scratch.refs = scratch.refs[:0]
		scratch.buf.Reset()
		aoiScratchPool.Put(scratch)
	}()
	scratch.buf.Reset()

	var snap protocol.ServerSnapshot
	for _, sess := range allSessions {
		sess.Lock()
		x, z := sess.Position[0], sess.Position[2]
		sessID := sess.SessionID
		udpAddr := sess.UDPAddr
		sess.Unlock()

		if udpAddr == nil {
			continue
		}

		scratch.ids = grid.GetNeighborsInto(scratch.ids[:0], x, z, AoIRadius)
		refs := scratch.refs[:0]

		for _, nID := range scratch.ids {
			if nID == sessID {
				continue
			}
			peer := sessions.GetByID(nID)
			if peer == nil {
				continue
			}

			peer.Lock()
			px, py, pz := peer.Position[0], peer.Position[1], peer.Position[2]
			ref := neighborRef{
				id:        peer.SessionID,
				x:         px,
				y:         py,
				z:         pz,
				yaw:       int16(peer.Rotation[0] * 100.0),
				pitch:     int16(peer.Rotation[1] * 100.0),
				animFlags: peer.AnimFlags,
				health:    uint8(peer.Health),
			}
			peer.Unlock()
			dx, dz := px-x, pz-z
			ref.distSq = dx*dx + dz*dz
			refs = append(refs, ref)
		}

		// Nearest-first ordering; the session-ID tie-break keeps output
		// deterministic when distances match.
		slices.SortFunc(refs, func(a, b neighborRef) int {
			switch {
			case a.distSq < b.distSq:
				return -1
			case a.distSq > b.distSq:
				return 1
			case a.id < b.id:
				return -1
			case a.id > b.id:
				return 1
			default:
				return 0
			}
		})

		if MaxSnapshotEntriesPerClientPerTick > 0 && len(refs) > MaxSnapshotEntriesPerClientPerTick {
			refs = refs[:MaxSnapshotEntriesPerClientPerTick]
		}
		scratch.refs = refs

		for start := 0; start < len(refs); start += protocol.MaxSnapshotEntries {
			end := start + protocol.MaxSnapshotEntries
			if end > len(refs) {
				end = len(refs)
			}
			chunk := refs[start:end]
			snap.Count = uint8(len(chunk))
			for i := range chunk {
				r := &chunk[i]
				snap.Entries[i] = protocol.SnapshotEntry{
					SessionID: r.id,
					PosX:      r.x,
					PosY:      r.y,
					PosZ:      r.z,
					Yaw:       r.yaw,
					Pitch:     r.pitch,
					AnimFlags: r.animFlags,
					Health:    r.health,
				}
			}

			scratch.buf.Reset()
			packetSeq := seq.Add(1)
			if err := protocol.WritePacket(&scratch.buf, protocol.OpServerSnapshot, packetSeq, protocol.FlagUnreliable, &snap); err != nil {
				continue
			}
			_ = udp.Send(udpAddr, scratch.buf.Bytes())
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

	var sec [64]byte
	copyNulTerm(sec[:], info.Section)
	var fac [16]byte
	copyNulTerm(fac[:], info.Faction)
	var name [32]byte
	copyNulTerm(name[:], info.Name)

	pkt := protocol.EntityEnterAoI{
		EntityID:   info.ID,
		EntityType: info.Type,
		Section:    sec,
		PosX:       info.Pos[0],
		PosY:       info.Pos[1],
		PosZ:       info.Pos[2],
		Faction:    fac,
		Health:     uint8(info.Health),
		Gvid:       info.Gvid,
		Name:       name,
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
