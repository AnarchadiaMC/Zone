package los

import (
	"errors"
	"fmt"
	"math"
)

const (
	DefaultCellSize = 3.0
	quantRange      = 65534.0
	quantBias       = 32767.0
	parallelEps     = 1e-12
	hitEps          = 1e-6
)

type Options struct {
	CellSize float32
	MinArea  float32
}

type Mesh struct {
	Vertices  [][3]float32
	Triangles [][3]uint32
}

type Stats struct {
	Vertices  int
	Triangles int
	GridX     int
	GridZ     int
	CellRefs  int
	FileBytes int64
}

type Occluders struct {
	boundsMin [3]float32
	boundsMax [3]float32
	origin    [3]float32
	cellSize  float32
	gridX     int
	gridZ     int
	scale     [3]float32
	verts     []int16
	tris      []uint32
	cellStart []uint32
	cellTris  []uint32
	fileBytes int64
}

func Build(mesh *Mesh, opts Options) (*Occluders, error) {
	if mesh == nil {
		return nil, errors.New("los: nil mesh")
	}
	nv := len(mesh.Vertices)
	nt := len(mesh.Triangles)
	if nv == 0 && nt > 0 {
		return nil, errors.New("los: mesh has triangles but no vertices")
	}

	cellSize := opts.CellSize
	if cellSize <= 0 || !isFinite32(cellSize) {
		cellSize = DefaultCellSize
	}
	minArea := float64(opts.MinArea)
	if math.IsNaN(minArea) || math.IsInf(minArea, 0) || minArea < 0 {
		minArea = 0
	}

	if nv == 0 || nt == 0 {
		return emptyOccluders(cellSize), nil
	}

	keep := make([]bool, nt)
	triRemap := make([]int32, nt)
	kept := 0
	for i := 0; i < nt; i++ {
		t := mesh.Triangles[i]
		if int(t[0]) >= nv || int(t[1]) >= nv || int(t[2]) >= nv {
			return nil, fmt.Errorf("los: triangle %d has out-of-range vertex index", i)
		}
		if triArea(mesh.Vertices[t[0]], mesh.Vertices[t[1]], mesh.Vertices[t[2]]) <= 0 {
			continue
		}
		if minArea > 0 && triArea(mesh.Vertices[t[0]], mesh.Vertices[t[1]], mesh.Vertices[t[2]]) < minArea {
			continue
		}
		keep[i] = true
		triRemap[i] = int32(kept)
		kept++
	}
	if kept == 0 {
		return emptyOccluders(cellSize), nil
	}

	min := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	max := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for i := 0; i < nt; i++ {
		if !keep[i] {
			continue
		}
		for k := 0; k < 3; k++ {
			v := mesh.Vertices[mesh.Triangles[i][k]]
			for a := 0; a < 3; a++ {
				f := float64(v[a])
				if f < min[a] {
					min[a] = f
				}
				if f > max[a] {
					max[a] = f
				}
			}
		}
	}

	remap := make([]int32, nv)
	for i := range remap {
		remap[i] = -1
	}
	usedVerts := 0
	for i := 0; i < nt; i++ {
		if !keep[i] {
			continue
		}
		for k := 0; k < 3; k++ {
			vi := mesh.Triangles[i][k]
			if remap[vi] < 0 {
				remap[vi] = int32(usedVerts)
				usedVerts++
			}
		}
	}

	ext := [3]float64{max[0] - min[0], max[1] - min[1], max[2] - min[2]}
	gx := int(math.Ceil(ext[0] / float64(cellSize)))
	gz := int(math.Ceil(ext[2] / float64(cellSize)))
	if gx < 1 {
		gx = 1
	}
	if gz < 1 {
		gz = 1
	}
	if int64(gx)*int64(gz) > 1<<26 {
		return nil, fmt.Errorf("los: grid too large (%dx%d), increase cell size", gx, gz)
	}
	nCells := gx * gz

	cellRange := func(i int) (ix0, ix1, iz0, iz1 int) {
		t := mesh.Triangles[i]
		v0 := mesh.Vertices[t[0]]
		v1 := mesh.Vertices[t[1]]
		v2 := mesh.Vertices[t[2]]
		x0 := math.Min(math.Min(float64(v0[0]), float64(v1[0])), float64(v2[0]))
		x1 := math.Max(math.Max(float64(v0[0]), float64(v1[0])), float64(v2[0]))
		z0 := math.Min(math.Min(float64(v0[2]), float64(v1[2])), float64(v2[2]))
		z1 := math.Max(math.Max(float64(v0[2]), float64(v1[2])), float64(v2[2]))
		ix0 = clampCell((x0-min[0])/float64(cellSize), gx)
		ix1 = clampCell((x1-min[0])/float64(cellSize), gx)
		iz0 = clampCell((z0-min[2])/float64(cellSize), gz)
		iz1 = clampCell((z1-min[2])/float64(cellSize), gz)
		return
	}

	counts := make([]uint32, nCells)
	var totalRefs uint64
	for i := 0; i < nt; i++ {
		if !keep[i] {
			continue
		}
		ix0, ix1, iz0, iz1 := cellRange(i)
		totalRefs += uint64(ix1-ix0+1) * uint64(iz1-iz0+1)
		for iz := iz0; iz <= iz1; iz++ {
			row := iz * gx
			for ix := ix0; ix <= ix1; ix++ {
				counts[row+ix]++
			}
		}
	}
	if totalRefs > math.MaxUint32 {
		return nil, errors.New("los: too many cell references")
	}

	cellStart := make([]uint32, nCells+1)
	var acc uint32
	for i := 0; i < nCells; i++ {
		cellStart[i] = acc
		acc += counts[i]
	}
	cellStart[nCells] = acc

	cursor := make([]uint32, nCells)
	copy(cursor, cellStart[:nCells])
	cellTris := make([]uint32, totalRefs)
	for i := 0; i < nt; i++ {
		if !keep[i] {
			continue
		}
		ci := uint32(triRemap[i])
		ix0, ix1, iz0, iz1 := cellRange(i)
		for iz := iz0; iz <= iz1; iz++ {
			row := iz * gx
			for ix := ix0; ix <= ix1; ix++ {
				idx := row + ix
				cellTris[cursor[idx]] = ci
				cursor[idx]++
			}
		}
	}

	scale := [3]float32{}
	for a := 0; a < 3; a++ {
		if ext[a] > 0 {
			scale[a] = float32(ext[a] / quantRange)
		}
	}

	verts := make([]int16, usedVerts*3)
	for old, ni := range remap {
		if ni < 0 {
			continue
		}
		v := mesh.Vertices[old]
		base := int(ni) * 3
		for a := 0; a < 3; a++ {
			if ext[a] == 0 {
				verts[base+a] = 0
				continue
			}
			q := math.Round((float64(v[a])-min[a])/ext[a]*quantRange) - quantBias
			if q > 32767 {
				q = 32767
			}
			if q < -32767 {
				q = -32767
			}
			verts[base+a] = int16(q)
		}
	}

	tris := make([]uint32, kept*3)
	w := 0
	for i := 0; i < nt; i++ {
		if !keep[i] {
			continue
		}
		t := mesh.Triangles[i]
		tris[w] = uint32(remap[t[0]])
		tris[w+1] = uint32(remap[t[1]])
		tris[w+2] = uint32(remap[t[2]])
		w += 3
	}

	o := &Occluders{
		boundsMin: [3]float32{float32(min[0]), float32(min[1]), float32(min[2])},
		boundsMax: [3]float32{float32(max[0]), float32(max[1]), float32(max[2])},
		origin:    [3]float32{float32(min[0]), float32(min[1]), float32(min[2])},
		cellSize:  cellSize,
		gridX:     gx,
		gridZ:     gz,
		scale:     scale,
		verts:     verts,
		tris:      tris,
		cellStart: cellStart,
		cellTris:  cellTris,
	}
	o.fileBytes = int64(o.encodedSize())
	return o, nil
}

