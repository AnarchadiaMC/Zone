package game

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"
	"zone-online/zone-server/internal/ai"
	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
)

type PacketRecorder interface {
	Record(data []byte)
}

var activeSink PacketRecorder

// LeaveAoI carries entity leave notification payload.
type LeaveAoI struct {
	SessionID uint32
}

const (
	OpLeave    = protocol.OpEntityLeaveAoI
	OpAiAction = protocol.OpAIActionEvent
	OpAck      = 0x0005
)

func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

type EventManager struct{}

func NewEventManager() *EventManager { return &EventManager{} }

type Server struct {
	cfg         *config.Config
	db          *database.DB
	dbQueue     chan *database.DBWriteJob
	udp         *network.UDPListener
	udpMu       sync.RWMutex
	sessions    *network.SessionManager
	grid        *SpatialGrid
	events      *EventManager
	economy     *EconomyManager
	stashMgr    *StashManager
	anticheat   *AntiCheatManager
	aoi         *AoIManager
	squads      *ai.SquadManager
	logger      *zap.Logger
	seq           atomic.Uint32
	ackQueue      *network.AckQueue
	emissionMgr   *EmissionOrchestrator
	damageHandler *DamageHandler
	sleepers      *SleeperManager
	startTime     time.Time
	tickCount     atomic.Uint64
}

func NewServer(cfg *config.Config, db *database.DB, logger *zap.Logger, dbQueue ...chan *database.DBWriteJob) *Server {
	var q chan *database.DBWriteJob
	if len(dbQueue) > 0 {
		q = dbQueue[0]
	}
	s := &Server{
		cfg:           cfg,
		db:            db,
		dbQueue:       q,
		sessions:      network.NewSessionManager(),
		grid:          NewSpatialGrid(64.0),
		events:        NewEventManager(),
		economy:       NewEconomyManager(db),
		stashMgr:      NewStashManager(db),
		anticheat:     NewAntiCheatManager(),
		aoi:           NewAoIManager(),
		squads:        ai.NewSquadManager(),
		logger:        logger,
		ackQueue:      network.NewAckQueue(),
		emissionMgr:   NewEmissionOrchestrator(),
		damageHandler: NewDamageHandler(logger),
		sleepers:      NewSleeperManager(logger),
		startTime:     time.Now(),
	}

	s.ackQueue.SetSendFunc(func(addr *net.UDPAddr, data []byte) error {
		if activeSink != nil {
			activeSink.Record(data)
		}
		if u := s.GetUDP(); u != nil {
			return u.Send(addr, data)
		}
		return nil
	})
	s.ackQueue.SetOnDrop(func(seq uint32, addr *net.UDPAddr) {
		s.logger.Warn("Reliable packet dropped after max retries",
			zap.Uint32("seq", seq),
			zap.String("addr", addr.String()),
		)
	})

	if cfg != nil && cfg.EmissionIntervalMin > 0 {
		s.emissionMgr.SetDormantDuration(time.Duration(cfg.EmissionIntervalMin) * time.Minute)
	}
	return s
}

// DamageHandler returns the active DamageHandler instance.
func (s *Server) DamageHandler() *DamageHandler {
	return s.damageHandler
}

// Sleepers returns the active SleeperManager instance.
func (s *Server) Sleepers() *SleeperManager {
	return s.sleepers
}

// EmissionMgr returns the active EmissionOrchestrator instance.
func (s *Server) EmissionMgr() *EmissionOrchestrator {
	return s.emissionMgr
}

// Squads returns the active SquadManager instance.
func (s *Server) Squads() *ai.SquadManager {
	return s.squads
}

// AckQueue returns the active AckQueue instance.
func (s *Server) AckQueue() *network.AckQueue {
	return s.ackQueue
}


// GetUDP safely retrieves the active UDPListener.
func (s *Server) GetUDP() *network.UDPListener {
	s.udpMu.RLock()
	defer s.udpMu.RUnlock()
	return s.udp
}

// SetUDP safely sets the UDPListener.
func (s *Server) SetUDP(u *network.UDPListener) {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	s.udp = u
}

