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

// SafezoneState carries safe zone transition state.
type SafezoneState struct {
	InSafeZone uint8
}

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
	seq         atomic.Uint32
	ackQueue    *network.AckQueue
	emissionMgr *EmissionOrchestrator
	startTime   time.Time
	tickCount   atomic.Uint64
}

func NewServer(cfg *config.Config, db *database.DB, logger *zap.Logger, dbQueue ...chan *database.DBWriteJob) *Server {
	var q chan *database.DBWriteJob
	if len(dbQueue) > 0 {
		q = dbQueue[0]
	}
	s := &Server{
		cfg:         cfg,
		db:          db,
		dbQueue:     q,
		sessions:    network.NewSessionManager(),
		grid:        NewSpatialGrid(64.0),
		events:      NewEventManager(),
		economy:     NewEconomyManager(db),
		stashMgr:    NewStashManager(db),
		anticheat:   NewAntiCheatManager(),
		aoi:         NewAoIManager(),
		squads:      ai.NewSquadManager(),
		logger:      logger,
		ackQueue:    network.NewAckQueue(),
		emissionMgr: NewEmissionOrchestrator(),
		startTime:   time.Now(),
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

	switch hdr.Opcode {
	case protocol.OpHandshakeReq:
		var req protocol.HandshakeReq
		if err := binary.Read(buf, binary.LittleEndian, &req); err == nil {
			s.logger.Info("Handshake", zap.String("addr", addr.String()))
			cleanUUID := nullTermString(req.UUID[:])
			if cleanUUID == "" {
				cleanUUID = string(bytes.Trim(req.UUID[:], "\x00"))
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
			sess := &network.PlayerSession{
				SessionID:    sessID,
				AccountID:    cleanUUID,
				UDPAddr:      addr,
				LastSeen:     time.Now(),
				Health:       100.0,
				CurrentLevel: "l01_escape",
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
			inSZ := (CheckSafeZone(sess.Position[0], sess.Position[1], sess.Position[2], sess.CurrentLevel) != nil)
			sess.InSafeZone = inSZ

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
			sess.Unlock()

			if uuid != "" {
				s.QueuePlayerTransform(uuid, pos[0], pos[1], pos[2], yaw, health)
			}
			s.grid.Remove(sessID)
			s.aoi.RemoveSession(sessID)

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
				sess.Lock()
				sessID := sess.SessionID
				sess.Position = [3]float32{ct.PosX, ct.PosY, ct.PosZ}
				sess.Rotation = [2]float32{float32(ct.Yaw) / 100.0, float32(ct.Pitch) / 100.0}
				sess.Velocity = [3]float32{float32(ct.VelX) / 100.0, float32(ct.VelY) / 100.0, float32(ct.VelZ) / 100.0}
				sess.AnimFlags = ct.AnimFlags
				sess.LastSeen = time.Now()
				uuid := sess.AccountID
				health := sess.Health
				level := sess.CurrentLevel
				prevSafe := sess.InSafeZone
				sess.Unlock()

				s.grid.Update(sessID, ct.PosX, ct.PosZ)

				isSafe := (CheckSafeZone(ct.PosX, ct.PosY, ct.PosZ, level) != nil)
				if isSafe != prevSafe {
					sess.Lock()
					sess.InSafeZone = isSafe
					sess.Unlock()

					s.SendToSession(sess, protocol.OpSafezoneState, protocol.FlagReliable, SafezoneState{InSafeZone: boolToUint8(isSafe)})
				}

				if uuid != "" {
					s.QueuePlayerTransform(uuid, ct.PosX, ct.PosY, ct.PosZ, float32(ct.Yaw)/100.0, health)
				}
			}
		}
	case protocol.OpChatText:
		var pkt protocol.ChatText
		if err := binary.Read(buf, binary.LittleEndian, &pkt); err != nil {
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
			s.ackQueue.Acknowledge(hdr.SequenceNum)
		}
	default:
		if s.ackQueue != nil && (hdr.FlagsChannel&protocol.FlagReliable != 0 || hdr.Opcode == 0) {
			s.ackQueue.Acknowledge(hdr.SequenceNum)
		}
	}
}

func (s *Server) Tick(now time.Time) {
	// S-01: Only increment play time once per second (every 30 ticks at 30Hz)
	tick := s.tickCount.Add(1)
	isSecondTick := (tick%30 == 0)

	// Timeout stale sessions & queue periodic stats
	for _, sess := range s.sessions.GetAll() {
		sess.Lock()
		sessID := sess.SessionID
		uuid := sess.AccountID
		stale := now.Sub(sess.LastSeen) > 30*time.Second
		sess.Unlock()
		if stale {
			s.grid.Remove(sessID)
			s.aoi.RemoveSession(sessID)
			s.sessions.RemoveSession(sessID)
		} else if isSecondTick && uuid != "" {
			s.QueuePeriodicStats(uuid, 1)
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
		SquadID: squadID,
		State:   uint8(state),
		PosX:    pos[0],
		PosY:    pos[1],
		PosZ:    pos[2],
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
