package game

import (
	"math/rand"
	"sort"
	"sync"
	"testing"
)

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

func TestSpatialGrid_InitDefaults(t *testing.T) {
	gZero := NewSpatialGrid(0)
	if gZero.cellSize != 64.0 {
		t.Errorf("expected default cellSize 64.0 for 0, got %v", gZero.cellSize)
	}

	gNeg := NewSpatialGrid(-10)
	if gNeg.cellSize != 64.0 {
		t.Errorf("expected default cellSize 64.0 for negative, got %v", gNeg.cellSize)
	}
}

func TestSpatialGrid_Lifecycle(t *testing.T) {
	g := NewSpatialGrid(32.0)

	if g.Count() != 0 {
		t.Fatalf("expected count 0, got %d", g.Count())
	}

	// Insert
	g.Insert(100, 10.0, 20.0)
	g.Insert(200, 50.0, 60.0)

	if g.Count() != 2 {
		t.Fatalf("expected count 2, got %d", g.Count())
	}
	if !g.Contains(100) || !g.Contains(200) {
		t.Fatal("expected both 100 and 200 to be contained")
	}
	if g.Contains(300) {
		t.Fatal("did not expect 300 to be contained")
	}

	x, z, ok := g.GetPosition(100)
	if !ok || x != 10.0 || z != 20.0 {
		t.Fatalf("expected pos (10, 20), got (%v, %v, %v)", x, z, ok)
	}

	// Overwrite existing ID via Insert
	g.Insert(100, 15.0, 25.0)
	if g.Count() != 2 {
		t.Fatalf("expected count 2 after re-insert, got %d", g.Count())
	}
	x, z, ok = g.GetPosition(100)
	if !ok || x != 15.0 || z != 25.0 {
		t.Fatalf("expected updated pos (15, 25), got (%v, %v)", x, z)
	}

	// Update within same cell (cellSize=32: (15,25) and (20,25) are both cell (0,0))
	g.Update(100, 20.0, 25.0)
	x, z, ok = g.GetPosition(100)
	if !ok || x != 20.0 || z != 25.0 {
		t.Fatalf("expected updated pos (20, 25), got (%v, %v)", x, z)
	}
	if g.Count() != 2 {
		t.Fatalf("expected count 2 after update, got %d", g.Count())
	}

	// Update across cell boundary: to (100.0, 100.0) -> cell (3, 3)
	g.Update(100, 100.0, 100.0)
	x, z, ok = g.GetPosition(100)
	if !ok || x != 100.0 || z != 100.0 {
		t.Fatalf("expected updated pos (100, 100), got (%v, %v)", x, z)
	}

	// Update non-existent entity inserts it
	g.Update(300, -10.0, -10.0)
	if g.Count() != 3 || !g.Contains(300) {
		t.Fatalf("expected entity 300 to be inserted by Update, count=%d", g.Count())
	}

	// Remove
	g.Remove(100)
	if g.Count() != 2 {
		t.Fatalf("expected count 2 after remove, got %d", g.Count())
	}
	if g.Contains(100) {
		t.Fatal("expected entity 100 to be removed")
	}
	if _, _, ok := g.GetPosition(100); ok {
		t.Fatal("expected GetPosition for entity 100 to return false")
	}

	// Remove non-existent entity is no-op
	g.Remove(999)
	if g.Count() != 2 {
		t.Fatalf("expected count 2 after removing non-existent entity, got %d", g.Count())
	}

	// Clear
	g.Clear()
	if g.Count() != 0 {
		t.Fatalf("expected count 0 after clear, got %d", g.Count())
	}
	if g.Contains(200) || g.Contains(300) {
		t.Fatal("expected all entities cleared")
	}
}