// InitUDP initializes the UDP listener for the server (S-04).
func (s *Server) InitUDP() error {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	if s.udp != nil {
		return nil
	}
	port := 0
	if s.cfg != nil {
		port = s.cfg.Port
	}
	udp, err := network.NewUDPListener(port, s.HandlePacket)
	if err != nil {
		return err
	}
	s.udp = udp
	return nil
}

func (s *Server) Run(ctx context.Context) error {
	if s.GetUDP() == nil {
		if err := s.InitUDP(); err != nil {
			return err
		}
	}
	udp := s.GetUDP()
	udp.Start(ctx)

	s.logger.Info("Server started")
	<-ctx.Done()
	return udp.Close()
}

// isValidTransform validates client coordinates and velocities against NaN, Inf, and plausible bounds (S-09).
func isValidTransform(ct *protocol.ClientTransform) bool {
	coords := [3]float64{float64(ct.PosX), float64(ct.PosY), float64(ct.PosZ)}
	for _, c := range coords {
		if math.IsNaN(c) || math.IsInf(c, 0) || math.Abs(c) > 10000.0 {
			return false
		}
	}
	vels := [3]float64{float64(ct.VelX) / 100.0, float64(ct.VelY) / 100.0, float64(ct.VelZ) / 100.0}
	for _, v := range vels {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1000.0 {
			return false
		}
	}
	return true
}

