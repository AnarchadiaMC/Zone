# levelgeom

Converts X-Ray `level.cform` collision geometry into the compact `.occl`
occluder file consumed by `zone-online/zone-server/internal/los`.

The server never needs the game engine, the `converter.exe` level pipeline or
the original `.db0` archive at runtime; it only reads the generated `.occl`.

## Build

```powershell
go build -o levelgeom.exe ./tools/levelgeom
```

## Extract level.cform from an Anomaly install

```powershell
# from a writable directory; converter.exe lives in the game's tools folder
& "D:\Anomaly\tools\converter.exe" -unpack -xdb "D:\Anomaly\db\levels\level_l01_escape.db0" -dir .\extract
# produces .\extract\levels\l01_escape\level.cform
```

## Convert

```powershell
.\levelgeom.exe -in .\extract\levels\l01_escape\level.cform -out .\l01_escape.occl
```

Example output (l01_escape, Anomaly 1.5.3 data):

```
cform   : version=4 verts=1574279 tris=2949775 bounds=(-833.45,-38.40,-633.75)..(766.55,92.51,966.38)
occluder: tris=774120 verts=515350 grid=534x534 refs=2754430 file=19463186 bytes (18.56 MiB, 29.5% of cform)
check   : reload ok
time    : 261ms
```

## Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `-in` | required | path to `level.cform` |
| `-out` | required | path of the `.occl` file to write |
| `-cell` | `3.0` | occluder grid cell size in meters; smaller = fewer triangles tested per cell, larger file |
| `-min-area` | `0.1` | triangles smaller than this (m^2) are dropped; `0` keeps every triangle (l01_escape then produces ~48 MiB) |
| `-check` | `true` | reload the written file and verify header/stats |

The `.occl` binary layout is documented in `docs/LOS_GEOMETRY.md`.