func clampCell(f float64, n int) int {
	c := int(math.Floor(f))
	if c < 0 {
		return 0
	}
	if c >= n {
		return n - 1
	}
	return c
}

func triArea(a, b, c [3]float32) float64 {
	e1x := float64(b[0] - a[0])
	e1y := float64(b[1] - a[1])
	e1z := float64(b[2] - a[2])
	e2x := float64(c[0] - a[0])
	e2y := float64(c[1] - a[1])
	e2z := float64(c[2] - a[2])
	cx := e1y*e2z - e1z*e2y
	cy := e1z*e2x - e1x*e2z
	cz := e1x*e2y - e1y*e2x
	return 0.5 * math.Sqrt(cx*cx+cy*cy+cz*cz)
}

func (o *Occluders) SegmentBlocked(from, to [3]float32) bool {
	if o == nil || len(o.tris) == 0 {
		return false
	}
	dx := float64(to[0] - from[0])
	dy := float64(to[1] - from[1])
	dz := float64(to[2] - from[2])

	t0 := 0.0
	t1 := 1.0
	minX := float64(o.boundsMin[0])
	maxX := float64(o.boundsMax[0])
	minZ := float64(o.boundsMin[2])
	maxZ := float64(o.boundsMax[2])

	if dx == 0 {
		if float64(from[0]) < minX || float64(from[0]) > maxX {
			return false
		}
	} else {
		inv := 1 / dx
		ta := (minX - float64(from[0])) * inv
		tb := (maxX - float64(from[0])) * inv
		if ta > tb {
			ta, tb = tb, ta
		}
		if ta > t0 {
			t0 = ta
		}
		if tb < t1 {
			t1 = tb
		}
	}
	if dz == 0 {
		if float64(from[2]) < minZ || float64(from[2]) > maxZ {
			return false
		}
	} else {
		inv := 1 / dz
		ta := (minZ - float64(from[2])) * inv
		tb := (maxZ - float64(from[2])) * inv
		if ta > tb {
			ta, tb = tb, ta
		}
		if ta > t0 {
			t0 = ta
		}
		if tb < t1 {
			t1 = tb
		}
	}
	if t0 > t1 {
		return false
	}

	cx := clampCell((float64(from[0])+dx*t0-minX)/float64(o.cellSize), o.gridX)
	cz := clampCell((float64(from[2])+dz*t0-minZ)/float64(o.cellSize), o.gridZ)
	endX := clampCell((float64(from[0])+dx*t1-minX)/float64(o.cellSize), o.gridX)
	endZ := clampCell((float64(from[2])+dz*t1-minZ)/float64(o.cellSize), o.gridZ)

	stepX := 0
	if dx > 0 {
		stepX = 1
	} else if dx < 0 {
		stepX = -1
	}
	stepZ := 0
	if dz > 0 {
		stepZ = 1
	} else if dz < 0 {
		stepZ = -1
	}

	if stepX == 0 && stepZ == 0 {
		return o.cellBlocked(cx, cz, from, dx, dy, dz)
	}

	cell := float64(o.cellSize)
	tDeltaX := math.Inf(1)
	tMaxX := math.Inf(1)
	if stepX != 0 {
		tDeltaX = cell / math.Abs(dx)
		boundary := minX + float64(cx+maxInt(stepX, 0))*cell
		tMaxX = (boundary - float64(from[0])) / dx
	}
	tDeltaZ := math.Inf(1)
	tMaxZ := math.Inf(1)
	if stepZ != 0 {
		tDeltaZ = cell / math.Abs(dz)
		boundary := minZ + float64(cz+maxInt(stepZ, 0))*cell
		tMaxZ = (boundary - float64(from[2])) / dz
	}

	limit := o.gridX + o.gridZ + 4
	steps := 0
	for {
		if o.cellBlocked(cx, cz, from, dx, dy, dz) {
			return true
		}
		if cx == endX && cz == endZ {
			return false
		}
		if tMaxX < tMaxZ {
			tMaxX += tDeltaX
			cx += stepX
		} else {
			tMaxZ += tDeltaZ
			cz += stepZ
		}
		if cx < 0 || cx >= o.gridX || cz < 0 || cz >= o.gridZ {
			return false
		}
		steps++
		if steps >= limit {
			ddaStepGuard(steps, from, to)
			return false
		}
	}
}