// sanitizeChatText strips non-printable ASCII/control runes except newline and tab, ensuring valid UTF-8.
func sanitizeChatText(raw string) string {
	if !utf8.ValidString(raw) {
		raw = strings.ToValidUTF8(raw, "")
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if r == '\n' || r == '\t' {
			b.WriteRune(r)
		} else if unicode.IsPrint(r) && !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Server) HandlePacket(data []byte, addr *net.UDPAddr) {
	if len(data) < 12 {
		return
	}
	buf := bytes.NewReader(data)
	hdr, err := protocol.ReadHeader(buf)
	if err != nil || hdr.Magic != protocol.HeaderMagic {
		return
	}

	// PRODUCTION FIX: immediately ACK every reliable inbound packet (except
	// ACKs themselves). Covers handshake/chat/disconnect explicit cases, not
	// just the default branch. Stops client retransmit storms.
	if hdr.Opcode != protocol.OpAck && hdr.FlagsChannel&protocol.FlagReliable != 0 {
		s.sendAck(addr, hdr.SequenceNum)
		if s.ackQueue != nil {
			s.ackQueue.Acknowledge(hdr.SequenceNum)
		}
	}

	switch hdr.Opcode {
	case protocol.OpHandshakeReq:
		var req protocol.HandshakeReq
		if err := binary.Read(buf, binary.LittleEndian, &req); err == nil {
			s.logger.Info("Handshake", zap.String("addr", addr.String()))
			// PRODUCTION FIX: gate protocol version (client sends protoVer=1).
			// Previously any version (e.g. test value 89) was accepted.
			if req.ProtocolVer != protocol.ProtocolVer {
				s.logger.Warn("Handshake version mismatch",
					zap.String("addr", addr.String()),
					zap.Uint8("got", req.ProtocolVer),
					zap.Uint8("want", protocol.ProtocolVer))
				res := protocol.HandshakeRes{
					Status:    3, // 3 = version mismatch
					WorldTime: uint64(time.Now().Unix()),
				}
				tempSess := &network.PlayerSession{UDPAddr: addr}
				s.SendToSession(tempSess, protocol.OpHandshakeRes, protocol.FlagReliable, res)
				return
			}
			cleanUUID := nullTermString(req.UUID[:])
			if cleanUUID == "" {
				cleanUUID = string(bytes.Trim(req.UUID[:], "\x00"))
			}

			// Ban Check (Issue 11)
			if s.db != nil {
				if banned, reason, err := CheckPlayerBanned(s.db, cleanUUID); err == nil && banned {
					s.logger.Warn("Banned player rejected", zap.String("uuid", cleanUUID), zap.String("reason", reason))
					res := protocol.HandshakeRes{
						Status:    2, // 2 = Banned
						WorldTime: uint64(time.Now().Unix()),
					}
					tempSess := &network.PlayerSession{UDPAddr: addr}
					s.SendToSession(tempSess, protocol.OpHandshakeRes, protocol.FlagReliable, res)
					return
				}
			}

			if s.cfg != nil && s.cfg.MaxPlayers > 0 && len(s.sessions.GetAll()) >= s.cfg.MaxPlayers {
				s.logger.Warn("Server full, rejecting handshake", zap.String("addr", addr.String()))
				res := protocol.HandshakeRes{
					Status:    1,
					WorldTime: uint64(time.Now().Unix()),
				}
				tempSess := &network.PlayerSession{UDPAddr: addr}
				s.SendToSession(tempSess, protocol.OpHandshakeRes, protocol.FlagReliable, res)
				return
			}

			sessID := s.seq.Add(1)
			token, _ := GenerateSessionToken()
			sess := &network.PlayerSession{
				SessionID:      sessID,
				SessionToken:   token,
				AccountID:      cleanUUID,
				UDPAddr:        addr,
				LastSeen:       time.Now(),
				LastCheckpoint: time.Now(),
				Health:         100.0,
				CurrentLevel:   "l01_escape",
			}
			if s.db != nil {
				nick := nullTermString(req.Nickname[:])
				if nick == "" {
					nick = "Stalker"
				}
				hwid := fmt.Sprintf("%x", req.HWIDHash)
				_ = s.db.AutoProvision(cleanUUID, hwid, nick)
				if char, err := s.db.LoadCharacter(cleanUUID); err == nil && char != nil {
					if char.LevelName != "" {
						sess.CurrentLevel = char.LevelName
					}
					sess.Position = [3]float32{char.PosX, char.PosY, char.PosZ}
					sess.Health = char.Health
				}
			}

			// Reclaim active sleeper if reconnecting (Issue 16)
			if s.sleepers != nil {
				if sleeper := s.sleepers.GetSleeperByAccount(cleanUUID); sleeper != nil {
					sleeper.RLock()
					sess.Position = sleeper.Position
					sess.Health = sleeper.Health
					sess.CurrentLevel = sleeper.CurrentLevel
					sleeperID := sleeper.EntityID
					sleeper.RUnlock()
					s.sleepers.RemoveSleeper(sleeperID)
					s.grid.Remove(sleeperID)
				}
			}

			s.sessions.AddSession(sess)

			res := protocol.HandshakeRes{
				Status:    0,
				SpawnX:    sess.Position[0],
				SpawnY:    sess.Position[1],
				SpawnZ:    sess.Position[2],
				WorldTime: uint64(time.Now().Unix()),
				EcoTier:   1,
			}
			binary.LittleEndian.PutUint32(res.SessionID[:], sessID)

			s.grid.Insert(sessID, sess.Position[0], sess.Position[2])
			szObj := CheckSafeZone(sess.Position[0], sess.Position[1], sess.Position[2], sess.CurrentLevel)
			inSZ := (szObj != nil)
			sess.InSafeZone = inSZ
			if inSZ {
				sess.SafeZoneID = szObj.ZoneID
			}

			s.SendToSession(sess, protocol.OpHandshakeRes, protocol.FlagReliable, res)
		}
	case protocol.OpDisconnect:
		if sess := s.sessions.GetByAddr(addr.String()); sess != nil {
			sess.Lock()
			sessID := sess.SessionID
			uuid := sess.AccountID
			pos := sess.Position
			yaw := sess.Rotation[0]
			health := sess.Health
			level := sess.CurrentLevel
			inSafe := sess.InSafeZone
			inCombat := time.Now().Before(sess.InCombatUntil)
			sess.Unlock()

			if uuid != "" {
				s.QueuePlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, health)
			}
			s.grid.Remove(sessID)
			s.aoi.RemoveSession(sessID)

			// Sleeper System (Issue 16, §17.1):
			// If outside safe zone or in combat, spawn a sleeper proxy
			if (!inSafe || inCombat) && s.sleepers != nil && health > 0 {
				sleeper := s.sleepers.CreateSleeper(sess, 30*time.Second)
				if sleeper != nil {
					s.grid.Insert(sleeper.EntityID, sleeper.Position[0], sleeper.Position[2])
					s.logger.Info("Spawned sleeper proxy for disconnected player",
						zap.String("uuid", uuid),
						zap.Uint32("entity_id", sleeper.EntityID),
					)
				}
			}

			leavePkt := protocol.EntityLeaveAoI{EntityID: sessID}
			for _, other := range s.sessions.GetAll() {
				if other.SessionID == sessID {
					continue
				}
				other.Lock()
				sameLevel := (other.CurrentLevel == level)
				other.Unlock()
				if sameLevel {
					s.SendToSession(other, protocol.OpEntityLeaveAoI, protocol.FlagReliable, leavePkt)
				}
			}

			s.sessions.RemoveSession(sessID)
			s.logger.Info("Client disconnected",
				zap.Uint32("session_id", sessID),
				zap.String("addr", addr.String()),
			)
		}
	case protocol.OpHeartbeat:
		var hb protocol.HeartbeatPayload
		if err := binary.Read(buf, binary.LittleEndian, &hb); err == nil {
			if sess := s.sessions.GetByAddr(addr.String()); sess != nil {
				sess.Lock()
				sess.LastSeen = time.Now()
				sess.Unlock()
				s.SendToSession(sess, protocol.OpHeartbeat, protocol.FlagUnreliable, hb)
			}
			// S-08: Drop heartbeat from unknown/unregistered session to prevent UDP amplification/reflection attacks.
		} else {
			if sess := s.sessions.GetByAddr(addr.String()); sess != nil {
				sess.Lock()
				sess.LastSeen = time.Now()
				sess.Unlock()
			}
		}
	case protocol.OpClientTransform:
		var ct protocol.ClientTransform
		if err := binary.Read(buf, binary.LittleEndian, &ct); err == nil {
			// S-09: Validate transform data bounds and NaN/Inf
			if !isValidTransform(&ct) {
				s.logger.Warn("Dropped invalid client transform",
					zap.String("addr", addr.String()),
					zap.Float32("x", ct.PosX),
					zap.Float32("y", ct.PosY),
					zap.Float32("z", ct.PosZ),
				)
				return
			}
			if sess := s.sessions.GetByAddr(addr.String()); sess != nil {
				// Authenticate session ID if present (Issue 14)
				reqSessID := binary.LittleEndian.Uint32(ct.SessionID[:])
				if reqSessID != 0 && !AuthenticateSessionPacket(sess, reqSessID, 0) {
					s.logger.Warn("Session ID mismatch in ClientTransform",
						zap.Uint32("expected", sess.SessionID),
						zap.Uint32("got", reqSessID),
					)
					return
				}

				now := time.Now()
				sess.Lock()
				dt := float32(now.Sub(sess.LastTransformTime).Seconds())
				sess.Unlock()

				// Speedhack and movement sanity check (Issue 17)
				if s.anticheat != nil {
					newPos := [3]float32{ct.PosX, ct.PosY, ct.PosZ}
					valid, reason := s.anticheat.ValidateMove(sess, newPos, dt)
					if !valid {
						violations := s.anticheat.RecordViolation(sess.SessionID, reason)
						if s.anticheat.ShouldKick(violations) {
							s.logger.Warn("Kicking player for repeated anticheat violations",
								zap.Uint32("session", sess.SessionID),
								zap.String("reason", reason),
								zap.Int("violations", violations),
							)
							s.KickSession(sess.SessionID)
							return
						}
						// Rubberband: drop packet and do not update position
						return
					}
				}

				sess.Lock()
				sessID := sess.SessionID
				sess.Position = [3]float32{ct.PosX, ct.PosY, ct.PosZ}
				sess.Rotation = [2]float32{float32(ct.Yaw) / 100.0, float32(ct.Pitch) / 100.0}
				sess.Velocity = [3]float32{float32(ct.VelX) / 100.0, float32(ct.VelY) / 100.0, float32(ct.VelZ) / 100.0}
				sess.AnimFlags = ct.AnimFlags
				sess.LastSeen = now
				sess.LastTransformTime = now
				uuid := sess.AccountID
				health := sess.Health
				level := sess.CurrentLevel
				prevSafe := sess.InSafeZone
				sess.Dirty = true
				sess.Unlock()

				s.grid.Update(sessID, ct.PosX, ct.PosZ)

				szObj := CheckSafeZone(ct.PosX, ct.PosY, ct.PosZ, level)
				isSafe := (szObj != nil)
				if isSafe != prevSafe {
					sess.Lock()
					sess.InSafeZone = isSafe
					var zoneIDStr string
					if isSafe {
						zoneIDStr = szObj.ZoneID
					}
					sess.SafeZoneID = zoneIDStr
					sess.Unlock()

					payload := protocol.SafezoneStatePayload{
						Locked: boolToUint8(isSafe),
					}
					copy(payload.ZoneID[:], zoneIDStr)
					s.SendToSession(sess, protocol.OpSafezoneState, protocol.FlagReliable, payload)

					// Safe zone entry/exit flushes character transform immediately (Issue 19)
					if uuid != "" {
						s.QueuePlayerTransform(uuid, ct.PosX, ct.PosY, ct.PosZ, float32(ct.Yaw)/100.0, health)
						sess.Lock()
						sess.LastCheckpoint = now
						sess.Dirty = false
						sess.Unlock()
					}
				}
			}
		}
	case protocol.OpChatText:
		var pkt protocol.ChatText
		if err := binary.Read(buf, binary.LittleEndian, &pkt); err != nil {
			return
		}
		sess := s.sessions.GetByAddr(addr.String())
		if sess == nil || (pkt.SenderID != 0 && !AuthenticateSessionPacket(sess, pkt.SenderID, 0)) {
			return
		}
		// Extract text up to the declared length, capped at 255.
		textLen := int(pkt.Len)
		if textLen > 255 {
			textLen = 255
		}
		raw := string(pkt.Text[:textLen])

		// Sanitize: strip non-printable runes and ensure valid UTF-8.
		sanitized := sanitizeChatText(raw)

		s.logger.Info("chat",
			zap.Uint32("sender_session", pkt.SenderID),
			zap.String("text", sanitized),
		)
		s.BroadcastChat(pkt.SenderID, sanitized)
	case protocol.OpDamageNotify:
		var dmg protocol.DamageNotify
		if err := binary.Read(buf, binary.LittleEndian, &dmg); err != nil {
			return
		}
		attackerSess := s.sessions.GetByAddr(addr.String())
		if attackerSess == nil {
			return
		}

		// Check if target is a sleeper proxy (Issue 16)
		if s.sleepers != nil {
			if sleeper := s.sleepers.GetSleeper(dmg.TargetID); sleeper != nil {
				remHealth, isDead := s.sleepers.ApplyDamage(dmg.TargetID, dmg.Damage)
				s.logger.Info("Damage applied to sleeper",
					zap.Uint32("sleeper", dmg.TargetID),
					zap.Float32("damage", dmg.Damage),
					zap.Float32("rem_health", remHealth),
					zap.Bool("dead", isDead),
				)
				if isDead {
					sleeper.RLock()
					uuid := sleeper.AccountID
					pos := sleeper.Position
					yaw := sleeper.Rotation[0]
					sleeper.RUnlock()
					if uuid != "" {
						s.QueuePlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, 0.0)
					}
					s.sleepers.RemoveSleeper(dmg.TargetID)
					s.grid.Remove(dmg.TargetID)
				}
				return
			}
		}

		targetSess := s.sessions.GetByID(dmg.TargetID)
		if targetSess == nil {
			return
		}

		if s.damageHandler != nil {
			applied, valid, reason := s.damageHandler.ValidateAndApplyDamage(attackerSess, targetSess, &dmg)
			if !valid {
				s.logger.Debug("Damage rejected", zap.String("reason", reason))
				return
			}
			dmgOut := dmg
			dmgOut.Damage = applied
			s.SendToSession(targetSess, protocol.OpDamageNotify, protocol.FlagReliable, dmgOut)
		}
	case protocol.OpStashInteract:
		var pkt protocol.StashInteractPayload
		if err := binary.Read(buf, binary.LittleEndian, &pkt); err != nil {
			s.logger.Warn("OpStashInteract: failed to parse payload", zap.Error(err))
			return
		}

		sess := s.sessions.GetByAddr(addr.String())
		var sessionID uint32
		if sess != nil {
			sess.Lock()
			sessionID = sess.SessionID
			sess.Unlock()
		}

		stashID := pkt.StashID
		section := nullTermString(pkt.ItemSection[:])
		count := int(pkt.Count)

		var status uint8
		var contents []byte

		switch pkt.Action {
		case 1: // Open
			s.logger.Info("Stash opened", zap.Uint32("stash_id", stashID), zap.Uint32("session_id", sessionID))
			var err error
			contents, err = s.stashMgr.OpenStash(stashID, sessionID)
			if err != nil {
				if err == ErrStashNotFound {
					status = 1
				} else {
					status = 2
				}
			}
		case 2: // Take
			s.logger.Info("Stash take", zap.Uint32("stash_id", stashID), zap.String("section", section), zap.Int("count", count))
			if err := s.stashMgr.ModifyStashItem(stashID, section, -count); err != nil {
				if err == ErrStashNotFound {
					status = 1
				} else {
					status = 2
				}
			}
		case 3: // Store
			s.logger.Info("Stash store", zap.Uint32("stash_id", stashID), zap.String("section", section), zap.Int("count", count))
			if err := s.stashMgr.ModifyStashItem(stashID, section, +count); err != nil {
				if err == ErrStashNotFound {
					status = 1
				} else {
					status = 2
				}
			}
		default:
			s.logger.Warn("OpStashInteract: unknown action", zap.Uint8("action", pkt.Action))
			status = 2
		}

		resp := protocol.StashResponsePayload{
			StashID: stashID,
			Status:  status,
			Count:   pkt.Count,
		}
		if len(contents) > 0 && len(contents) <= len(resp.Data) {
			copy(resp.Data[:], contents)
		}
		s.sendStashResponse(addr, resp)
	case OpAck:
		if s.ackQueue != nil {
			var ackSeq uint32
			if binary.Read(buf, binary.LittleEndian, &ackSeq) == nil && ackSeq != 0 {
				s.ackQueue.Acknowledge(ackSeq)
			}
			// NOTE: do NOT acknowledge hdr.SequenceNum here — ACK packets are
			// unreliable and never enqueued; acking their own seq is a no-op
			// that masked the missing outbound-ACK bug below.
		}
	default:
		// Reliable inbound already ACKed above; nothing further needed.
	}
}

