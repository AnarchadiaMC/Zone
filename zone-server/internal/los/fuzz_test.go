package los

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzLoad(f *testing.F) {
	built := mustBuild(f, wallAtX0(), Options{CellSize: 1})
	path := filepath.Join(f.TempDir(), "seed.occl")
	if err := built.Save(path); err != nil {
		f.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		f.Fatalf("ReadFile: %v", err)
	}
	f.Add(raw)
	f.Add(raw[:headerSize])
	f.Add(raw[:len(raw)-1])
	f.Add([]byte{})
	f.Add([]byte("ZLOS"))
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "fuzz.occl")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Skipf("write: %v", err)
		}
		o, err := Load(path)
		if err != nil {
			return
		}
		if o == nil {
			t.Fatal("nil occluder without error")
		}
		o.SegmentBlocked([3]float32{-2, 1, 0}, [3]float32{2, 1, 0})
	})
}
