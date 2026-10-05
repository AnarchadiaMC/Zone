package game

import (
	"math"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/ai"
	"zone-online/zone-server/internal/config"
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

// AI replication defaults, used when the config omits the keys.
const (
	defaultAIEnterRadiusM = 180
	defaultAILeaveRadiusM = 220
	defaultAIMaxEntities  = 64

	// aiStateMinInterval caps OpAIState at ~30 Hz regardless of the configured
	// tick rate: at 60/120/240 Hz ticks only every N-th tick actually streams
	// puppet state, while ENTITY_ENTER/LEAVE transitions still process every
	// tick. Decimation is per session via PlayerSession.LastAIStateSent.
	aiStateMinInterval = time.Second / 30
)

// resolveAIRadii returns the configured enter/leave hysteresis radii with
// defaults applied. cfg may be nil (tests).
func resolveAIRadii(cfg *config.Config) (enter, leave float32) {
	enter, leave = defaultAIEnterRadiusM, defaultAILeaveRadiusM
	if cfg != nil {
		if cfg.AIEnterRadiusM > 0 {
			enter = float32(cfg.AIEnterRadiusM)
		}
		if cfg.AILeaveRadiusM > 0 {
			leave = float32(cfg.AILeaveRadiusM)
		}
	}
	if leave < enter {
		leave = enter
	}
	return enter, leave
}

// resolveAIMaxEntities returns the configured puppet cap with the default
// applied. cfg may be nil (tests).
func resolveAIMaxEntities(cfg *config.Config) int {
	if cfg != nil && cfg.AIMaxEntities > 0 {
		return cfg.AIMaxEntities
	}
	return defaultAIMaxEntities
}

// aiReplication owns the puppet entity registry and the per-session AoI
// visibility sets for AI entities. It does not insert puppets into the player
// SpatialGrid: AI visibility is computed against the same radius directly, so
// the 30 Hz player snapshot path is untouched.
//
// Wave A scope: patrol-only puppets, no combat, no damage, no AI-vs-player
// logic. State antics/attack/death values exist on the wire but are unused.
type aiReplication struct {
	mu          sync.Mutex
	enterRadius float32
	leaveRadius float32
	maxEntities int
	logger      *zap.Logger
	squads      []*ai.Squad
	entityIDs   map[uint32]uint32
	visible     map[uint32]map[uint32]bool
	nextID      uint32

	// tickMu serializes tickAIReplication so the scratch buffers below are
	// safely reused by a single caller at a time.
	tickMu sync.Mutex
	// scratch buffers, valid only while tickMu is held.
	scratchViews      []aiPlayerView
	scratchPuppets    []ai.PuppetState
	scratchCands      []aiCandidate
	scratchVisible    []uint32
	scratchSquads     []*ai.Squad
	scratchIDs        map[uint32]uint32
	scratchOnline     map[uint32]bool
	scratchInRange    map[uint32]bool
	scratchVisibleSet map[uint32]bool
}

// aiCandidate is one in-range puppet with its squared distance to the
// receiving player, kept in a reusable scratch slice.
type aiCandidate struct {
	state  ai.PuppetState
	distSq float32
}

func newAIReplication(enterRadius, leaveRadius float32, maxEntities int, logger *zap.Logger) *aiReplication {
	if enterRadius <= 0 {
		enterRadius = defaultAIEnterRadiusM
	}
	if leaveRadius < enterRadius {
		leaveRadius = enterRadius
	}
	if maxEntities <= 0 {
		maxEntities = defaultAIMaxEntities
	}
	return &aiReplication{
		enterRadius: enterRadius,
		leaveRadius: leaveRadius,
		maxEntities: maxEntities,
		logger:      logger,
		entityIDs:   make(map[uint32]uint32),
		visible:     make(map[uint32]map[uint32]bool),
		nextID:      AIEntityIDBase,
	}
}

// register assigns a stable wire entity ID to a puppet squad. It returns false
// (and logs) once the entity cap is reached; the caller must then not
// replicate or simulate that squad.
func (a *aiReplication) register(sq *ai.Squad) bool {
	if sq == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.entityIDs[sq.ID]; ok {
		return true
	}
	if a.maxEntities > 0 && len(a.squads) >= a.maxEntities {
		if a.logger != nil {
			a.logger.Warn("AI entity cap reached; skipping puppet registration",
				zap.Uint32("squad", sq.ID),
				zap.String("label", sq.Label),
				zap.Int("cap", a.maxEntities),
			)
		}
		return false
	}
	id := a.nextID
	a.nextID++
	a.squads = append(a.squads, sq)
	a.entityIDs[sq.ID] = id
	return true
}

// release drops a despawned squad from the registry and from every session's
// visibility set so no ghost entity ID can be re-sent.
func (a *aiReplication) release(squadID uint32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.entityIDs, squadID)
	for i, sq := range a.squads {
		if sq.ID == squadID {
			a.squads = append(a.squads[:i], a.squads[i+1:]...)
			break
		}
	}
	for sessID, set := range a.visible {
		if set[squadID] {
			delete(set, squadID)
			if len(set) == 0 {
				delete(a.visible, sessID)
			}
		}
	}
}

