# AI Squad Replication (Wave A)

Server-side AI puppet replication for protocol v5. The client receives the same
packets as any other actor (`ENTITY_ENTER_AOI`, `ENTITY_LEAVE_AOI`,
`OpAIState`) and needs no C++ changes.

## Scope

- Patrol-only puppets. **No combat, no damage, no AI-vs-player logic runs in
  this wave.** `State` stays `AIStatePatrol`; the `anim` attack/death values
  exist on the wire but are never produced for puppets.
- One replicated entity per squad (the leader puppet). The seeded squads are
  conceptually three-member patrols; member-level puppets are roadmap.
- Puppets are *not* inserted into the player `SpatialGrid`. Visibility is
  computed directly against `ai_online_radius_m`, so the 30 Hz player snapshot
  path and the 48-player regression are untouched.

## Wire (frozen v5)

| Packet | Op | Dir | Flags | Payload |
|---|---|---|---|---|
| `OpAIState` | `0x007C` | S->C | unreliable | `count u8`, then per entity 19 B: `EntityID u32`, `X f32`, `Y f32`, `Z f32`, `Yaw u16` (0..65535, 0 = facing +Z, counter-clockwise), `Anim u8` (0=idle, 1=walk, 2=run, 3=attack, 4=death) |

The legacy `OpAIActionEvent` (`0x0070`) squad-state broadcast and the
non-puppet `SquadManager.SpawnSquad`/`Tick` patrol simulation were removed;
`OpAIState` is the only AI replication packet.

- Max 32 entries per packet; larger sets are chunked nearest-first exactly like
  `OpServerSnapshot`. The struct's `binary.Size` is `1 + 32*19 = 609`; the
  writer emits only `count` entries (`1 + 19*count` bytes on the wire).
- `ENTITY_ENTER_AOI` uses `EntityType 0` (AI), `Health 100`, `Gvid 0` (the
  client resolves the graph vertex from its own world data), `Name` = squad
  label, `Section` = squad section, `Faction` = squad faction.

## Entity IDs

- AI wire entity IDs are allocated from `AIEntityIDBase = 1_000_000` and
  increment per registered puppet.
- Session IDs now come from a dedicated `Server.sessionIDSeq` counter (they
  previously shared the packet-sequence counter, which reaches 1M in minutes on
  a busy server). This guarantees AI IDs cannot collide with session IDs for
  any realistic server lifetime.

## Seeding

`ai_squads` is extended (idempotent migration) with `label`, `patrol_radius`,
`walk_speed`, `run_speed`. When the table is empty and `ai_enabled` is true,
`SeedAISquads` inserts four l01_escape patrols around the Cordon rookie village
(safe-zone centre `-211.3, -20.2, -145.8`), placed so that even their full
patrol circle stays farther than the 220 m replication radius from the world
origin (which keeps DB-backed unit tests with ad-hoc origin sessions
deterministic):

| label | section | faction | spawn (x,y,z) | patrol radius |
|---|---|---|---|---|
| Cordon Patrol Alpha | `sim_default_stalker_0` | stalker | -281, -20, -189 | 32 m |
| Cordon Patrol Bravo | `sim_default_stalker_1` | stalker | -247, -21, -121 | 28 m |
| Cordon Bandit Raid | `sim_default_bandit_0` | bandit | -156, -19, -207 | 30 m |
| Cordon Bandit Outpost | `sim_default_bandit_1` | bandit | -300, -22, -110 | 40 m |

Seeding is idempotent: a non-empty table (operator-managed rows included) is
never modified. `patrol_path` is reserved for explicit waypoint JSON
(`[[x,y,z], ...]`); NULL/invalid paths generate a 12-point circular circuit at
registration time. The `is_online` column is not written in wave A.

## Simulation tiers

- **ONLINE** (any same-level player within `ai_online_radius_m`, 2D distance):
  stepped every game tick with `dt = elapsed` clamped to 250 ms.
- **OFFLINE**: macro-stepped at 1 Hz with `dt = elapsed` clamped to 2 s.

Steering is speed-based along the generated loop: walk speed 1.5 m/s default,
run 3.0 m/s available via `SquadManager.SetPuppetRun` (optional transition, not
scheduled by default). Yaw faces the movement direction; anim is idle when
stationary, walk/run by speed.

## Replication flow (per tick)

1. Copy session positions, decide the ONLINE set from pre-step positions.
2. `SquadManager.TickPuppets` advances puppet positions.
3. For each player, compute same-level puppets inside the radius and diff
   against the per-session visibility set tracked by `aiReplication`:
   - new -> `AoIManager.NotifyEntityEnter` (idempotent, exactly-once),
   - gone -> `AoIManager.NotifyEntityLeave`.
4. Stream `OpAIState` chunks (max 32) to every player with in-range squads.

`ai_enabled: false` performs no seeding, registers no entities, and sends no AI
packets. `ai_online_radius_m` defaults to 220.

## Tests

- `TestAI_AoIBoundariesExactlyOnce`, `TestAI_StateChunkedOnlyOnline`,
  `TestAI_OfflineMacroStepAt1Hz`, `TestAI_SeedSquadsWhenTableEmpty`,
  `TestAI_DisabledNoEntities` (game), plus protocol layout/round-trip tests and
  the existing 48-session regressions.

## Roadmap

Combat, damage, loot, faction hostility, per-member puppets, server-side
pathfinding against level geometry, and persistence of puppet health/state.
