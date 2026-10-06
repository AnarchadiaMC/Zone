# Server-Side Line-of-Sight Geometry

Status: pipeline implemented and tested, **wired into gameplay and enabled by
default** since the LOS-gating wave (`los_enabled: true`, multi-sample
head/chest/pelvis, fail-open without an artifact). This document records how
level collision geometry is extracted from the live Anomaly install, how it is
converted into a compact occluder file, the binary format, the Go API in
`internal/los` and benchmark results. The gating rules, configuration and
fail-open policy live in `zone-server/docs/LOS_GATING.md`; only `l01_escape` has
a generated artifact so far.

## 1. Where the geometry lives

The live install keeps each level in a packed archive:

```
D:\Anomaly\db\levels\level_l01_escape.db0   295,730,493 bytes
D:\Anomaly\db\levels\level_zaton.db0        415,301,142 bytes
D:\Anomaly\db\levels\level_pripyat.db0      508,998,195 bytes
... (33 level packages on this install, 26,563,606 - 508,998,195 bytes each)
```

`D:\Anomaly\tools\converter.exe` (xray_re tools, "X-Ray game asset converter
(Jun 24 2015)") unpacks `.db0` archives. The extracted level folder contains,
for `l01_escape`:

| File | Size (bytes) | Role |
|------|--------------|------|
| `level.cform` | 66,087,784 | collision geometry (CDB model): terrain + static objects; this is the raycast source |
| `level.geom` | 100,982,416 | render static geometry |
| `level.geomx` | 22,351,930 | extended render geometry |
| `level` | 7,760,864 | level metadata/visuals |
| `level.ai` | 14,374,297 | AI graph |
| `level.details` | 10,286,568 | detail objects |
| `level.hom` | 56,580 | hierarchical occlusion map (render-side, not used) |

`F:\AnomalyDev\xray-monolith` (engine source) confirms the layout in
`src/xrEngine/xrLevel.h` (`hdrCFORM`) and `src/xrCDB/xr_area.cpp`
(`CObjectSpace::Load` reads `level.cform` as header + vertices + triangles).

## 2. Extraction (works)

```powershell
New-Item -ItemType Directory -Force -Path .\extract
& "D:\Anomaly\tools\converter.exe" -unpack -xdb `
    "D:\Anomaly\db\levels\level_l01_escape.db0" -dir .\extract
# -> .\extract\levels\l01_escape\level.cform
```

`-unpack` needs no `fsconverter.ltx`; the command completed in ~0.55 s and the
whole 295 MB archive was extracted. This was run for `l01_escape`; the same
command works for every `level_*.db0`.

## 3. `converter -level` OBJ dump: blocked (do not use)

The converter advertises `-level -mode maya|le|le2` for level geometry dumps.
It cannot be used with the shipped Anomaly tooling:

1. `converter.exe -level ...` first looks for `fsconverter.ltx`. The file is
   **not shipped** with Anomaly (`D:\Anomaly\tools` contains only
   `converter.exe`, `db_unpacker*.bat`, checksums):
   `can't find fsconverter.ltx` -> `can't initialize the file system`.
2. Supplying an adapted `fsconverter_soc.ltx` from
   `abramcumner/xray_re-tools` parses only if the X-Ray SDK six-field format
   (`$name$ = bool| bool| parent| subpath| mask| caption`) is kept; a naive
   `$name$ = path` file fails with `can't parse line 1`. The parser lives in
   `sources/xray_re/xr_file_system.cxx` (`parse_fs_spec`).
3. The level tool then requires `converter.ini` from `$sdk_root$` (**not
   shipped**; the upstream `etc/converter.ini` hardcodes dead paths such as
   `E:\Games\COP\gamedata\`) and `shaders_xrlc.xr` from `$game_data$`.
   `shaders_xrlc.xr` is **not present in any Anomaly `.db0`** (checked
   `configs.db0` and `levels.db0` with `-flt *xrlc*`), and without it
   `level_tools` aborts. The intermediate error observed was
   `path $game_textures$ does not exist` from `check_paths()`.

Because of this, `tools/levelgeom` parses `level.cform` directly. That is more
faithful for raycasts anyway: `cform` is exactly the collision mesh the game
itself raycasts against.

## 4. cform format (verified)

Little-endian, `#pragma pack(push,8)` (`src/xrEngine/xrLevel.h:87` in
xray-monolith; the fields below are naturally aligned, so the on-disk layout
is identical to a tighter packing):

| Offset | Type | Field |
|--------|------|-------|
| 0 | `u32` | version (4) |
| 4 | `u32` | vertex count |
| 8 | `u32` | face count |
| 12 | `f32[3]` | AABB min (x, y, z) |
| 24 | `f32[3]` | AABB max |
| 36 | `f32[3 * vc]` | vertices |
| 36 + 12*vc | `u32[4 * fc]` | faces: 3 vertex indices + material/sector word |

For `l01_escape`: version 4, 1,574,279 vertices, 2,949,775 faces,
bounds `(-833.45, -38.40, -633.75)..(766.55, 92.51, 966.38)`; the size equation
`36 + vc*12 + fc*16 == 66,087,784` holds exactly.

Full triangle soup is ~66 MB per large level, too much to ship as-is, so it is
converted into a grid-indexed occluder.

## 5. Occluder format `ZLOS` v1

Little-endian. Header is 88 bytes:

| Offset | Type | Field |
|--------|------|-------|
| 0 | `char[4]` | magic `"ZLOS"` |
| 4 | `u32` | format version (1) |
| 8 | `u32` | flags; bit 0 = quantized int16 vertices (always set in v1) |
| 12 | `u8` | index width: 2, 3 or 4 bytes |
| 13 | `u8[3]` | reserved (zero) |
| 16 | `f32[3]` | bounds min |
| 28 | `f32[3]` | bounds max |
| 40 | `f32[3]` | grid origin (equals bounds min) |
| 52 | `f32` | grid cell size (meters) |
| 56 | `u32` | grid dimension X |
| 60 | `u32` | grid dimension Z |
| 64 | `u32` | vertex count |
| 68 | `u32` | triangle count |
| 72 | `f32[3]` | quantization scale per axis (`extent / 65534`) |
| 84 | `u32` | reserved (zero) |

Then, in order:

1. **Vertices** — `vertexCount * 3` `i16`. Decode with
   `coord = boundsMin[axis] + (q + 32767) * scale[axis]`.
2. **Triangles** — `triangleCount * 3` indices, each `indexWidth` bytes,
   referencing vertices.
3. **cellStart** — `(gridX*gridZ + 1)` `u32` CSR offsets.
4. **cellTris** — `refCount` triangle indices, each `indexWidth` bytes.

Design notes:

- The grid is 2D over world X/Z. A triangle is referenced by **every** cell its
  X/Z AABB overlaps, so a ray query is complete: any triangle crossing the
  segment lies in at least one visited cell. All heights are in each cell's
  list, so multi-floor geometry and vertical rays are handled.
- Index width is chosen as the smallest of 2/3/4 bytes that fits both the
  vertex and triangle counts (l01_escape uses 3).
- Vertices are quantized per axis to int16 over the level AABB
  (l01_escape: ~2.4 cm horizontal, ~2 mm vertical precision). Unreferenced
  vertices and degenerate triangles are dropped at build time.
- The data is immutable after `Load`, so queries are lock-free and safe for
  concurrent readers.

## 6. Conversion

```powershell
go build -o levelgeom.exe ./tools/levelgeom
.\levelgeom.exe -in .\extract\levels\l01_escape\level.cform -out .\l01_escape.occl
```

Default `-cell 3.0 -min-area 0` (exact) result for `l01_escape`:

```
cform   : version=4 verts=1574279 tris=2949775 bounds=(-833.45,-38.40,-633.75)..(766.55,92.51,966.38)
faces   : input=2949775 kept=2949773 dropped=2
occluder: tris=2949773 verts=1574279 grid=534x534 refs=5277829 file=52967834 bytes (50.51 MiB, 80.1% of cform)
check   : reload ok
time    : 585ms
```

The default is exact: 2,949,773 of 2,949,775 triangles are kept; the 2 dropped
faces are zero-area degenerates (warning printed because `dropped > 0`). The
534x534 grid holds 5,277,829 cell references in **50.51 MiB** (80.1% of the
63 MB cform, 17.9% of the original 295 MB `.db0`).

Artifact note: the generated `l01_escape.occl` is 52,967,834 bytes with
SHA-256 `705d987281888bfb322994fa625bbae8b0813a07aed9fd493855e77353957263`.
The `.occl` is a build artifact derived from game data and is not committed.

`-min-area > 0` is an approximation and is **not** the default: any positive
value drops collision faces and can create server-side shoot-through gaps
(anti-cheat). For comparison, the earlier `-min-area 0.1` run kept 774,120
triangles (26.2%) and produced 18.56 MiB, but the 2.17M dropped faces made it
unsuitable for authoritative LOS.

Flags: `-cell` (grid cell size; default 3.0 m), `-min-area` (drop faces below
this area; default `0` = exact; values above 0 print an anti-cheat warning),
`-check` (reload and verify, default true). See `tools/levelgeom/README.md`.

## 7. Runtime API (`internal/los`)

```go
import "zone-online/zone-server/internal/los"

occ, err := los.Load(`levels\l01_escape.occl`)   // (*Occluders, error)
if err != nil { /* level without occluder */ }

blocked := occ.SegmentBlocked(
    [3]float32{ax, ay, az}, // attacker eye position
    [3]float32{tx, ty, tz}, // target hit position
)
visible := occ.Visible(from, to) // == !SegmentBlocked
```

- `SegmentBlocked` returns `true` when any triangle lies strictly between the
  two points (Möller–Trumbore, `t` in `(0,1)`). Endpoints touching a wall are
  not a block; zero-length segments return `false`.
- Zero allocations per query, verified by `TestZeroAllocations`; worst case is
  bounded by a 2D DDA over at most `gridX + gridZ` cells plus the triangles in
  those cells.
- Extra helpers: `Build(mesh, Options)`, `(*Occluders).Save(path)`,
  `Stats()`, `Bounds()`, `CellSize()`.
- `Load` is hardened: 256 MB file cap (stat check plus a limit reader),
  aggregate element-count caps before allocation, finite non-negative scale,
  finite positive cell size, `min <= max` bounds, consistent zero/positive
  vertex+triangle counts, monotonic cell table and in-range triangle/cell
  indices. Malformed input returns a descriptive error and never panics;
  empty occluders (0 vertices / 0 triangles) load and never block.

Memory: `Load` keeps an immutable in-RAM form, not the file. On 64-bit,
`verts` is `[]int16` (6 B/vertex), while `tris`, `cellTris` and `cellStart`
are `[]uint32` (12 B/triangle, 4 B/ref, 4 B/cell). For the default exact
`l01_escape.occl` (2,949,773 triangles, 1,574,279 vertices, 5,277,829 refs,
534x534 cells) that is about **64 MiB resident** once `Load` returns. During
`Load` the raw file buffer is still alive while `parse` allocates, so the
transient peak is file + resident, about **115 MiB**; both buffers are
independent and the file buffer is freed after return. The older
`-min-area 0.1` artifact was about 24.5 MB resident with a ~44 MB transient
peak (measured 23.4 MiB / 42.0 MiB), which the exact default supersedes.

## 8. Integration into `damage.go` (future, example only)

`ValidateAndApplyDamage` already reads `attackerPos`/`targetPos` (damage.go:121,
damage.go:130) and today has **no LOS check** (see the comment at
damage.go:218). The future flag-gated integration would look like this; this
snippet is documentation, `damage.go` is intentionally untouched.

> **Integration warning — read before enabling the flag.** The single
> eye/torso offset below is not sufficient for authoritative LOS. Without
> stance and time alignment it produces **false "blocked" rejects**: a
> crouched or prone target whose shot line clears geometry gets rejected when
> queried at standing torso height, and transforms from different ticks make
> the query test a stale line. Before `los_enabled` is turned on:
>
> 1. sample multiple body points (head, chest, pelvis) and apply the game's
>    clear/blocked rule to the set, instead of one eye and one torso point;
> 2. read stance (stand/crouch/prone) from `AnimFlags` so those sample heights
>    follow the animation state;
> 3. optionally time-align attacker and target transforms through the
>    transform ring so both ends of the query come from one consistent tick.
>
> The snippet below is the minimal shape of the call, not a shippable check.

```go
// DamageHandler fields (future):
//   los        *los.Occluders
//   losEnabled bool

// SetOccluders wires the level occluder; keep it nil until los_enabled is on.
func (dh *DamageHandler) SetOccluders(o *los.Occluders, enabled bool) {
    dh.los = o
    dh.losEnabled = enabled
}

// Inside ValidateAndApplyDamage, after the range check (~damage.go:181):
if dh.losEnabled && dh.los != nil {
    from := attackerPos
    from[1] += 1.6 // attacker eye height
    to := targetPos
    to[1] += 1.2 // target torso height
    if dh.los.SegmentBlocked(from, to) {
        return 0, false, "line of sight blocked"
    }
}
```

Wiring notes:

- One `.occl` per level; the existing cross-level gate (damage.go:138) means
  only the attacker/target level's occluder is consulted. A level manager would
  `los.Load` the matching file on level load and call `SetOccluders`.
- `los_enabled` must default to `false`; v0.5.0 behaviour is unchanged until the
  flag is deliberately turned on and the geometry is validated against live
  play. Do not enable it before the multi-sample/stance/time-alignment work in
  the integration warning above is implemented; the single-offset example
  alone will cause false LOS rejects.
- The existing range gate is 300 m. A synthetic 500 m clear query costs
  ~4.5 µs, but random full-level pairs benchmark at ~172 µs each (see section
  9); a multi-sample body check multiplies that. This is still small against
  the damage budget (400 damage/s), but the multi-sample work should budget
  ~3 queries per LOS check rather than assume the synthetic clear number.

## 9. Tests and benchmarks

```
go test ./internal/los -count=1
ok  zone-online/zone-server/internal/los

go test ./internal/los -bench Segment -benchmem -run "^$"
BenchmarkSegmentBlocked-16        99.9-120.8 ns/op   0 B/op   0 allocs/op
BenchmarkSegmentBlockedClear-16    4.51-4.71 us/op   0 B/op   0 allocs/op
BenchmarkSegmentBlockedReal-16    171729-176675 ns/op  64.2 %blocked  0 B/op  0 allocs/op
```

Tests cover: wall blocks, open space clear, thin diagonal wall, segments
starting/ending inside wall cells, zero-length segments, vertical ray through a
floor, exact grid-corner traversals and grazing/boundary rays, `-min-area`
filtering, empty occluders (zero and degenerate meshes), option sanitization,
save/load round-trip, a table-driven corrupt-file suite (bad version, flags,
index width, counts, grid, scale/cell size, bounds, non-monotonic cell table,
out-of-range triangle and cell indices), oversized-file rejection, concurrent
reads, and a zero-allocation assertion via `testing.AllocsPerRun`. A
`FuzzLoad` target exercises `Load`/`parse` on arbitrary bytes; `go test` runs
its seeds and `go test -fuzz=FuzzLoad` can search further.

`BenchmarkSegmentBlocked` is the typical case (a wall close to the segment);
`BenchmarkSegmentBlockedClear` is a 500 m clear synthetic segment crossing 167
grid cells (~27 ns/cell). `BenchmarkSegmentBlockedReal` is the real-level
number: it loads the default exact `l01_escape.occl` (`LOS_OCCL_PATH`), draws
4096 random point pairs uniformly from the level bounds, shuffles them (cold
cache, no repeated hot cell path), and reports 171,729-176,675 ns/op with
64.2% blocked on an AMD Ryzen 9 6900HX, 0 allocs/op. That is the worst-case
shape: many random pairs span hundreds of metres of open level. Typical
in-combat queries are much shorter and cost far less; the level's 300 m range
gate still bounds a single query by a few hundred microseconds.

## 10. Limitations and blockers

- **Static collision geometry only.** `level.cform` contains terrain and static
  objects. Movable/breakable objects and dynamic entities are not occluders;
  the game handles those with per-object collision models the server does not
  have. This is a wall/terrain LOS, not a full physics raycast.
- **`-min-area > 0` is an approximation and an anti-cheat risk.** The default
  is `0` (exact); any positive value drops collision faces, and a dropped face
  is a line the server considers clear that the engine may not, i.e. a
  shoot-through gap. The earlier `0.1` default dropped 2.17M of 2.95M faces on
  l01_escape and must not be used for authoritative LOS. The exact default
  costs 50.51 MiB per large level (l01_escape).
- **int16 quantization** moves vertices by at most half a quantization step
  (~1.2 cm on l01_escape). Grazing rays can disagree with the engine.
- **No heightfield compression.** Terrain triangles are stored as triangles;
  a heightfield representation could shrink files further but was out of scope.
- **converter OBJ dumping is blocked** with the shipped tooling (missing
  `fsconverter.ltx`, `converter.ini`, `shaders_xrlc.xr`; upstream
  `converter.ini` references dead absolute paths). Direct `cform` parsing in
  `tools/levelgeom` replaces it and needs no external tool after extraction.
- **Not wired into gameplay.** No `los_enabled` flag exists; `damage.go`,
  `server.go`, protocol and config were not modified.

## 11. Reproduce from scratch

```powershell
# 1. extract (any writable dir)
& "D:\Anomaly\tools\converter.exe" -unpack -xdb `
    "D:\Anomaly\db\levels\level_l01_escape.db0" -dir .\extract

# 2. convert
cd F:\AnomalyDev\zone-online\zone-server
go build -o levelgeom.exe ./tools/levelgeom
.\levelgeom.exe -in ..\..\extract\levels\l01_escape\level.cform -out .\l01_escape.occl

# 3. test
go test ./internal/los -bench Segment -benchmem -run "^$"
```