// sendAck transmits an unreliable OpAck for seq back to addr. Fire-and-forget:
// ACKs are never enqueued for retransmission.
func (s *Server) sendAck(addr *net.UDPAddr, seq uint32) {
	udp := s.GetUDP()
	if udp == nil && activeSink == nil {
		return
	}
	pktSeq := s.seq.Add(1)
	var buf bytes.Buffer
	if err := protocol.WritePacket(&buf, protocol.OpAck, pktSeq, protocol.FlagUnreliable, seq); err != nil {
		return
	}
	data := buf.Bytes()
	if activeSink != nil {
		activeSink.Record(data)
	}
	if udp != nil {
		_ = udp.Send(addr, data)
	}
}

func (s *Server) Tick(now time.Time) {
	// S-01: Only increment play time once per second (every 30 ticks at 30Hz)
	tick := s.tickCount.Add(1)
	isSecondTick := (tick%30 == 0)

	// Sleeper Tick (Issue 16)
	if s.sleepers != nil {
		s.sleepers.Tick(now, func(sl *Sleeper) {
			sl.RLock()
			uuid := sl.AccountID
			pos := sl.Position
			yaw := sl.Rotation[0]
			health := sl.Health
			sl.RUnlock()
			if uuid != "" {
				s.QueuePlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, health)
			}
			s.grid.Remove(sl.EntityID)
		}, func(sl *Sleeper) {
			sl.RLock()
			uuid := sl.AccountID
			pos := sl.Position
			yaw := sl.Rotation[0]
			sl.RUnlock()
			if uuid != "" {
				s.QueuePlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, 0.0)
			}
			s.grid.Remove(sl.EntityID)
		})
	}

	// Timeout stale sessions & periodic checkpointing (Issue 19)
	for _, sess := range s.sessions.GetAll() {
		sess.Lock()
		sessID := sess.SessionID
		uuid := sess.AccountID
		stale := now.Sub(sess.LastSeen) > 30*time.Second
		dirty := sess.Dirty
		pos := sess.Position
		yaw := sess.Rotation[0]
		health := sess.Health
		flagSafe := sess.InSafeZone
		level := sess.CurrentLevel
		inCombat := now.Before(sess.InCombatUntil)
		addrStr := ""
		if sess.UDPAddr != nil {
			addrStr = sess.UDPAddr.String()
		}
		sess.Unlock()
		// Authoritative safe-zone state: recompute from position, don't trust
		// a possibly-stale session flag (direct-inserted test sessions and
		// clients that never sent a transform have InSafeZone=false).
		inSafe := flagSafe || CheckSafeZone(pos[0], pos[1], pos[2], level) != nil
		if stale {
			s.grid.Remove(sessID)
			s.aoi.RemoveSession(sessID)
			if s.ackQueue != nil && addrStr != "" {
				s.ackQueue.RemoveByAddr(addrStr)
			}
			s.sessions.RemoveSession(sessID)
			if uuid != "" && dirty {
				s.QueuePlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, health)
			}
			// PRODUCTION FIX (§17.1): pull-the-cable combat logging must spawn
			// a sleeper exactly like explicit disconnect. Old code only did
			// this on OpDisconnect, so timeouts escaped punishment.
			if (!inSafe || inCombat) && s.sleepers != nil && health > 0 {
				if sleeper := s.sleepers.CreateSleeper(sess, 30*time.Second); sleeper != nil {
					s.grid.Insert(sleeper.EntityID, sleeper.Position[0], sleeper.Position[2])
					s.logger.Info("Spawned sleeper proxy for timed-out player",
						zap.String("uuid", uuid),
						zap.Uint32("entity_id", sleeper.EntityID),
					)
				}
			}
		} else {
			if isSecondTick && uuid != "" {
				s.QueuePeriodicStats(uuid, 1)
			}
			// 60s Checkpoint (Issue 19)
			if ShouldCheckpointSession(sess, now, 60*time.Second) && uuid != "" {
				s.QueuePlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, health)
				sess.Lock()
				sess.LastCheckpoint = now
				sess.Dirty = false
				sess.Unlock()
			}
		}
	}
	if udp := s.GetUDP(); udp != nil {
		s.aoi.BroadcastSnapshots(s.sessions, s.grid, udp, &s.seq)
	}

	if s.emissionMgr != nil {
		s.emissionMgr.Tick(now, s.sessions, s.GetUDP(), &s.seq)
	}

	if s.ackQueue != nil {
		s.ackQueue.Tick(now)
	}

	if udp := s.GetUDP(); udp != nil {
		if s.squads != nil {
			s.squads.Tick(now, func(squadID uint32, state ai.AIState, pos [3]float32) {
				s.broadcastSquadAction(squadID, state, pos)
			})
		}
	}
}

