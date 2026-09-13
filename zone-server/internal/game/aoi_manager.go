package game

import (
	"sync"
	"sync/atomic"

	"zone-online/zone-server/internal/network"
)

const AoIRadius float32 = 220.0

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
	// Stubs for snapshots
}

func (a *AoIManager) NotifyEntityEnter(session *network.PlayerSession, entityID uint32, entityType uint8, section string, pos [3]float32, faction uint8, health uint16, udp *network.UDPListener, seq *atomic.Uint32) {
	// Stubs for enter aoi
}

func (a *AoIManager) NotifyEntityLeave(session *network.PlayerSession, entityID uint32, udp *network.UDPListener, seq *atomic.Uint32) {
	// Stubs for leave aoi
}
