# AI Squad Replication (Wave A + combat)

Server-side AI puppet replication for protocol v5. The client receives the same
packets as any other actor (`ENTITY_ENTER_AOI`, `ENTITY_LEAVE_AOI`,
`OpAIState`, `OpDamageNotify`) and needs no C++ changes.

## Scope

- Patrol replication plus a lightweight server-side combat FSM
  (IDLE/PATROL, ALERT, CHASE, ATTACK, DEAD). The FSM is not an X-Ray port: it
  uses the existing puppet simulation and the shared player-damage validation.
- One replicated entity per squad (the leader puppet). The seeded squads are
  conceptually three-member patrols; member-level puppets are roadmap.
- Puppets are *not* inserted into the player `SpatialGrid`. Visibility is
  computed directly against the replication radii (`ai_enter_radius_m` /
  `ai_leave_radius_m`), so the 30 Hz player snapshot path and the 48-player
  regression are untouched.

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

- **ONLINE** (any same-level player within `ai_leave_radius_m`, 2D distance):
  stepped every game tick with `dt = elapsed` clamped to 250 ms.
- **OFFLINE**: macro-stepped at 1 Hz with `dt = elapsed` clamped to 2 s.

Steering is speed-based along the generated loop: walk speed 1.5 m/s default,
run 3.0 m/s used while chasing or via `SquadManager.SetPuppetRun`. Yaw faces the
movement direction; anim is idle when stationary, walk/run by speed, attack in
the ATTACK state and death in the DEAD state.

## Replication flow (per tick)

1. Copy session positions, decide the ONLINE set from pre-step positions.
2. `SquadManager.TickPuppets` advances puppet positions.
3. For each player, compute same-level puppets inside the radius and diff
   against the per-session visibility set tracked by `aiReplication`:
   - new -> `AoIManager.NotifyEntityEnter` (idempotent, exactly-once),
   - gone -> `AoIManager.NotifyEntityLeave`.
4. Stream `OpAIState` chunks (max 32) to every player with in-range squads.

`ai_enabled: false` performs no seeding, registers no entities, and sends no AI
packets. `ai_online_radius_m` is a legacy alias; absent an explicit non-220
value, `ai_enter_radius_m` / `ai_leave_radius_m` default to 180 m / 220 m and
`ai_max_entities` caps the registered puppet squads at 64.

## Combat FSM (Wave B)

| key | default | meaning |
|---|---|---|
| `ai_combat_enabled` | `true` | master switch; `false` = patrol only |
| `ai_aggro_radius_m` | `40` | 3D acquire radius for a hostile player |
| `ai_attack_range_m` | `2.0` | melee reach (distance at which ATTACK starts) |
| `ai_attack_cooldown_ms` | `1500` | time between melee swings |
| `ai_melee_damage` | `10` | damage per swing |
| `ai_corpse_seconds` | `5` | dead puppet despawn/ENTITY_LEAVE delay |
| `ai_patrol_resume_s` | `10` | ALERT cool-down before PATROL resumes |

States:

- **IDLE/PATROL** — generated loop patrol. A hostile player inside
  `ai_aggro_radius_m` is acquired. Hostility comes from the persisted faction
  relation table (`< 0` = enemy); same faction never engages. Squads with an
  empty or `monster` faction are mutants/neutral and hostile to everyone.
- **CHASE** — steers toward the player at run speed (3.0 m/s default, clamped
  to the 250 ms online step), `anim=2`.
- **ATTACK** — inside `ai_attack_range_m`, faces the player, `anim=3`, and
  swings every `ai_attack_cooldown_ms`.
- **ALERT** — target died/left/disconnected/safe-zoned or the squad entered a
  safe zone. After `ai_patrol_resume_s`, returns to PATROL.
- **DEAD** — player damage reduced the puppet to 0 HP. `anim=4` streams for
  `ai_corpse_seconds`, then the entity is released with `ENTITY_LEAVE_AOI`.

Damage paths:

- AI -> player: the FSM emits an attack event; the game validates it through
  the **same** `DamageHandler.ValidateAndApplyDamage` as player damage (level,
  safe zone, group/faction, range, line of sight when the level has an occluder,
  sanity, budget) using a proxy session with
  `SessionID = AI entity id`, then relays `OpDamageNotify` (0x0050) to the
  victim with `AttackerID = AI entity id`.
- Player -> AI: `OpDamageNotify` with `TargetID >= 1_000_000` is routed to
  `handlePuppetDamage`. Attacker session, range <= 300 m to the puppet's
  current position, line of sight when an occluder exists, damage
  sanity/clamp and the shared rolling budget are
  validated; the amount is applied to the puppet HP and an updated
  `ENTITY_ENTER_AOI` is pushed to sessions already tracking it (the
  `OpAIState` wire entry has no health field). A lethal hit sets DEAD.
- Targets are filtered to alive, same-level players outside server safe zones;
  the attacking squad must also be outside a safe zone.
- **No loot yet** (roadmap): corpses release without dropping items.

## Tests

- `TestAI_AoIBoundariesExactlyOnce`, `TestAI_StateChunkedOnlyOnline`,
  `TestAI_OfflineMacroStepAt1Hz`, `TestAI_SeedSquadsWhenTableEmpty`,
  `TestAI_DisabledNoEntities` (game), plus protocol layout/round-trip tests and
  the existing 48-session regressions.
- Combat: `TestCombat_*` (ai package FSM) and `TestAICombat_*` (game
  integration: hostility gating, safe zones, cooldown, relay, player kills,
  corpse lifecycle, config flag, validation).

## Roadmap

Loot drops, server-side pathfinding against level geometry during chase,
per-member puppets, faction-aware AI-vs-AI combat, and persistence of puppet
health/state.