func (s *Server) broadcastSquadAction(squadID uint32, state ai.AIState, pos [3]float32) {
	payload := protocol.AIActionPayload{
		EntityID: squadID,
		Action:   uint8(state),
		TargetID: 0,
	}
	for _, sess := range s.sessions.GetAll() {
		s.SendToSession(sess, OpAiAction, protocol.FlagUnreliable, payload)
	}
}

func (s *Server) BroadcastSquadAction(squadID uint32, state ai.AIState, pos [3]float32) {
	s.broadcastSquadAction(squadID, state, pos)
}

func (s *Server) SendToSession(sess *network.PlayerSession, opcode uint16, flags uint8, payload interface{}) {
	udp := s.GetUDP()
	if sess == nil || sess.UDPAddr == nil || (udp == nil && activeSink == nil) {
		return
	}

	seq := s.seq.Add(1)

	var buf bytes.Buffer
	if err := protocol.WritePacket(&buf, opcode, seq, flags, payload); err != nil {
		s.logger.Error("Failed to write packet", zap.Error(err))
		return
	}

	data := buf.Bytes()

	if activeSink != nil {
		activeSink.Record(data)
	}

	if (flags & protocol.FlagReliable) != 0 {
		if s.ackQueue != nil {
			s.ackQueue.EnqueueReliable(seq, sess.UDPAddr, data)
		}
	}

	if udp != nil {
		if err := udp.Send(sess.UDPAddr, data); err != nil {
			s.logger.Error("Failed to send packet", zap.Error(err))
		}
	}
}