// snapshotLocked copies the squad registry and entity-ID map into the reusable
// scratch buffers under ONE lock acquisition per tick. The returned slices/map
// are only valid until the next snapshot.
func (a *aiReplication) snapshotLocked() ([]*ai.Squad, map[uint32]uint32) {
	a.scratchSquads = append(a.scratchSquads[:0], a.squads...)
	if a.scratchIDs == nil {
		a.scratchIDs = make(map[uint32]uint32, len(a.entityIDs))
	}
	for k := range a.scratchIDs {
		delete(a.scratchIDs, k)
	}
	for k, v := range a.entityIDs {
		a.scratchIDs[k] = v
	}
	return a.scratchSquads, a.scratchIDs
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

// visibleIDsInto copies the session's visible squad IDs into buf (reused by the
// tick path to avoid a per-session slice allocation).
func (a *aiReplication) visibleIDsInto(sessionID uint32, buf []uint32) []uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	buf = buf[:0]
	for id := range a.visible[sessionID] {
		buf = append(buf, id)
	}
	return buf
}

func (a *aiReplication) visibleIDs(sessionID uint32) []uint32 {
	out := a.visibleIDsInto(sessionID, nil)
	if len(out) == 0 {
		return nil
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

// forgetSession drops one session's AI visibility set immediately (called on
// every disconnect/kick/timeout/evict path, not only on the next tick).
func (a *aiReplication) forgetSession(sessionID uint32) {
	a.mu.Lock()
	delete(a.visible, sessionID)
	a.mu.Unlock()
}

// registerAIPuppet creates a puppet squad and registers it for replication.
// Squads beyond the configured entity cap are despawned again and reported as
// nil so they neither simulate nor replicate.
func (s *Server) registerAIPuppet(def ai.PuppetDef) *ai.Squad {
	if s == nil || s.squads == nil || s.aiRepl == nil {
		return nil
	}
	sq := s.squads.RegisterPuppet(def)
	if sq == nil {
		return nil
	}
	if !s.aiRepl.register(sq) {
		s.squads.DespawnSquad(sq.ID)
		return nil
	}
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
	sess        *network.PlayerSession
	id          uint32
	level       string
	pos         [3]float32
	lastAIState time.Time
}

// tickAIReplication is called once per game tick.
// It tiers puppet simulation (online at tick rate, offline at 1 Hz), maintains
// per-session ENTITY_ENTER/LEAVE_AOI transitions with enter/leave hysteresis,
// and streams OpAIState chunks (max 32 entries) at a time-based ~30 Hz ceiling
// regardless of the configured tick rate.
func (s *Server) tickAIReplication(now time.Time) {
	if s == nil || s.aiRepl == nil || s.squads == nil {
		return
	}
	if s.cfg != nil && !s.cfg.AIEnabledOrDefault() {
		return
	}
	a := s.aiRepl
	a.tickMu.Lock()
	defer a.tickMu.Unlock()

	if a.count() == 0 {
		return
	}

	// One session snapshot per tick (lock each session once).
	views := a.scratchViews[:0]
	for _, sess := range s.sessions.GetAll() {
		if sess == nil {
			continue
		}
		sess.Lock()
		v := aiPlayerView{
			sess:        sess,
			id:          sess.SessionID,
			level:       sess.CurrentLevel,
			pos:         sess.Position,
			lastAIState: sess.LastAIStateSent,
		}
		sess.Unlock()
		views = append(views, v)
	}
	a.scratchViews = views

	// One registry snapshot per tick: every entityID lookup below uses the
	// local map instead of re-locking per squad.
	a.mu.Lock()
	_, entityIDs := a.snapshotLocked()
	a.mu.Unlock()

	enterSq := a.enterRadius * a.enterRadius
	leaveSq := a.leaveRadius * a.leaveRadius

	// Tier decision uses the positions from the previous step: a squad is
	// ONLINE when any player on its level is inside the leave radius (the
	// larger radius keeps a squad simulated until it truly leaves).
	online := a.scratchOnline
	if online == nil {
		online = make(map[uint32]bool)
		a.scratchOnline = online
	}
	for k := range online {
		delete(online, k)
	}
	puppets := s.squads.SnapshotPuppetsInto(a.scratchPuppets)
	a.scratchPuppets = puppets
	for _, p := range puppets {
		for i := range views {
			if p.Level == views[i].level && distSq2D(p.Position, views[i].pos) <= leaveSq {
				online[p.ID] = true
				break
			}
		}
	}
	// Dead squads broadcast AnimDeath and are despawned after the delay; the
	// returned IDs are released AFTER the per-view pass below so visible
	// sessions still get their ENTITY_LEAVE_AOI before the entity disappears.
	despawned := s.squads.TickPuppets(now, online)
	puppets = s.squads.SnapshotPuppetsInto(a.scratchPuppets)
	a.scratchPuppets = puppets

	udp := s.GetUDP()
	if udp == nil {
		// No transport (DB-less scale tests): simulation advanced, nothing to
		// send and no visibility bookkeeping to keep.
		for _, squadID := range despawned {
			a.release(squadID)
		}
		return
	}

	live := a.scratchLiveMap()
	for i := range views {
		live[views[i].id] = true
	}
	a.retainSessions(live)

	visibleSet := a.scratchVisibleSet
	if visibleSet == nil {
		visibleSet = make(map[uint32]bool)
		a.scratchVisibleSet = visibleSet
	}

	for i := range views {
		v := &views[i]

		// Hysteresis: an already-visible puppet stays until it crosses the
		// leave radius; a new one only enters inside the enter radius.
		visible := a.visibleIDsInto(v.id, a.scratchVisible)
		a.scratchVisible = visible
		for k := range visibleSet {
			delete(visibleSet, k)
		}
		for _, id := range visible {
			visibleSet[id] = true
		}

		cands := a.scratchCands[:0]
		inRange := a.scratchInRange
		if inRange == nil {
			inRange = make(map[uint32]bool)
			a.scratchInRange = inRange
		}
		for k := range inRange {
			delete(inRange, k)
		}
		for _, p := range puppets {
			if p.Level != v.level {
				continue
			}
			d := distSq2D(p.Position, v.pos)
			if d > leaveSq {
				continue
			}
			if !visibleSet[p.ID] && d > enterSq {
				continue
			}
			cands = append(cands, aiCandidate{state: p, distSq: d})
			inRange[p.ID] = true
		}
		a.scratchCands = cands

		// ENTITY_LEAVE exactly once per visible->miss transition.
		for _, squadID := range visible {
			if inRange[squadID] {
				continue
			}
			if entityID, ok := entityIDs[squadID]; ok {
				s.aoi.NotifyEntityLeave(v.sess, entityID, s)
			}
			a.clearVisible(v.id, squadID)
		}

		// Nearest-first ordering with a squad-ID tie-break keeps chunk output
		// deterministic; every in-range squad is delivered exactly once.
		sort.Slice(cands, func(x, y int) bool {
			if cands[x].distSq != cands[y].distSq {
				return cands[x].distSq < cands[y].distSq
			}
			return cands[x].state.ID < cands[y].state.ID
		})

		// Entity cap: nearest-first wins; visible tail entries get a leave and
		// are dropped from visibility, and the skip is logged.
		if a.maxEntities > 0 && len(cands) > a.maxEntities {
			dropped := 0
			for _, c := range cands[a.maxEntities:] {
				if visibleSet[c.state.ID] {
					if entityID, ok := entityIDs[c.state.ID]; ok {
						s.aoi.NotifyEntityLeave(v.sess, entityID, s)
					}
					a.clearVisible(v.id, c.state.ID)
				}
				dropped++
			}
			if dropped > 0 && a.logger != nil {
				a.logger.Warn("AI entity cap exceeded for session; dropping farthest",
					zap.Uint32("session", v.id),
					zap.Int("cap", a.maxEntities),
					zap.Int("dropped", dropped),
				)
			}
			cands = cands[:a.maxEntities]
			a.scratchCands = cands
		}

		// ENTITY_ENTER exactly once per miss->visible transition.
		for _, c := range cands {
			if a.markVisible(v.id, c.state.ID) {
				entityID, ok := entityIDs[c.state.ID]
				if !ok {
					// Registry changed under us; undo the visibility edge so
					// the next tick can re-evaluate cleanly.
					a.clearVisible(v.id, c.state.ID)
					continue
				}
				s.aoi.NotifyEntityEnter(v.sess, aiEntityInfo(c.state, entityID), s)
			}
		}

		if len(cands) == 0 {
			continue
		}

		// Time-based decimation: stream state at most every aiStateMinInterval
		// per session, independent of the tick rate. Transitions above are
		// never decimated.
		if now.Sub(v.lastAIState) < aiStateMinInterval {
			continue
		}
		for start := 0; start < len(cands); start += protocol.MaxAIStateEntries {
			end := start + protocol.MaxAIStateEntries
			if end > len(cands) {
				end = len(cands)
			}
			var pkt protocol.AIStatePacket
			pkt.Count = uint8(end - start)
			for j, c := range cands[start:end] {
				entityID, ok := entityIDs[c.state.ID]
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
		v.sess.Lock()
		v.sess.LastAIStateSent = now
		v.sess.Unlock()
	}

	// Registry release happens after the per-view pass so any session that
	// still had the despawned squad visible received its leave packet above.
	for _, squadID := range despawned {
		a.release(squadID)
	}
}

// scratchLiveMap returns the reusable live-session set.
func (a *aiReplication) scratchLiveMap() map[uint32]bool {
	if a.scratchOnline == nil {
		a.scratchOnline = make(map[uint32]bool)
	}
	for k := range a.scratchOnline {
		delete(a.scratchOnline, k)
	}
	return a.scratchOnline
}

// aiEntityInfo builds the ENTITY_ENTER_AOI payload fields for a puppet.
// EntityType 0 marks an AI squad; Gvid 0 tells the client to resolve the actor
// section against its own graph. Health is clamped into the wire u16 range,
// with the wave-A default of 100 for unset/zero health.
func aiEntityInfo(p ai.PuppetState, entityID uint32) EntityInfo {
	return EntityInfo{
		ID:      entityID,
		Type:    0,
		Section: p.Section,
		Pos:     p.Position,
		Faction: p.Faction,
		Health:  clampAIHealth(p.Health),
		Gvid:    0,
		Name:    p.Label,
	}
}

// clampAIHealth clamps a puppet health value into the wire u16 range.
func clampAIHealth(h float32) uint16 {
	if h <= 0 {
		return 100
	}
	if h > 65535 {
		return 65535
	}
	return uint16(h)
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
