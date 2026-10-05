package game

import (
	"math"
	"sort"
	"sync"
	"time"

	"zone-online/zone-server/internal/ai"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

// AIEntityIDBase is the first wire entity ID handed to AI puppets. Session IDs
// are allocated from a dedicated monotonic counter (server.sessionIDSeq) that
// starts at 1, so AI IDs at 1_000_000+ cannot collide with session IDs for any
// realistic number of player connections. The range also stays clear of the
// packet-sequence counter (server.seq), which is no longer used for session
// identity.
const AIEntityIDBase uint32 = 1_000_000

// aiReplication owns the puppet entity registry and the per-session AoI
// visibility sets for AI entities. It does not insert puppets into the player
// SpatialGrid: AI visibility is computed against the same radius directly, so
// the 30 Hz player snapshot path is untouched.
//
// Wave A scope: patrol-only puppets, no combat, no damage, no AI-vs-player
// logic. State antics/attack/death values exist on the wire but are unused.
type aiReplication struct {
	mu           sync.Mutex
	onlineRadius float32
	squads       []*ai.Squad
	entityIDs    map[uint32]uint32
	visible      map[uint32]map[uint32]bool
	nextID       uint32
}

func newAIReplication(onlineRadius float32) *aiReplication {
	if onlineRadius <= 0 {
		onlineRadius = AoIRadius
	}
	return &aiReplication{
		onlineRadius: onlineRadius,
		entityIDs:    make(map[uint32]uint32),
		visible:      make(map[uint32]map[uint32]bool),
		nextID:       AIEntityIDBase,
	}
}

// register assigns a stable wire entity ID to a puppet squad.
func (a *aiReplication) register(sq *ai.Squad) uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, ok := a.entityIDs[sq.ID]; ok {
		return id
	}
	id := a.nextID
	a.nextID++
	a.squads = append(a.squads, sq)
	a.entityIDs[sq.ID] = id
	return id
}

func (a *aiReplication) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.squads)
}

func (a *aiReplication) entityID(squadID uint32) (uint32, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.entityIDs[squadID]
	return id, ok
}

// markVisible records that sessionID now knows squadID and reports whether the
// transition was new (exactly-once ENTITY_ENTER_AOI).
func (a *aiReplication) markVisible(sessionID, squadID uint32) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.visible[sessionID]
	if set == nil {
		set = make(map[uint32]bool)
		a.visible[sessionID] = set
	}
	if set[squadID] {
		return false
	}
	set[squadID] = true
	return true
}

func (a *aiReplication) clearVisible(sessionID, squadID uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.visible[sessionID]; set != nil {
		delete(set, squadID)
		if len(set) == 0 {
			delete(a.visible, sessionID)
		}
	}
}

