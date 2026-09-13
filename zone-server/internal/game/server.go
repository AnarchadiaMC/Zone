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

type EconomyManager struct{}
func NewEconomyManager() *EconomyManager { return &EconomyManager{} }

type Server struct {
	cfg       *config.Config
	db        *database.DB
	udp       *network.UDPListener
	sessions  *network.SessionManager
	grid      *SpatialGrid
	events    *EventManager
	economy   *EconomyManager
	anticheat *AntiCheatManager
	aoi       *AoIManager
	squads    *ai.SquadManager
	logger    *zap.Logger
	seq       atomic.Uint32
	ackQueue  *network.AckQueue
}

func NewServer(cfg *config.Config, db *database.DB, logger *zap.Logger) *Server {
	s := &Server{
		cfg:       cfg,
		db:        db,
		sessions:  network.NewSessionManager(),
		grid:      NewSpatialGrid(64.0),
		events:    NewEventManager(),
		economy:   NewEconomyManager(),
		anticheat: NewAntiCheatManager(),
		aoi:       NewAoIManager(),
		squads:    ai.NewSquadManager(),
		logger:    logger,
		ackQueue:  network.NewAckQueue(),
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
		if sess := s.sessions.GetByAddr(addr.String()); sess != nil {
			sess.Lock()
			sess.LastSeen = time.Now()
			sess.Unlock()
		}
	}
}

func (s *Server) Tick(now time.Time) {
	// Timeout stale sessions
	for _, sess := range s.sessions.GetAll() {
		sess.Lock()
		stale := now.Sub(sess.LastSeen) > 30*time.Second
		sess.Unlock()
		if stale {
			s.sessions.RemoveSession(sess.SessionID)
		}
	}
	s.aoi.BroadcastSnapshots(s.sessions, s.grid, s.udp, &s.seq)
	s.squads.Tick(s.sessions, s.grid)
}

func (s *Server) SendToSession(sess *network.PlayerSession, opcode uint16, flags uint8, payload interface{}) {
	// Send packet...
}
