package los

import (
	"math/rand"
	"os"
	"testing"
)

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

func BenchmarkSegmentBlockedReal(b *testing.B) {
	path := os.Getenv("LOS_OCCL_PATH")
	if path == "" {
		b.Skip("set LOS_OCCL_PATH to a real .occl file to run")
	}
	o, err := Load(path)
	if err != nil {
		b.Fatal(err)
	}
	min, max := o.Bounds()
	rng := rand.New(rand.NewSource(1))
	pairs := make([][2][3]float32, 4096)
	for i := range pairs {
		for s := 0; s < 2; s++ {
			pairs[i][s] = [3]float32{
				min[0] + rng.Float32()*(max[0]-min[0]),
				min[1] + rng.Float32()*(max[1]-min[1]),
				min[2] + rng.Float32()*(max[2]-min[2]),
			}
		}
	}
	rng.Shuffle(len(pairs), func(i, j int) { pairs[i], pairs[j] = pairs[j], pairs[i] })

	b.ReportAllocs()
	b.ResetTimer()
	blocked := 0
	for i := 0; i < b.N; i++ {
		p := pairs[i%len(pairs)]
		if o.SegmentBlocked(p[0], p[1]) {
			blocked++
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(blocked)/float64(b.N)*100, "%blocked")
}
