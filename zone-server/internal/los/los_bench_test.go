package los

import "testing"

func corridorMesh(walls int) *Mesh {
	verts := make([][3]float32, 0, walls*8)
	tris := make([][3]uint32, 0, walls*4)
	for i := 0; i < walls; i++ {
		x := float32(-250 + i*10)
		base := uint32(len(verts))
		verts = append(verts,
			[3]float32{x, -1, -4},
			[3]float32{x, -1, -1},
			[3]float32{x, 3, -1},
			[3]float32{x, 3, -4},
			[3]float32{x, -1, 1},
			[3]float32{x, -1, 4},
			[3]float32{x, 3, 4},
			[3]float32{x, 3, 1},
		)
		tris = append(tris,
			[3]uint32{base, base + 1, base + 2},
			[3]uint32{base, base + 2, base + 3},
			[3]uint32{base + 4, base + 5, base + 6},
			[3]uint32{base + 4, base + 6, base + 7},
		)
	}
	return &Mesh{Vertices: verts, Triangles: tris}
}

func BenchmarkSegmentBlocked(b *testing.B) {
	o, err := Build(corridorMesh(50), Options{CellSize: 3})
	if err != nil {
		b.Fatal(err)
	}
	from := [3]float32{-249, 1.5, 2}
	to := [3]float32{249, 1.5, 2}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !o.SegmentBlocked(from, to) {
			b.Fatal("expected blocked")
		}
	}
}

func BenchmarkSegmentBlockedClear(b *testing.B) {
	o, err := Build(corridorMesh(50), Options{CellSize: 3})
	if err != nil {
		b.Fatal(err)
	}
	from := [3]float32{-249, 1.5, 0}
	to := [3]float32{249, 1.5, 0}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if o.SegmentBlocked(from, to) {
			b.Fatal("expected clear")
		}
	}
}
