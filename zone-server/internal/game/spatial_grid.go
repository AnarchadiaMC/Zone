package game

import (
	"math"
	"sync"
)

type gridCellKey struct {
	X int32
	Z int32
}

// SpatialGrid provides a 2D spatial partitioning grid for fast proximity queries.
type SpatialGrid struct {
	cellSize        float32
	mu              sync.RWMutex
	cells           map[gridCellKey]map[uint32]struct{}
	entityPositions map[uint32]gridCellKey
	entityCoords    map[uint32][2]float32
}

// NewSpatialGrid creates a new SpatialGrid with the specified cell size.
func NewSpatialGrid(cellSize float32) *SpatialGrid {
	if cellSize <= 0 {
		cellSize = 64.0
	}
	return &SpatialGrid{
		cellSize:        cellSize,
		cells:           make(map[gridCellKey]map[uint32]struct{}),
		entityPositions: make(map[uint32]gridCellKey),
		entityCoords:    make(map[uint32][2]float32),
	}
}

// cellKey computes the gridCellKey for the given world coordinates.
func (g *SpatialGrid) cellKey(x, z float32) gridCellKey {
	return gridCellKey{
		X: int32(math.Floor(float64(x / g.cellSize))),
		Z: int32(math.Floor(float64(z / g.cellSize))),
	}
}

// Insert adds an entity to the grid at (x, z). If the entity already exists,
// it is removed from its prior cell and inserted at the new coordinates.
func (g *SpatialGrid) Insert(id uint32, x, z float32) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if oldKey, exists := g.entityPositions[id]; exists {
		if cell, ok := g.cells[oldKey]; ok {
			delete(cell, id)
			if len(cell) == 0 {
				delete(g.cells, oldKey)
			}
		}
	}

	key := g.cellKey(x, z)
	cell, ok := g.cells[key]
	if !ok {
		cell = make(map[uint32]struct{})
		g.cells[key] = cell
	}
	cell[id] = struct{}{}
	g.entityPositions[id] = key
	g.entityCoords[id] = [2]float32{x, z}
}

// Remove removes an entity from its cell and reverse mapping in O(1) time.
func (g *SpatialGrid) Remove(id uint32) {
	g.mu.Lock()
	defer g.mu.Unlock()

	key, exists := g.entityPositions[id]
	if !exists {
		return
	}

	if cell, ok := g.cells[key]; ok {
		delete(cell, id)
		if len(cell) == 0 {
			delete(g.cells, key)
		}
	}
	delete(g.entityPositions, id)
	delete(g.entityCoords, id)
}

// Update updates an entity's coordinates. If the entity crossed into a new cell,
// it updates the cell membership. If not present, it inserts it.
func (g *SpatialGrid) Update(id uint32, x, z float32) {
	g.mu.Lock()
	defer g.mu.Unlock()

	oldKey, exists := g.entityPositions[id]
	if !exists {
		key := g.cellKey(x, z)
		cell, ok := g.cells[key]
		if !ok {
			cell = make(map[uint32]struct{})
			g.cells[key] = cell
		}
		cell[id] = struct{}{}
		g.entityPositions[id] = key
		g.entityCoords[id] = [2]float32{x, z}
		return
	}

	newKey := g.cellKey(x, z)
	if oldKey != newKey {
		if oldCell, ok := g.cells[oldKey]; ok {
			delete(oldCell, id)
			if len(oldCell) == 0 {
				delete(g.cells, oldKey)
			}
		}
		newCell, ok := g.cells[newKey]
		if !ok {
			newCell = make(map[uint32]struct{})
			g.cells[newKey] = newCell
		}
		newCell[id] = struct{}{}
		g.entityPositions[id] = newKey
	}
	g.entityCoords[id] = [2]float32{x, z}
}

// GetNeighbors finds all entities within radius of (x, z).
// It queries only the candidate cells intersecting [x-radius, x+radius] x [z-radius, z+radius]
// and verifies exact Euclidean distance (dx^2 + dz^2 <= radius^2).
func (g *SpatialGrid) GetNeighbors(x, z float32, radius float32) []uint32 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if radius < 0 {
		return []uint32{}
	}

	minCellX := int32(math.Floor(float64((x - radius) / g.cellSize)))
	maxCellX := int32(math.Floor(float64((x + radius) / g.cellSize)))
	minCellZ := int32(math.Floor(float64((z - radius) / g.cellSize)))
	maxCellZ := int32(math.Floor(float64((z + radius) / g.cellSize)))

	radiusSq := radius * radius
	neighbors := make([]uint32, 0)
	seen := make(map[uint32]struct{})

	for cx := minCellX; cx <= maxCellX; cx++ {
		for cz := minCellZ; cz <= maxCellZ; cz++ {
			cell, ok := g.cells[gridCellKey{X: cx, Z: cz}]
			if !ok {
				continue
			}
			for id := range cell {
				if _, alreadySeen := seen[id]; alreadySeen {
					continue
				}
				coords, ok := g.entityCoords[id]
				if !ok {
					continue
				}
				dx := coords[0] - x
				dz := coords[1] - z
				if dx*dx+dz*dz <= radiusSq {
					seen[id] = struct{}{}
					neighbors = append(neighbors, id)
				}
			}
		}
	}

	return neighbors
}

// Count returns the total number of tracked entities in the grid.
func (g *SpatialGrid) Count() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.entityPositions)
}

// Clear removes all entities from the grid.
func (g *SpatialGrid) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cells = make(map[gridCellKey]map[uint32]struct{})
	g.entityPositions = make(map[uint32]gridCellKey)
	g.entityCoords = make(map[uint32][2]float32)
}

// GetPosition retrieves the (x, z) coordinates of an entity.
func (g *SpatialGrid) GetPosition(id uint32) (float32, float32, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	coords, ok := g.entityCoords[id]
	if !ok {
		return 0, 0, false
	}
	return coords[0], coords[1], true
}

// Contains reports whether the entity is tracked in the grid.
func (g *SpatialGrid) Contains(id uint32) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.entityPositions[id]
	return ok
}

// CellSize returns the configured cell size.
func (g *SpatialGrid) CellSize() float32 {
	return g.cellSize
}
