package los

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func quad(v0, v1, v2, v3 [3]float32) *Mesh {
	return &Mesh{
		Vertices:  [][3]float32{v0, v1, v2, v3},
		Triangles: [][3]uint32{{0, 1, 2}, {0, 2, 3}},
	}
}

func twoQuads(v0, v1, v2, v3, w0, w1, w2, w3 [3]float32) *Mesh {
	return &Mesh{
		Vertices:  [][3]float32{v0, v1, v2, v3, w0, w1, w2, w3},
		Triangles: [][3]uint32{{0, 1, 2}, {0, 2, 3}, {4, 5, 6}, {4, 6, 7}},
	}
}

func mustBuild(t *testing.T, mesh *Mesh, opts Options) *Occluders {
	t.Helper()
	o, err := Build(mesh, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return o
}

func wallAtX0() *Mesh {
	return quad(
		[3]float32{0, -1, -5},
		[3]float32{0, -1, 5},
		[3]float32{0, 3, 5},
		[3]float32{0, 3, -5},
	)
}

func TestWallBlocks(t *testing.T) {
	o := mustBuild(t, wallAtX0(), Options{CellSize: 1})

	if !o.SegmentBlocked([3]float32{-2, 1, 0}, [3]float32{2, 1, 0}) {
		t.Fatal("segment through wall must be blocked")
	}
	if o.SegmentBlocked([3]float32{-2, 1, 0}, [3]float32{-1, 1, 0}) {
		t.Fatal("segment on one side of wall must be clear")
	}
	if o.SegmentBlocked([3]float32{2, 1, -4}, [3]float32{2, 1, 4}) {
		t.Fatal("segment parallel to wall must be clear")
	}
	if !o.SegmentBlocked([3]float32{2, 1, 0}, [3]float32{-2, 1, 0}) {
		t.Fatal("reversed segment through wall must be blocked")
	}
	if o.Visible([3]float32{-2, 1, 0}, [3]float32{2, 1, 0}) {
		t.Fatal("Visible must be inverse of SegmentBlocked")
	}
}

func TestOpenSpaceClear(t *testing.T) {
	o := mustBuild(t, wallAtX0(), Options{CellSize: 1})
	if o.SegmentBlocked([3]float32{-2, 5, 0}, [3]float32{2, 5, 0}) {
		t.Fatal("segment above the wall must be clear")
	}
	if o.SegmentBlocked([3]float32{-2, 1, 20}, [3]float32{2, 1, 20}) {
		t.Fatal("segment outside level bounds must be clear")
	}
	if o.SegmentBlocked([3]float32{-20, 1, 0}, [3]float32{-10, 1, 0}) {
		t.Fatal("segment fully outside level bounds must be clear")
	}
}

func TestThinDiagonalWall(t *testing.T) {
	mesh := quad(
		[3]float32{-5, -1, -5},
		[3]float32{5, -1, 5},
		[3]float32{5, 3, 5},
		[3]float32{-5, 3, -5},
	)
	o := mustBuild(t, mesh, Options{CellSize: 1})

	if !o.SegmentBlocked([3]float32{-2, 1, 2}, [3]float32{2, 1, -2}) {
		t.Fatal("segment crossing diagonal wall must be blocked")
	}
	if o.SegmentBlocked([3]float32{-2, 1, -1}, [3]float32{-2, 1, 2}) {
		t.Fatal("segment parallel to diagonal wall must be clear")
	}
}

func TestSegmentEndpointsInsideWallCells(t *testing.T) {
	o := mustBuild(t, wallAtX0(), Options{CellSize: 1})

	if !o.SegmentBlocked([3]float32{-0.1, 1, 0}, [3]float32{5, 1, 0}) {
		t.Fatal("segment starting inside wall cell must be blocked")
	}
	if !o.SegmentBlocked([3]float32{-5, 1, 0}, [3]float32{0.1, 1, 0}) {
		t.Fatal("segment ending inside wall cell must be blocked")
	}
	if o.SegmentBlocked([3]float32{0.05, 1, 0}, [3]float32{0.05, 1, 0}) {
		t.Fatal("zero-length segment must not be blocked")
	}
	if o.SegmentBlocked([3]float32{0, 1, 0}, [3]float32{0, 1, 0}) {
		t.Fatal("zero-length segment on the wall must not be blocked")
	}
	if o.SegmentBlocked([3]float32{-0.05, 1, 0}, [3]float32{-0.05, 1, 0}) {
		t.Fatal("zero-length segment inside wall cell must not be blocked")
	}
}

func TestVerticalSegment(t *testing.T) {
	floor := quad(
		[3]float32{-5, 0, -5},
		[3]float32{5, 0, -5},
		[3]float32{5, 0, 5},
		[3]float32{-5, 0, 5},
	)
	o := mustBuild(t, floor, Options{CellSize: 1})
	if !o.SegmentBlocked([3]float32{0, -1, 0}, [3]float32{0, 1, 0}) {
		t.Fatal("vertical segment through floor must be blocked")
	}
	if o.SegmentBlocked([3]float32{0, 1, 0}, [3]float32{0, 3, 0}) {
		t.Fatal("vertical segment above floor must be clear")
	}
}

func TestMinAreaFilter(t *testing.T) {
	mesh := twoQuads(
		[3]float32{0, -1, -5},
		[3]float32{0, -1, 5},
		[3]float32{0, 3, 5},
		[3]float32{0, 3, -5},
		[3]float32{5, -0.1, -0.1},
		[3]float32{5, -0.1, 0.1},
		[3]float32{5, 0.1, 0.1},
		[3]float32{5, 0.1, -0.1},
	)
	o := mustBuild(t, mesh, Options{CellSize: 1, MinArea: 1})
	if o.SegmentBlocked([3]float32{4, 0, 0}, [3]float32{6, 0, 0}) {
		t.Fatal("filtered small triangle must not block")
	}
	o = mustBuild(t, mesh, Options{CellSize: 1})
	if !o.SegmentBlocked([3]float32{4, 0, 0}, [3]float32{6, 0, 0}) {
		t.Fatal("small triangle must block when filter disabled")
	}
}

func TestRoundTrip(t *testing.T) {
	mesh := twoQuads(
		[3]float32{0, -1, -5},
		[3]float32{0, -1, 5},
		[3]float32{0, 3, 5},
		[3]float32{0, 3, -5},
		[3]float32{10, -1, -5},
		[3]float32{10, -1, 5},
		[3]float32{10, 3, 5},
		[3]float32{10, 3, -5},
	)
	built := mustBuild(t, mesh, Options{CellSize: 2, MinArea: 0.01})
	path := filepath.Join(t.TempDir(), "level.occl")
	if err := built.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Stats() != built.Stats() {
		t.Fatalf("stats mismatch: built=%+v loaded=%+v", built.Stats(), loaded.Stats())
	}
	if int64(info.Size()) != loaded.Stats().FileBytes {
		t.Fatalf("file size mismatch: %d vs %d", info.Size(), loaded.Stats().FileBytes)
	}

	cases := []struct {
		from, to [3]float32
		blocked  bool
	}{
		{[3]float32{-2, 1, 0}, [3]float32{2, 1, 0}, true},
		{[3]float32{2, 1, 0}, [3]float32{8, 1, 0}, false},
		{[3]float32{8, 1, 0}, [3]float32{12, 1, 0}, true},
		{[3]float32{-2, 1, 20}, [3]float32{12, 1, 20}, false},
	}
	for _, c := range cases {
		if got := loaded.SegmentBlocked(c.from, c.to); got != c.blocked {
			t.Fatalf("SegmentBlocked(%v,%v)=%v want %v", c.from, c.to, got, c.blocked)
		}
		if got := built.SegmentBlocked(c.from, c.to); got != c.blocked {
			t.Fatalf("built SegmentBlocked(%v,%v)=%v want %v", c.from, c.to, got, c.blocked)
		}
	}
}

func TestLoadRejectsCorruptFiles(t *testing.T) {
	mesh := wallAtX0()
	built := mustBuild(t, mesh, Options{CellSize: 1})
	dir := t.TempDir()

	bad := filepath.Join(dir, "bad_magic.occl")
	data := make([]byte, headerSize)
	copy(data, []byte("XXXX"))
	if err := os.WriteFile(bad, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("bad magic must fail")
	}

	good := filepath.Join(dir, "good.occl")
	if err := built.Save(good); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	trunc := filepath.Join(dir, "trunc.occl")
	if err := os.WriteFile(trunc, raw[:headerSize-1], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(trunc); err == nil {
		t.Fatal("truncated file must fail")
	}

	short := filepath.Join(dir, "short.occl")
	if err := os.WriteFile(short, raw[:len(raw)-1], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(short); err == nil {
		t.Fatal("truncated cell table must fail")
	}
}

func TestConcurrentReads(t *testing.T) {
	mesh := wallAtX0()
	o := mustBuild(t, mesh, Options{CellSize: 1})
	var blocked atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				if o.SegmentBlocked([3]float32{-2, 1, 0}, [3]float32{2, 1, 0}) {
					blocked.Add(1)
				}
				o.Visible([3]float32{-2, 1, 0}, [3]float32{-1, 1, 0})
			}
		}()
	}
	wg.Wait()
	if got := blocked.Load(); got != 16000 {
		t.Fatalf("concurrent blocked count=%d want 16000", got)
	}
}

func TestZeroAllocations(t *testing.T) {
	mesh := wallAtX0()
	o := mustBuild(t, mesh, Options{CellSize: 1})
	from := [3]float32{-2, 1, 0}
	to := [3]float32{2, 1, 0}
	allocs := testing.AllocsPerRun(1000, func() {
		o.SegmentBlocked(from, to)
	})
	if allocs != 0 {
		t.Fatalf("SegmentBlocked allocates %.2f objects per call", allocs)
	}
}
