package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"zone-online/zone-server/internal/los"
)

const (
	cformVersion = 4
	cformHeader  = 36
)

type cform struct {
	Version uint32
	Min     [3]float32
	Max     [3]float32
	Verts   [][3]float32
	Tris    [][3]uint32
}

func loadCform(path string) (*cform, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < cformHeader {
		return nil, errors.New("cform: file too small")
	}
	le := binary.LittleEndian
	version := le.Uint32(data[0:])
	if version != cformVersion {
		return nil, fmt.Errorf("cform: unsupported version %d (want %d)", version, cformVersion)
	}
	vc := int(le.Uint32(data[4:]))
	fc := int(le.Uint32(data[8:]))
	need := int64(cformHeader) + int64(vc)*12 + int64(fc)*16
	if int64(len(data)) != need {
		return nil, fmt.Errorf("cform: size mismatch: got %d bytes, header wants %d (verts=%d faces=%d)", len(data), need, vc, fc)
	}
	if vc == 0 && fc > 0 {
		return nil, errors.New("cform: faces reference a missing vertex table")
	}
	c := &cform{Version: version}
	for a := 0; a < 3; a++ {
		c.Min[a] = math.Float32frombits(le.Uint32(data[12+a*4:]))
		c.Max[a] = math.Float32frombits(le.Uint32(data[24+a*4:]))
	}
	off := cformHeader
	c.Verts = make([][3]float32, vc)
	for i := range c.Verts {
		c.Verts[i][0] = math.Float32frombits(le.Uint32(data[off:]))
		c.Verts[i][1] = math.Float32frombits(le.Uint32(data[off+4:]))
		c.Verts[i][2] = math.Float32frombits(le.Uint32(data[off+8:]))
		off += 12
	}
	c.Tris = make([][3]uint32, fc)
	for i := range c.Tris {
		c.Tris[i][0] = le.Uint32(data[off:])
		c.Tris[i][1] = le.Uint32(data[off+4:])
		c.Tris[i][2] = le.Uint32(data[off+8:])
		off += 16
	}
	return c, nil
}

func main() {
	in := flag.String("in", "", "input level.cform path")
	out := flag.String("out", "", "output .occl path")
	cell := flag.Float64("cell", float64(los.DefaultCellSize), "occluder grid cell size in meters")
	minArea := flag.Float64("min-area", 0, "drop triangles smaller than this area in m^2 (0 = exact, keeps all; >0 can create anti-cheat LOS gaps)")
	check := flag.Bool("check", true, "reload the written occluder and verify stats")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "levelgeom converts X-Ray level.cform collision geometry into a server occluder file.\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: levelgeom -in <level.cform> -out <level.occl> [-cell 3.0] [-min-area 0]\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Any -min-area value above 0 drops collision faces and can create server-side\nshoot-through gaps. Default is 0 (exact).\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *in == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if math.IsNaN(*minArea) || math.IsInf(*minArea, 0) || *minArea < 0 {
		fatal(fmt.Errorf("invalid -min-area %v: must be finite and >= 0", *minArea))
	}
	if *minArea > 0 {
		fmt.Fprintf(os.Stderr, "warning : -min-area=%g > 0 drops collision faces; server-side LOS can be shot through where faces were dropped (anti-cheat gap). Use -min-area 0 for exact geometry.\n", *minArea)
	}

	start := time.Now()
	c, err := loadCform(*in)
	if err != nil {
		fatal(err)
	}
	info, _ := os.Stat(*in)
	fmt.Printf("cform   : version=%d verts=%d tris=%d bounds=(%.2f,%.2f,%.2f)..(%.2f,%.2f,%.2f)\n",
		c.Version, len(c.Verts), len(c.Tris),
		c.Min[0], c.Min[1], c.Min[2], c.Max[0], c.Max[1], c.Max[2])

	mesh := &los.Mesh{Vertices: c.Verts, Triangles: c.Tris}
	o, err := los.Build(mesh, los.Options{CellSize: float32(*cell), MinArea: float32(*minArea)})
	if err != nil {
		fatal(err)
	}
	if err := o.Save(*out); err != nil {
		fatal(err)
	}
	st := o.Stats()
	dropped := len(c.Tris) - st.Triangles
	fmt.Printf("faces   : input=%d kept=%d dropped=%d\n", len(c.Tris), st.Triangles, dropped)
	if dropped > 0 {
		fmt.Fprintf(os.Stderr, "warning : %d faces dropped (degenerate or below -min-area=%g); dropped faces can create shoot-through gaps. Re-run with -min-area 0 for exact geometry.\n", dropped, *minArea)
	}
	var inBytes int64
	if info != nil {
		inBytes = info.Size()
	}
	ratio := 0.0
	if inBytes > 0 {
		ratio = float64(st.FileBytes) / float64(inBytes) * 100
	}
	fmt.Printf("occluder: tris=%d verts=%d grid=%dx%d refs=%d file=%d bytes (%.2f MiB, %.1f%% of cform)\n",
		st.Triangles, st.Vertices, st.GridX, st.GridZ, st.CellRefs, st.FileBytes,
		float64(st.FileBytes)/1048576, ratio)

	if *check {
		loaded, err := los.Load(*out)
		if err != nil {
			fatal(fmt.Errorf("reload: %w", err))
		}
		if loaded.Stats() != st {
			fatal(fmt.Errorf("reload stats mismatch: wrote %+v, read %+v", st, loaded.Stats()))
		}
		fmt.Printf("check   : reload ok\n")
	}
	fmt.Printf("time    : %s\n", time.Since(start).Round(time.Millisecond))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "levelgeom:", err)
	os.Exit(1)
}