func TestSpatialGrid_GetNeighbors_Distances(t *testing.T) {
	g := NewSpatialGrid(50.0)

	// Center point (100, 100)
	g.Insert(1, 100.0, 100.0) // dist 0
	g.Insert(2, 100.0, 130.0) // dist 30 (dz=30)
	g.Insert(3, 130.0, 140.0) // dist 50 (dx=30, dz=40 -> 30^2+40^2=2500=50^2)
	g.Insert(4, 131.0, 140.0) // dist > 50 (31^2+40^2=2561 > 2500)
	g.Insert(5, 200.0, 200.0) // dist ~141.4 (dx=100, dz=100)

	// Query with radius 20: only 1
	n1 := g.GetNeighbors(100.0, 100.0, 20.0)
	if len(n1) != 1 || n1[0] != 1 {
		t.Fatalf("expected [1], got %v", n1)
	}

	// Query with radius 30: 1 and 2
	n2 := g.GetNeighbors(100.0, 100.0, 30.0)
	sortIDs(n2)
	if len(n2) != 2 || n2[0] != 1 || n2[1] != 2 {
		t.Fatalf("expected [1, 2], got %v", n2)
	}

	// Query with radius 50: 1, 2, and 3 (exact distance 50 included), not 4 or 5
	n3 := g.GetNeighbors(100.0, 100.0, 50.0)
	sortIDs(n3)
	if len(n3) != 3 || n3[0] != 1 || n3[1] != 2 || n3[2] != 3 {
		t.Fatalf("expected [1, 2, 3], got %v", n3)
	}

	// Query with radius 51: includes 4
	n4 := g.GetNeighbors(100.0, 100.0, 51.0)
	sortIDs(n4)
	if len(n4) != 4 || n4[3] != 4 {
		t.Fatalf("expected 4 entities including 4, got %v", n4)
	}

	// Query far away
	nFar := g.GetNeighbors(1000.0, 1000.0, 50.0)
	if len(nFar) != 0 {
		t.Fatalf("expected empty slice, got %v", nFar)
	}
}

func TestSpatialGrid_AcrossCellBoundaries(t *testing.T) {
	// Cell size 10.0
	g := NewSpatialGrid(10.0)

	// Entity 1 at (9.9, 5.0) -> cell (0, 0)
	// Entity 2 at (10.1, 5.0) -> cell (1, 0)
	g.Insert(1, 9.9, 5.0)
	g.Insert(2, 10.1, 5.0)

	// Entity 3 at (5.0, 9.9) -> cell (0, 0)
	// Entity 4 at (5.0, 10.1) -> cell (0, 1)
	g.Insert(3, 5.0, 9.9)
	g.Insert(4, 5.0, 10.1)

	// Across X boundary: query at (10.0, 5.0) with radius 1.0
	nx := g.GetNeighbors(10.0, 5.0, 1.0)
	sortIDs(nx)
	if len(nx) != 2 || nx[0] != 1 || nx[1] != 2 {
		t.Fatalf("expected [1, 2] across X boundary, got %v", nx)
	}

	// Across Z boundary: query at (5.0, 10.0) with radius 1.0
	nz := g.GetNeighbors(5.0, 10.0, 1.0)
	sortIDs(nz)
	if len(nz) != 2 || nz[0] != 3 || nz[1] != 4 {
		t.Fatalf("expected [3, 4] across Z boundary, got %v", nz)
	}

	// Four adjacent cells meeting at corner (10.0, 10.0):
	// Cell (0, 0): (9.0, 9.0)
	// Cell (1, 0): (11.0, 9.0)
	// Cell (0, 1): (9.0, 11.0)
	// Cell (1, 1): (11.0, 11.0)
	g.Insert(10, 9.0, 9.0)
	g.Insert(11, 11.0, 9.0)
	g.Insert(12, 9.0, 11.0)
	g.Insert(13, 11.0, 11.0)

	// Distance from (10.0, 10.0) to each corner entity is sqrt(1+1) = sqrt(2) ~ 1.414
	nCorner := g.GetNeighbors(10.0, 10.0, 1.5)
	cornerIDs := []uint32{}
	for _, id := range nCorner {
		if id >= 10 && id <= 13 {
			cornerIDs = append(cornerIDs, id)
		}
	}
	sortIDs(cornerIDs)
	if len(cornerIDs) != 4 || cornerIDs[0] != 10 || cornerIDs[1] != 11 || cornerIDs[2] != 12 || cornerIDs[3] != 13 {
		t.Fatalf("expected entities 10..13 across 4 quadrant cells, got %v", cornerIDs)
	}
}

