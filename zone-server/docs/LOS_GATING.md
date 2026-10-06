# Line-of-Sight Gating

Server-side hit validation against level geometry, wired into the shared
`DamageHandler` so player-vs-player, player-vs-AI and AI-vs-player hits obey the
same gate.

## Configuration

| key | default | meaning |
|---|---|---|
| `los_enabled` | `true` | master switch; `false` allows every hit |
| `los_data_dir` | `zone_los` | directory holding `<level>.occl` files |

Files are built by `tools/levelgeom` from X-Ray `level.cform` geometry (see
`tools/levelgeom/README.md` and `docs/LOS_GEOMETRY.md` at the repository root).
The occluder for a level is loaded **on demand** on the first hit on that level
and cached for the process lifetime.

## Fail-open policy

If the file is missing, unreadable or corrupt, or the level name cannot be used
safely as a file name (only `[A-Za-z0-9_.-]`, no `..`), the hit is allowed and
one warning is logged per level. A missing occluder never blocks gameplay.

## Multi-sample check

With an occluder present, the hit is rejected only when **all three** segments
are blocked:

- attacker eye `pos + 1.6` -> victim head `pos + 1.6`
- attacker eye `pos + 1.6` -> victim chest `pos + 1.0`
- attacker eye `pos + 1.6` -> victim pelvis `pos + 0.5`

Any clear segment allows the hit. A rejected hit is audited with the reason
`no line of sight` (rate-limited per attacker like every other rejection).

## Tests

`TestLOS_TallWallRejectsHit`, `TestLOS_LowWallAllowsChestSample`,
`TestLOS_FailsOpen`, `TestLOS_DisabledAllowsDamage`,
`TestLOS_ConfigDefaults` build synthetic occluders with `internal/los`.
