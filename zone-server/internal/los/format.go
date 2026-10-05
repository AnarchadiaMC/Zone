package los

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
)

const (
	fileMagic     = "ZLOS"
	fileVersion   = 1
	headerSize    = 88
	maxVertices   = 50_000_000
	maxTriangles  = 50_000_000
	maxGridDim    = 1_000_000
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(data)
}

func parse(data []byte) (*Occluders, error) {
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
	width := int(data[12])
	if width != 2 && width != 3 && width != 4 {
		return nil, fmt.Errorf("los: invalid index width %d", width)
	}
	vc := int(le.Uint32(data[64:]))
	tc := int(le.Uint32(data[68:]))
	gx := int(le.Uint32(data[56:]))
	gz := int(le.Uint32(data[60:]))
	if vc <= 0 || tc <= 0 || vc > maxVertices || tc > maxTriangles {
		return nil, errors.New("los: invalid vertex/triangle count")
	}
	if gx <= 0 || gz <= 0 || gx > maxGridDim || gz > maxGridDim {
		return nil, errors.New("los: invalid grid dimensions")
	}
	nCells := gx * gz

	need := headerSize + vc*6 + tc*3*width + (nCells+1)*4
	if len(data) < need {
		return nil, errors.New("los: truncated file")
	}
	refBytes := len(data) - need
	if refBytes%width != 0 {
		return nil, errors.New("los: truncated cell reference table")
	}
	refs := refBytes / width
	if refs > maxCellRefs {
		return nil, errors.New("los: cell reference table too large")
	}

	o := &Occluders{
		gridX:     gx,
		gridZ:     gz,
		cellSize:  getF32(data[52:]),
		fileBytes: int64(len(data)),
	}
	for a := 0; a < 3; a++ {
		o.boundsMin[a] = getF32(data[16+a*4:])
		o.boundsMax[a] = getF32(data[28+a*4:])
		o.origin[a] = getF32(data[40+a*4:])
		o.scale[a] = getF32(data[72+a*4:])
	}
	if !(o.cellSize > 0) {
		return nil, errors.New("los: invalid cell size")
	}

	off := headerSize
	o.verts = make([]int16, vc*3)
	for i := range o.verts {
		o.verts[i] = int16(le.Uint16(data[off:]))
		off += 2
	}
	o.tris = make([]uint32, tc*3)
	for i := range o.tris {
		idx := getIndex(data[off:], width)
		if int(idx) >= vc {
			return nil, errors.New("los: triangle index out of range")
		}
		o.tris[i] = idx
		off += width
	}
	o.cellStart = make([]uint32, nCells+1)
	for i := range o.cellStart {
		o.cellStart[i] = le.Uint32(data[off:])
		off += 4
	}
	if int(o.cellStart[0]) != 0 || int(o.cellStart[nCells]) != refs {
		return nil, errors.New("los: inconsistent cell table")
	}
	for i := 0; i < nCells; i++ {
		if o.cellStart[i] > o.cellStart[i+1] {
			return nil, errors.New("los: non-monotonic cell table")
		}
	}
	o.cellTris = make([]uint32, refs)
	for i := range o.cellTris {
		idx := getIndex(data[off:], width)
		if int(idx) >= tc {
			return nil, errors.New("los: cell triangle index out of range")
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