func (a *aiReplication) visibleIDs(sessionID uint32) []uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.visible[sessionID]
	if len(set) == 0 {
		return nil
	}
	out := make([]uint32, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// retainSessions drops visibility tracking for sessions that no longer exist.
func (a *aiReplication) retainSessions(live map[uint32]bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id := range a.visible {
		if !live[id] {
			delete(a.visible, id)
		}
	}
}

// registerAIPuppet creates a puppet squad and registers it for replication.
func (s *Server) registerAIPuppet(def ai.PuppetDef) *ai.Squad {
	if s == nil || s.squads == nil || s.aiRepl == nil {
		return nil
	}
	sq := s.squads.RegisterPuppet(def)
	if sq == nil {
		return nil
	}
	s.aiRepl.register(sq)
	return sq
}

// AIEntityIDOf returns the wire entity ID assigned to a puppet squad.
func (s *Server) AIEntityIDOf(squadID uint32) (uint32, bool) {
	if s == nil || s.aiRepl == nil {
		return 0, false
	}
	return s.aiRepl.entityID(squadID)
}

// aiPlayerView is a per-tick copy of session replication state.
type aiPlayerView struct {
	sess  *network.PlayerSession
	id    uint32
	level string
	pos   [3]float32
}

// tickAIReplication is called once per game tick, after the legacy squad tick.
// It tiers puppet simulation (online at tick rate, offline at 1 Hz), maintains
// per-session ENTITY_ENTER/LEAVE_AOI transitions, and streams OpAIState chunks
// (max 32 entries) to every player with at least one online squad in range.
func (s *Server) tickAIReplication(now time.Time) {
	if s == nil || s.aiRepl == nil || s.squads == nil {
		return
	}
	if s.cfg != nil && !s.cfg.AIEnabledOrDefault() {
		return
	}
	if s.aiRepl.count() == 0 {
		return
	}

	views := make([]aiPlayerView, 0, len(s.sessions.GetAll()))
	for _, sess := range s.sessions.GetAll() {
		if sess == nil {
			continue
		}
		sess.Lock()
		v := aiPlayerView{sess: sess, id: sess.SessionID, level: sess.CurrentLevel, pos: sess.Position}
		sess.Unlock()
		views = append(views, v)
	}

	radius := s.aiRepl.onlineRadius
	radiusSq := radius * radius

	// Tier decision uses the positions from the previous step: a squad is
	// ONLINE when any player on its level is inside the radius.
	online := make(map[uint32]bool)
	for _, p := range s.squads.SnapshotPuppets() {
		for i := range views {
			if p.Level == views[i].level && distSq2D(p.Position, views[i].pos) <= radiusSq {
				online[p.ID] = true
				break
			}
		}
	}
	s.squads.TickPuppets(now, online)
	puppets := s.squads.SnapshotPuppets()

	udp := s.GetUDP()
	if udp == nil {
		// No transport (DB-less scale tests): simulation advanced, nothing to
		// send and no visibility bookkeeping to keep.
		return
	}

	live := make(map[uint32]bool, len(views))
	for i := range views {
		live[views[i].id] = true
	}
	s.aiRepl.retainSessions(live)

	type candidate struct {
		state  ai.PuppetState
		distSq float32
	}
	for i := range views {
		v := &views[i]

		cands := make([]candidate, 0, len(puppets))
		inRange := make(map[uint32]bool, len(puppets))
		for _, p := range puppets {
			if p.Level != v.level {
				continue
			}
			d := distSq2D(p.Position, v.pos)
			if d <= radiusSq {
				cands = append(cands, candidate{state: p, distSq: d})
				inRange[p.ID] = true
			}
		}

		// ENTITY_ENTER_AOI exactly once per transition (relies on visibleIDs
		// plus markVisible for duplicate suppression).
		for _, squadID := range s.aiRepl.visibleIDs(v.id) {
			if !inRange[squadID] {
				if entityID, ok := s.aiRepl.entityID(squadID); ok {
					s.aoi.NotifyEntityLeave(v.sess, entityID, udp, &s.seq)
				}
				s.aiRepl.clearVisible(v.id, squadID)
			}
		}
		for _, c := range cands {
			if s.aiRepl.markVisible(v.id, c.state.ID) {
				entityID, ok := s.aiRepl.entityID(c.state.ID)
				if !ok {
					continue
				}
				s.aoi.NotifyEntityEnter(v.sess, aiEntityInfo(c.state, entityID), udp, &s.seq)
			}
		}

		if len(cands) == 0 {
			continue
		}

		// Nearest-first ordering with a squad-ID tie-break keeps chunk output
		// deterministic; every in-range squad is delivered exactly once.
		sort.Slice(cands, func(a, b int) bool {
			if cands[a].distSq != cands[b].distSq {
				return cands[a].distSq < cands[b].distSq
			}
			return cands[a].state.ID < cands[b].state.ID
		})

		for start := 0; start < len(cands); start += protocol.MaxAIStateEntries {
			end := start + protocol.MaxAIStateEntries
			if end > len(cands) {
				end = len(cands)
			}
			var pkt protocol.AIStatePacket
			pkt.Count = uint8(end - start)
			for j, c := range cands[start:end] {
				entityID, ok := s.aiRepl.entityID(c.state.ID)
				if !ok {
					continue
				}
				pkt.Entries[j] = protocol.AIStateEntry{
					EntityID: entityID,
					X:        c.state.Position[0],
					Y:        c.state.Position[1],
					Z:        c.state.Position[2],
					Yaw:      yawToWire(c.state.Yaw),
					Anim:     animToWire(c.state.Anim),
				}
			}
			s.SendToSession(v.sess, protocol.OpAIState, protocol.FlagUnreliable, pkt)
		}
	}
}

// aiEntityInfo builds the ENTITY_ENTER_AOI payload fields for a puppet.
// EntityType 0 marks an AI squad; Health is fixed at 100 in wave A and Gvid 0
// tells the client to resolve the actor section against its own graph.
func aiEntityInfo(p ai.PuppetState, entityID uint32) EntityInfo {
	health := p.Health
	if health <= 0 {
		health = 100
	}
	return EntityInfo{
		ID:      entityID,
		Type:    0,
		Section: p.Section,
		Pos:     p.Position,
		Faction: p.Faction,
		Health:  uint16(health),
		Gvid:    0,
		Name:    p.Label,
	}
}

// yawToWire converts a world yaw in radians to the frozen full-circle u16
// encoding (0..65535, 0 = facing +Z, increasing counter-clockwise).
func yawToWire(yaw float32) uint16 {
	const twoPi = 2 * math.Pi
	y := math.Mod(float64(yaw), twoPi)
	if y < 0 {
		y += twoPi
	}
	return uint16(y / twoPi * 65536.0)
}

// animToWire maps the ai package animation value onto the protocol constant.
func animToWire(anim uint8) uint8 {
	switch anim {
	case ai.AnimIdle:
		return protocol.AIAnimIdle
	case ai.AnimWalk:
		return protocol.AIAnimWalk
	case ai.AnimRun:
		return protocol.AIAnimRun
	case ai.AnimAttack:
		return protocol.AIAnimAttack
	case ai.AnimDeath:
		return protocol.AIAnimDeath
	default:
		return protocol.AIAnimIdle
	}
}

func distSq2D(a, b [3]float32) float32 {
	dx := a[0] - b[0]
	dz := a[2] - b[2]
	return dx*dx + dz*dz
}