var ddaStepGuard = func(steps int, from, to [3]float32) {
	panic(fmt.Sprintf("los: DDA step bound exceeded after %d steps from %v to %v; occluder grid inconsistent", steps, from, to))
}

func emptyOccluders(cellSize float32) *Occluders {
	o := &Occluders{
		cellSize:  cellSize,
		gridX:     1,
		gridZ:     1,
		cellStart: make([]uint32, 2),
	}
	o.fileBytes = int64(o.encodedSize())
	return o
}

func (o *Occluders) Visible(from, to [3]float32) bool {
	return !o.SegmentBlocked(from, to)
}

func (o *Occluders) cellBlocked(cx, cz int, from [3]float32, dx, dy, dz float64) bool {
	if cx < 0 || cx >= o.gridX || cz < 0 || cz >= o.gridZ {
		return false
	}
	idx := cz*o.gridX + cx
	start := o.cellStart[idx]
	end := o.cellStart[idx+1]
	for r := start; r < end; r++ {
		if o.triBlocked(o.cellTris[r], from, dx, dy, dz) {
			return true
		}
	}
	return false
}

func (o *Occluders) triBlocked(t uint32, from [3]float32, dx, dy, dz float64) bool {
	b := t * 3
	i0 := o.tris[b]
	i1 := o.tris[b+1]
	i2 := o.tris[b+2]

	var v0, v1, v2 [3]float64
	for a := 0; a < 3; a++ {
		sc := float64(o.scale[a])
		base := float64(o.boundsMin[a]) + quantBias*sc
		v0[a] = base + float64(o.verts[i0*3+uint32(a)])*sc
		v1[a] = base + float64(o.verts[i1*3+uint32(a)])*sc
		v2[a] = base + float64(o.verts[i2*3+uint32(a)])*sc
	}

	e1 := [3]float64{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
	e2 := [3]float64{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}

	px := dy*e2[2] - dz*e2[1]
	py := dz*e2[0] - dx*e2[2]
	pz := dx*e2[1] - dy*e2[0]

	det := e1[0]*px + e1[1]*py + e1[2]*pz
	if math.Abs(det) < parallelEps {
		return false
	}
	inv := 1 / det

	tx := float64(from[0]) - v0[0]
	ty := float64(from[1]) - v0[1]
	tz := float64(from[2]) - v0[2]

	u := (tx*px + ty*py + tz*pz) * inv
	if u < 0 || u > 1 {
		return false
	}

	qx := ty*e1[2] - tz*e1[1]
	qy := tz*e1[0] - tx*e1[2]
	qz := tx*e1[1] - ty*e1[0]

	v := (dx*qx + dy*qy + dz*qz) * inv
	if v < 0 || u+v > 1 {
		return false
	}

	tt := (e2[0]*qx + e2[1]*qy + e2[2]*qz) * inv
	return tt > hitEps && tt < 1-hitEps
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (o *Occluders) vertexCount() int {
	return len(o.verts) / 3
}

func (o *Occluders) triangleCount() int {
	return len(o.tris) / 3
}

func (o *Occluders) encodedSize() int {
	width := indexWidth(maxInt(o.vertexCount(), o.triangleCount()))
	return headerSize + o.vertexCount()*6 + o.triangleCount()*3*width + (o.gridX*o.gridZ+1)*4 + len(o.cellTris)*width
}

func (o *Occluders) Stats() Stats {
	if o == nil {
		return Stats{}
	}
	return Stats{
		Vertices:  o.vertexCount(),
		Triangles: o.triangleCount(),
		GridX:     o.gridX,
		GridZ:     o.gridZ,
		CellRefs:  len(o.cellTris),
		FileBytes: o.fileBytes,
	}
}

func (o *Occluders) Bounds() (min, max [3]float32) {
	if o == nil {
		return
	}
	return o.boundsMin, o.boundsMax
}
