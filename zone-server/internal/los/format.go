package los

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

const (
	fileMagic     = "ZLOS"
	fileVersion   = 1
	headerSize    = 88
	maxFileBytes  = 256 << 20
	maxVertices   = 50_000_000
	maxTriangles  = 50_000_000
	maxGridDim    = 1_000_000
	maxCells      = 1 << 26
	maxCellRefs   = 400_000_000
	flagsQuantize = 1
)

func indexWidth(n int) int {
	if n <= 0xFFFF {
		return 2
	}
	if n <= 0xFFFFFF {
		return 3
	}
	return 4
}

func isFinite32(v float32) bool {
	return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
}

func (o *Occluders) Save(path string) error {
	if o == nil {
		return errors.New("los: nil occluders")
	}
	vc := o.vertexCount()
	tc := o.triangleCount()
	width := indexWidth(maxInt(vc, tc))
	nCells := o.gridX * o.gridZ
	size := o.encodedSize()
	buf := make([]byte, size)

	copy(buf[0:4], fileMagic)
	le := binary.LittleEndian
	le.PutUint32(buf[4:], fileVersion)
	le.PutUint32(buf[8:], flagsQuantize)
	buf[12] = byte(width)
	putF32(buf[16:], o.boundsMin[0])
	putF32(buf[20:], o.boundsMin[1])
	putF32(buf[24:], o.boundsMin[2])
	putF32(buf[28:], o.boundsMax[0])
	putF32(buf[32:], o.boundsMax[1])
	putF32(buf[36:], o.boundsMax[2])
	putF32(buf[40:], o.origin[0])
	putF32(buf[44:], o.origin[1])
	putF32(buf[48:], o.origin[2])
	putF32(buf[52:], o.cellSize)
	le.PutUint32(buf[56:], uint32(o.gridX))
	le.PutUint32(buf[60:], uint32(o.gridZ))
	le.PutUint32(buf[64:], uint32(vc))
	le.PutUint32(buf[68:], uint32(tc))
	putF32(buf[72:], o.scale[0])
	putF32(buf[76:], o.scale[1])
	putF32(buf[80:], o.scale[2])

	off := headerSize
	for i := 0; i < vc*3; i++ {
		le.PutUint16(buf[off:], uint16(o.verts[i]))
		off += 2
	}
	for i := 0; i < tc*3; i++ {
		putIndex(buf[off:], o.tris[i], width)
		off += width
	}
	for i := 0; i < nCells+1; i++ {
		le.PutUint32(buf[off:], o.cellStart[i])
		off += 4
	}
	for i := 0; i < len(o.cellTris); i++ {
		putIndex(buf[off:], o.cellTris[i], width)
		off += width
	}

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return err
	}
	o.fileBytes = int64(size)
	return nil
}

func Load(path string) (*Occluders, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("los: file too large (%d bytes, limit %d)", info.Size(), int64(maxFileBytes))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("los: file too large (%d bytes, limit %d)", len(data), int64(maxFileBytes))
	}
	return parse(data)
}

