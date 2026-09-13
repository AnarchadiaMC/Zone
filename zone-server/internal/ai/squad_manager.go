package ai

import (
	"zone-online/zone-server/internal/network"
)

type SquadManager struct{}

func NewSquadManager() *SquadManager {
	return &SquadManager{}
}

// Ensure the type of the grid matches by keeping it an empty interface for the test, or just import game but circular dependency needs to be avoided
// To avoid circular dependency game <-> ai, we use interface
type GridInterface interface {
	GetNeighbors(x, z float32, radius float32) []uint32
}

func (s *SquadManager) Tick(sessions *network.SessionManager, grid interface{}) {
	// tick
}
