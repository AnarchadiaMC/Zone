package game

import (
	"context"
	"bytes"
	"encoding/binary"
	"net"
	"time"
	"sync/atomic"

	"zone-online/zone-server/internal/config"
	"zone-online/zone-server/internal/database"
	"zone-online/zone-server/internal/network"
	"zone-online/zone-server/internal/protocol"
	"zone-online/zone-server/internal/ai"
	"go.uber.org/zap"
)

type EventManager struct{}
func NewEventManager() *EventManager { return &EventManager{} }

type AntiCheatManager struct{}
func NewAntiCheatManager() *AntiCheatManager { return &AntiCheatManager{} }

type Server struct {
	cfg       *config.Config
	db        *database.DB
	dbQueue   chan *database.DBWriteJob
	udp       *network.UDPListener
	sessions  *network.SessionManager
	grid      *SpatialGrid
	events    *EventManager
	economy   *EconomyManager
	stashMgr  *StashManager
	anticheat *AntiCheatManager
	aoi       *AoIManager
	squads    *ai.SquadManager
	logger    *zap.Logger
	seq       atomic.Uint32
	ackQueue  *network.AckQueue
	startTime time.Time
}

func NewServer(cfg *config.Config, db *database.DB, logger *zap.Logger, dbQueue ...chan *database.DBWriteJob) *Server {
	var q chan *database.DBWriteJob
	if len(dbQueue) > 0 {
		q = dbQueue[0]
	}
	s := &Server{
		cfg:       cfg,
		db:        db,
		dbQueue:   q,
		sessions:  network.NewSessionManager(),
		grid:      NewSpatialGrid(64.0),
		events:    NewEventManager(),
		economy:   NewEconomyManager(db),
		stashMgr:  NewStashManager(db),
		anticheat: NewAntiCheatManager(),
		aoi:       NewAoIManager(),
		squads:    ai.NewSquadManager(),
		logger:    logger,
		ackQueue:  network.NewAckQueue(),
		startTime: time.Now(),
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	udp, err := network.NewUDPListener(s.cfg.Port, s.HandlePacket)
	if err != nil {
		return err
	}
	s.udp = udp
	s.udp.Start(ctx)
	
	s.logger.Info("Server started")
	<-ctx.Done()
	return s.udp.Close()
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

	// Simple dispatch
	switch hdr.Opcode {
	case protocol.OpHandshakeReq:
		// Implementation for HandshakeReq
		var req protocol.HandshakeReq
		if err := binary.Read(buf, binary.LittleEndian, &req); err == nil {
			s.logger.Info("Handshake", zap.String("addr", addr.String()))
		}
	case protocol.OpHeartbeat:
		var hb protocol.HeartbeatPayload
		if err := binary.Read(buf, binary.LittleEndian, &hb); err == nil {
			if sess := s.sessions.GetByAddr(addr.String()); sess != nil {
				sess.Lock()
				sess.LastSeen = time.Now()
				sess.Unlock()
				s.SendToSession(sess, protocol.OpHeartbeat, protocol.FlagUnreliable, hb)
			} else {
				var respBuf bytes.Buffer
				seq := s.seq.Add(1)
				if err := protocol.WritePacket(&respBuf, protocol.OpHeartbeat, seq, protocol.FlagUnreliable, hb); err == nil && s.udp != nil {
					_ = s.udp.Send(addr, respBuf.Bytes())
				}
			}
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
			if sess := s.sessions.GetByAddr(addr.String()); sess != nil {
				sess.Lock()
				sess.Position = [3]float32{ct.PosX, ct.PosY, ct.PosZ}
				sess.Rotation = [2]float32{float32(ct.Yaw) / 100.0, float32(ct.Pitch) / 100.0}
				sess.Velocity = [3]float32{float32(ct.VelX) / 100.0, float32(ct.VelY) / 100.0, float32(ct.VelZ) / 100.0}
				sess.AnimFlags = ct.AnimFlags
				sess.LastSeen = time.Now()
				uuid := sess.AccountID
				health := sess.Health
				sess.Unlock()

				if uuid != "" {
					s.QueuePlayerTransform(uuid, ct.PosX, ct.PosY, ct.PosZ, float32(ct.Yaw)/100.0, health)
				}
			}
		}
	}
}

func (s *Server) Tick(now time.Time) {
	// Timeout stale sessions & queue periodic stats
	for _, sess := range s.sessions.GetAll() {
		sess.Lock()
		uuid := sess.AccountID
		stale := now.Sub(sess.LastSeen) > 30*time.Second
		sess.Unlock()
		if stale {
			s.sessions.RemoveSession(sess.SessionID)
		} else if uuid != "" {
			s.QueuePeriodicStats(uuid, 1)
		}
	}
	s.aoi.BroadcastSnapshots(s.sessions, s.grid, s.udp, &s.seq)
}

func (s *Server) SendToSession(sess *network.PlayerSession, opcode uint16, flags uint8, payload interface{}) {
	if sess == nil || sess.UDPAddr == nil || s.udp == nil {
		return
	}

	seq := s.seq.Add(1)

	var buf bytes.Buffer
	if err := protocol.WritePacket(&buf, opcode, seq, flags, payload); err != nil {
		s.logger.Error("Failed to write packet", zap.Error(err))
		return
	}

	data := buf.Bytes()

	if (flags & protocol.FlagReliable) != 0 {
		if s.ackQueue != nil {
			s.ackQueue.EnqueueReliable(seq, sess.UDPAddr, data)
		}
	}

	if err := s.udp.Send(sess.UDPAddr, data); err != nil {
		s.logger.Error("Failed to send packet", zap.Error(err))
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
	s.SendToSession(sess, protocol.OpDisconnect, protocol.FlagReliable, uint8(1))
	s.sessions.RemoveSession(sessionID)
	return true
}

func (s *Server) BanPlayer(uuid string, reason string) error {
	if s.db != nil {
		if err := s.db.BanAccount(uuid, reason); err != nil {
			return err
		}
	}
	for _, sess := range s.sessions.GetAll() {
		sess.Lock()
		accID := sess.AccountID
		sessID := sess.SessionID
		sess.Unlock()
		if accID == uuid {
			s.KickSession(sessID)
		}
	}
	return nil
}