func parse(data []byte) (*Occluders, error) {
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("los: file too large (%d bytes, limit %d)", len(data), int64(maxFileBytes))
	}
	if len(data) < headerSize {
		return nil, errors.New("los: file too small")
	}
	if string(data[0:4]) != fileMagic {
		return nil, errors.New("los: bad magic")
	}
	le := binary.LittleEndian
	version := le.Uint32(data[4:])
	if version != fileVersion {
		return nil, fmt.Errorf("los: unsupported version %d", version)
	}
	flags := le.Uint32(data[8:])
	if flags&flagsQuantize == 0 {
		return nil, errors.New("los: unsupported vertex encoding")
	}
	if flags&^uint32(flagsQuantize) != 0 {
		return nil, fmt.Errorf("los: unsupported flags 0x%08x", flags)
	}
	width := int(data[12])
	if width != 2 && width != 3 && width != 4 {
		return nil, fmt.Errorf("los: invalid index width %d", width)
	}

	vc := int64(le.Uint32(data[64:]))
	tc := int64(le.Uint32(data[68:]))
	if vc > maxVertices || tc > maxTriangles {
		return nil, fmt.Errorf("los: vertex/triangle count too large (verts=%d tris=%d)", vc, tc)
	}
	if (vc == 0) != (tc == 0) {
		return nil, fmt.Errorf("los: inconsistent vertex/triangle count (verts=%d tris=%d)", vc, tc)
	}
	gx := int64(le.Uint32(data[56:]))
	gz := int64(le.Uint32(data[60:]))
	if gx <= 0 || gz <= 0 || gx > maxGridDim || gz > maxGridDim {
		return nil, fmt.Errorf("los: invalid grid dimensions %dx%d", gx, gz)
	}
	nCells := gx * gz
	if nCells > maxCells {
		return nil, fmt.Errorf("los: grid too large (%dx%d)", gx, gz)
	}

	need := int64(headerSize) + vc*6 + tc*3*int64(width) + (nCells+1)*4
	if int64(len(data)) < need {
		return nil, fmt.Errorf("los: truncated file (need %d bytes, have %d)", need, len(data))
	}
	refBytes := int64(len(data)) - need
	if refBytes%int64(width) != 0 {
		return nil, errors.New("los: truncated cell reference table")
	}
	refs := refBytes / int64(width)
	if refs > maxCellRefs {
		return nil, fmt.Errorf("los: cell reference table too large (%d)", refs)
	}

	o := &Occluders{
		gridX:     int(gx),
		gridZ:     int(gz),
		fileBytes: int64(len(data)),
	}
	o.cellSize = getF32(data[52:])
	if !isFinite32(o.cellSize) || o.cellSize <= 0 {
		return nil, fmt.Errorf("los: invalid cell size %v", o.cellSize)
	}
	for a := 0; a < 3; a++ {
		o.boundsMin[a] = getF32(data[16+a*4:])
		o.boundsMax[a] = getF32(data[28+a*4:])
		o.origin[a] = getF32(data[40+a*4:])
		o.scale[a] = getF32(data[72+a*4:])
		if !isFinite32(o.boundsMin[a]) || !isFinite32(o.boundsMax[a]) || !isFinite32(o.origin[a]) {
			return nil, fmt.Errorf("los: non-finite bounds on axis %d", a)
		}
		if o.boundsMin[a] > o.boundsMax[a] {
			return nil, fmt.Errorf("los: inverted bounds on axis %d", a)
		}
		if !isFinite32(o.scale[a]) || o.scale[a] < 0 {
			return nil, fmt.Errorf("los: invalid scale on axis %d", a)
		}
	}

	off := headerSize
	o.verts = make([]int16, int(vc)*3)
	for i := range o.verts {
		o.verts[i] = int16(le.Uint16(data[off:]))
		off += 2
	}
	o.tris = make([]uint32, int(tc)*3)
	for i := range o.tris {
		idx := getIndex(data[off:], width)
		if int64(idx) >= vc {
			return nil, fmt.Errorf("los: triangle index %d out of range (verts=%d)", idx, vc)
		}
		o.tris[i] = idx
		off += width
	}
	o.cellStart = make([]uint32, int(nCells)+1)
	for i := range o.cellStart {
		o.cellStart[i] = le.Uint32(data[off:])
		off += 4
	}
	if int64(o.cellStart[0]) != 0 || int64(o.cellStart[nCells]) != refs {
		return nil, errors.New("los: inconsistent cell table")
	}
	for i := 0; i < int(nCells); i++ {
		if o.cellStart[i] > o.cellStart[i+1] {
			return nil, fmt.Errorf("los: non-monotonic cell table at cell %d", i)
		}
	}
	o.cellTris = make([]uint32, int(refs))
	for i := range o.cellTris {
		idx := getIndex(data[off:], width)
		if int64(idx) >= tc {
			return nil, fmt.Errorf("los: cell triangle index %d out of range (tris=%d)", idx, tc)
		}
		o.cellTris[i] = idx
		off += width
	}
	return o, nil
}

func putF32(b []byte, v float32) {
	binary.LittleEndian.PutUint32(b, math.Float32bits(v))
}

func getF32(b []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}

func putIndex(b []byte, v uint32, width int) {
	switch width {
	case 2:
		binary.LittleEndian.PutUint16(b, uint16(v))
	case 3:
		b[0] = byte(v)
		b[1] = byte(v >> 8)
		b[2] = byte(v >> 16)
	default:
		binary.LittleEndian.PutUint32(b, v)
	}
}

func getIndex(b []byte, width int) uint32 {
	switch width {
	case 2:
		return uint32(binary.LittleEndian.Uint16(b))
	case 3:
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	default:
		return binary.LittleEndian.Uint32(b)
	}
}
