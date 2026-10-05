package main

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"zone-online/zone-server/internal/los"
)

func writeCform(t *testing.T, version uint32, vc, fc int, tris [][3]uint32) string {
	t.Helper()
	verts := make([][3]float32, vc)
	for i := range verts {
		verts[i] = [3]float32{float32(i), 0, 0}
	}
	buf := make([]byte, cformHeader+vc*12+fc*16)
	le := binary.LittleEndian
	le.PutUint32(buf[0:], version)
	le.PutUint32(buf[4:], uint32(vc))
	le.PutUint32(buf[8:], uint32(fc))
	off := cformHeader
	for _, v := range verts {
		for a := 0; a < 3; a++ {
			le.PutUint32(buf[off:], math.Float32bits(v[a]))
			off += 4
		}
	}
	for i := 0; i < fc; i++ {
		var tri [3]uint32
		if i < len(tris) {
			tri = tris[i]
		}
		for a := 0; a < 3; a++ {
			le.PutUint32(buf[off:], tri[a])
			off += 4
		}
		off += 4
	}
	path := filepath.Join(t.TempDir(), "level.cform")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadCform(t *testing.T) {
	valid := writeCform(t, cformVersion, 4, 2, [][3]uint32{{0, 1, 2}, {0, 2, 3}})
	c, err := loadCform(valid)
	if err != nil {
		t.Fatalf("loadCform valid: %v", err)
	}
	if len(c.Verts) != 4 || len(c.Tris) != 2 {
		t.Fatalf("verts=%d tris=%d want 4/2", len(c.Verts), len(c.Tris))
	}

	empty := writeCform(t, cformVersion, 0, 0, nil)
	if _, err := loadCform(empty); err != nil {
		t.Fatalf("loadCform empty: %v", err)
	}

	noFaces := writeCform(t, cformVersion, 4, 0, nil)
	if _, err := loadCform(noFaces); err != nil {
		t.Fatalf("loadCform without faces: %v", err)
	}

	noVerts := writeCform(t, cformVersion, 0, 1, [][3]uint32{{0, 0, 0}})
	if _, err := loadCform(noVerts); err == nil {
		t.Fatal("faces without vertices must fail")
	}

	badVersion := writeCform(t, cformVersion+1, 0, 0, nil)
	if _, err := loadCform(badVersion); err == nil {
		t.Fatal("bad version must fail")
	}
}

func TestEmptyCformConvertsToEmptyOccluder(t *testing.T) {
	c, err := loadCform(writeCform(t, cformVersion, 0, 0, nil))
	if err != nil {
		t.Fatalf("loadCform: %v", err)
	}
	o, err := los.Build(&los.Mesh{Vertices: c.Verts, Triangles: c.Tris}, los.Options{CellSize: 3})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	st := o.Stats()
	if st.Vertices != 0 || st.Triangles != 0 {
		t.Fatalf("expected empty occluder, got %+v", st)
	}
	path := filepath.Join(t.TempDir(), "empty.occl")
	if err := o.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := los.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