func (s *Server) QueueWrite(query string, args ...interface{}) {
	if s.dbQueue == nil {
		return
	}
	select {
	case s.dbQueue <- &database.DBWriteJob{Query: query, Args: args}:
	default:
		s.logger.Warn("DB write queue full, dropping write", zap.String("query", query))
	}
}

func (s *Server) QueuePlayerTransform(uuid string, x, y, z, yaw, health float32) {
	s.QueueWrite("UPDATE characters SET pos_x=?, pos_y=?, pos_z=?, yaw=?, health=?, updated_at=? WHERE client_uuid=?",
		x, y, z, yaw, health, time.Now().Unix(), uuid)
}

func (s *Server) QueuePeriodicStats(uuid string, playTimeDeltaSec int) {
	s.QueueWrite("UPDATE characters SET play_time_sec = play_time_sec + ?, updated_at=? WHERE client_uuid=?",
		playTimeDeltaSec, time.Now().Unix(), uuid)
}

func (s *Server) BroadcastChat(senderID uint32, msg string) {
	pkt := protocol.NewChatText(senderID, msg)
	for _, sess := range s.sessions.GetAll() {
		s.SendToSession(sess, protocol.OpChatText, protocol.FlagReliable, pkt)
	}
}

func (s *Server) GetStats() (int, int, time.Duration) {
	active := len(s.sessions.GetAll())
	tickRate := 30
	if s.cfg != nil && s.cfg.TickRateHz > 0 {
		tickRate = s.cfg.TickRateHz
	}
	uptime := time.Since(s.startTime)
	return active, tickRate, uptime
}

