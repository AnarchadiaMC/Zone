package los

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func validOccluderBytes(t *testing.T) ([]byte, *Occluders) {
	t.Helper()
	built := mustBuild(t, wallAtX0(), Options{CellSize: 1})
	path := filepath.Join(t.TempDir(), "valid.occl")
	if err := built.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return raw, built
}

func TestLoadRejectsCorruptHeaders(t *testing.T) {
	raw, built := validOccluderBytes(t)
	st := built.Stats()
	width := indexWidth(maxInt(st.Vertices, st.Triangles))
	vertsOff := headerSize
	trisOff := vertsOff + st.Vertices*6
	cellStartOff := trisOff + st.Triangles*3*width
	cellTrisOff := cellStartOff + (st.GridX*st.GridZ+1)*4
	le := binary.LittleEndian

	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"bad version", func(b []byte) { le.PutUint32(b[4:], fileVersion+1) }},
		{"bad flags zero", func(b []byte) { le.PutUint32(b[8:], 0) }},
		{"bad flags unknown", func(b []byte) { le.PutUint32(b[8:], flagsQuantize|0x80) }},
		{"bad index width", func(b []byte) { b[12] = 7 }},
		{"huge vertex count", func(b []byte) { le.PutUint32(b[64:], 0xFFFFFFFF) }},
		{"huge triangle count", func(b []byte) { le.PutUint32(b[68:], 0xFFFFFFFF) }},
		{"huge grid dim", func(b []byte) { le.PutUint32(b[56:], 0xFFFFFFFF) }},
		{"huge grid cells", func(b []byte) {
			le.PutUint32(b[56:], maxGridDim)
			le.PutUint32(b[60:], maxGridDim)
		}},
		{"nan scale", func(b []byte) { putF32(b[72:], float32(math.NaN())) }},
		{"negative scale", func(b []byte) { putF32(b[72:], -0.5) }},
		{"inf scale", func(b []byte) { putF32(b[76:], float32(math.Inf(1))) }},
		{"nan cell size", func(b []byte) { putF32(b[52:], float32(math.NaN())) }},
		{"zero cell size", func(b []byte) { putF32(b[52:], 0) }},
		{"inverted bounds", func(b []byte) { putF32(b[16:], 100) }},
		{"nan bounds", func(b []byte) { putF32(b[28:], float32(math.NaN())) }},
		{"missing vertex table", func(b []byte) { le.PutUint32(b[64:], 0) }},
		{"non-monotonic cell table", func(b []byte) { le.PutUint32(b[cellStartOff+4:], 0xFFFFFFFF) }},
		{"triangle index out of range", func(b []byte) { putIndex(b[trisOff:], uint32(st.Vertices), width) }},
		{"cell triangle index out of range", func(b []byte) { putIndex(b[cellTrisOff:], uint32(st.Triangles), width) }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := append([]byte(nil), raw...)
			c.mutate(data)
			path := filepath.Join(t.TempDir(), "corrupt.occl")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load accepted corrupt file")
			}
		})
	}
}

func TestLoadRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.occl")
	f, err := os.Create(path)
	if err != nil {
		t.Skipf("cannot create sparse file: %v", err)
	}
	if err := f.Truncate(maxFileBytes + 1); err != nil {
		f.Close()
		t.Skipf("cannot truncate sparse file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("oversized file must be rejected")
	}
}

func TestEmptyOccluders(t *testing.T) {
	cases := []struct {
		name string
		mesh *Mesh
	}{
		{"nil slices", &Mesh{}},
		{"vertices only", &Mesh{Vertices: [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}}},
		{"all degenerate", &Mesh{
			Vertices:  [][3]float32{{0, 0, 0}, {1, 0, 0}, {2, 0, 0}},
			Triangles: [][3]uint32{{0, 1, 2}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := Build(c.mesh, Options{CellSize: 1})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			st := o.Stats()
			if st.Vertices != 0 || st.Triangles != 0 || st.CellRefs != 0 {
				t.Fatalf("expected empty occluder, got %+v", st)
			}
			path := filepath.Join(t.TempDir(), "empty.occl")
			if err := o.Save(path); err != nil {
				t.Fatalf("Save: %v", err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if loaded.Stats() != st {
				t.Fatalf("stats mismatch: wrote %+v read %+v", st, loaded.Stats())
			}
			if loaded.SegmentBlocked([3]float32{-1, 0, -1}, [3]float32{1, 0, 1}) {
				t.Fatal("empty occluder must never block")
			}
		})
	}
}

func TestBuildSanitizesOptions(t *testing.T) {
	mesh := wallAtX0()
	for _, minArea := range []float32{float32(math.NaN()), float32(math.Inf(1)), -1} {
		o, err := Build(mesh, Options{CellSize: 1, MinArea: minArea})
		if err != nil {
			t.Fatalf("Build MinArea=%v: %v", minArea, err)
		}
		if o.Stats().Triangles != 2 {
			t.Fatalf("MinArea=%v dropped triangles, want 2", minArea)
		}
	}
}
