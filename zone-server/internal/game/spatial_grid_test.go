package game

import "testing"

func TestSpatialGrid(t *testing.T) {
	grid := NewSpatialGrid(10.0)
	if grid == nil {
		t.Fatal("NewSpatialGrid returned nil")
	}

	if grid.cellSize != 10.0 {
		t.Errorf("expected cellSize 10.0, got %v", grid.cellSize)
	}

	neighbors := grid.GetNeighbors(0, 0, 5)
	if len(neighbors) != 0 {
		t.Errorf("expected 0 neighbors, got %v", len(neighbors))
	}
}