func (s *Server) KickSession(sessionID uint32) bool {
	sess := s.sessions.GetByID(sessionID)
	if sess == nil {
		return false
	}

	// S-16: Save player character state before removing session
	sess.Lock()
	uuid := sess.AccountID
	pos := sess.Position
	yaw := sess.Rotation[0]
	health := sess.Health
	sess.Unlock()

	if uuid != "" {
		s.QueuePlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, health)
		if s.db != nil && s.dbQueue == nil {
			_, _ = s.db.RawDB().Exec(
				"UPDATE characters SET pos_x=?, pos_y=?, pos_z=?, yaw=?, health=?, updated_at=? WHERE client_uuid=?",
				pos[0], pos[1], pos[2], yaw, health, time.Now().Unix(), uuid,
			)
		}
	}

	s.SendToSession(sess, protocol.OpDisconnect, protocol.FlagReliable, uint8(1))
	s.grid.Remove(sessionID)
	s.aoi.RemoveSession(sessionID)
	s.sessions.RemoveSession(sessionID)
	return true
}

func (s *Server) BanPlayer(uuid string, reason string) error {
	if s.db != nil {
		if err := s.db.BanAccount(uuid, reason); err != nil {
			return err
		}
	}
	// S-16 & S-24: Flush state via KickSession and break immediately upon finding matching session
	for _, sess := range s.sessions.GetAll() {
		sess.Lock()
		accID := sess.AccountID
		sessID := sess.SessionID
		sess.Unlock()
		if accID == uuid {
			s.KickSession(sessID)
			break
		}
	}
	return nil
}

// sendStashResponse serialises resp as OpStashResponse and sends it directly
// to addr without requiring an established session (client may not be registered yet).
func (s *Server) sendStashResponse(addr *net.UDPAddr, resp protocol.StashResponsePayload) {
	udp := s.GetUDP()
	if udp == nil && activeSink == nil {
		return
	}
	seq := s.seq.Add(1)
	var buf bytes.Buffer
	if err := protocol.WritePacket(&buf, protocol.OpStashResponse, seq, protocol.FlagReliable, resp); err != nil {
		s.logger.Error("sendStashResponse: failed to write packet", zap.Error(err))
		return
	}
	data := buf.Bytes()
	if activeSink != nil {
		activeSink.Record(data)
	}
	if s.ackQueue != nil {
		s.ackQueue.EnqueueReliable(seq, addr, data)
	}
	if udp != nil {
		if err := udp.Send(addr, data); err != nil {
			s.logger.Error("sendStashResponse: send failed", zap.Error(err))
		}
	}
}

// nullTermString converts a null-terminated fixed-width byte slice to a Go string.
func nullTermString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
