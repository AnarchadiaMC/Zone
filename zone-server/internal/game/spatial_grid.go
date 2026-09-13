package game

import (
	"sync"
)

type SpatialGrid struct {
	cellSize float32
	mu       sync.RWMutex
	cells    map[string][]uint32
}

func NewSpatialGrid(cellSize float32) *SpatialGrid {
	return &SpatialGrid{
		cellSize: cellSize,
		cells:    make(map[string][]uint32),
	}
}

func (g *SpatialGrid) GetNeighbors(x, z float32, radius float32) []uint32 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return []uint32{}
}
