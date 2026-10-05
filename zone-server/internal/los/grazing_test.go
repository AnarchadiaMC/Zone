package los

import "testing"

func cornerMesh() *Mesh {
	return twoQuads(
		[3]float32{0, 0, 0},
		[3]float32{6, 0, 0},
		[3]float32{6, 0, 6},
		[3]float32{0, 0, 6},
		[3]float32{3, 0, 0},
		[3]float32{3, 0, 6},
		[3]float32{3, 3, 6},
		[3]float32{3, 3, 0},
	)
}

func TestExactCornerAndGrazing(t *testing.T) {
	o := mustBuild(t, cornerMesh(), Options{CellSize: 1})
	if st := o.Stats(); st.GridX != 6 || st.GridZ != 6 {
		t.Fatalf("grid=%dx%d want 6x6", st.GridX, st.GridZ)
	}

	cases := []struct {
		name    string
		from    [3]float32
		to      [3]float32
		blocked bool
	}{
		{
			"diagonal through grid corners and wall",
			[3]float32{0.5, 1, 0.5},
			[3]float32{5.5, 1, 5.5},
			true,
		},
		{
			"diagonal above wall",
			[3]float32{0.5, 5, 0.5},
			[3]float32{5.5, 5, 5.5},
			false,
		},
		{
			"along cell boundary inside wall plane",
			[3]float32{3, 1, 0.5},
			[3]float32{3, 1, 5.5},
			false,
		},
		{
			"starts on wall surface",
			[3]float32{3, 1, 3},
			[3]float32{5, 1, 3},
			false,
		},
		{
			"ends exactly on wall surface",
			[3]float32{1, 1, 1},
			[3]float32{3, 1, 3},
			false,
		},
		{
			"near wall bottom corner",
			[3]float32{2, 1, -0.5},
			[3]float32{4, 1, 1.5},
			true,
		},
		{
			"just misses wall bottom corner",
			[3]float32{2, 1, -1.1},
			[3]float32{4, 1, 0.9},
			false,
		},
		{
			"vertical near grid line through floor",
			[3]float32{3, 1, 2},
			[3]float32{3, -1, 2},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := o.SegmentBlocked(c.from, c.to); got != c.blocked {
				t.Fatalf("SegmentBlocked(%v,%v)=%v want %v", c.from, c.to, got, c.blocked)
			}
		})
	}
}