func TestSpatialGrid_BoundaryConditions(t *testing.T) {
	g := NewSpatialGrid(20.0)

	// Negative coordinates
	g.Insert(1, -50.0, -50.0)
	g.Insert(2, -49.0, -50.0)
	g.Insert(3, -0.5, -0.5)
	g.Insert(4, 0.5, 0.5)

	nNeg := g.GetNeighbors(-50.0, -50.0, 2.0)
	sortIDs(nNeg)
	if len(nNeg) != 2 || nNeg[0] != 1 || nNeg[1] != 2 {
		t.Fatalf("expected [1, 2] in negative coords, got %v", nNeg)
	}

	// Crossing zero boundary between -0.5 and +0.5
	nZero := g.GetNeighbors(0.0, 0.0, 1.0)
	sortIDs(nZero)
	if len(nZero) != 2 || nZero[0] != 3 || nZero[1] != 4 {
		t.Fatalf("expected [3, 4] crossing zero boundary, got %v", nZero)
	}

	// Large coordinates
	g.Insert(5, 500000.0, -500000.0)
	nLarge := g.GetNeighbors(500000.0, -500000.0, 10.0)
	if len(nLarge) != 1 || nLarge[0] != 5 {
		t.Fatalf("expected [5] at large coords, got %v", nLarge)
	}

	// Zero radius: exact match only
	nZeroR := g.GetNeighbors(-50.0, -50.0, 0.0)
	if len(nZeroR) != 1 || nZeroR[0] != 1 {
		t.Fatalf("expected [1] with zero radius at exact position, got %v", nZeroR)
	}

	nZeroRMiss := g.GetNeighbors(-50.01, -50.0, 0.0)
	if len(nZeroRMiss) != 0 {
		t.Fatalf("expected empty with zero radius at offset position, got %v", nZeroRMiss)
	}

	// Negative radius
	nNegR := g.GetNeighbors(0.0, 0.0, -5.0)
	if len(nNegR) != 0 {
		t.Fatalf("expected empty slice for negative radius, got %v", nNegR)
	}
}

func TestSpatialGrid_Concurrency(t *testing.T) {
	g := NewSpatialGrid(25.0)
	const numWriters = 4
	const numReaders = 4
	const opsPerWorker = 500

	var wg sync.WaitGroup
	wg.Add(numWriters + numReaders)

	// Concurrent writers inserting, updating, removing
	for w := 0; w < numWriters; w++ {
		workerID := uint32(w)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(workerID * 1000)))
			for i := 0; i < opsPerWorker; i++ {
				entityID := workerID*1000 + uint32(i%100)
				x := rng.Float32()*400.0 - 200.0
				z := rng.Float32()*400.0 - 200.0

				switch i % 3 {
				case 0:
					g.Insert(entityID, x, z)
				case 1:
					g.Update(entityID, x, z)
				case 2:
					g.Remove(entityID)
				}
			}
		}()
	}

	// Concurrent readers
	for r := 0; r < numReaders; r++ {
		readerID := r
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(readerID * 2000)))
			for i := 0; i < opsPerWorker; i++ {
				x := rng.Float32()*400.0 - 200.0
				z := rng.Float32()*400.0 - 200.0
				radius := rng.Float32() * 60.0

				_ = g.GetNeighbors(x, z, radius)
				_ = g.Count()
				_ = g.Contains(uint32(rng.Intn(4000)))
			}
		}()
	}

	wg.Wait()
}

func sortIDs(ids []uint32) {
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
}
