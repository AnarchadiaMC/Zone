# Zone — Production Roadmap

This document tracks what Zone does today and the milestones required to reach a
production-ready multiplayer survival server. It is written against the current
repository state (October 2026).

---

## Current State

**Dedicated Go server.** `zone-server` is a single headless binary. It uses
SQLite in WAL mode for accounts, characters, inventory, stashes, safe zones,
audit log and AI squads, and an async write-behind queue for non-blocking
persistence. The game loop runs at a default 30 Hz (`tick_rate_hz` is
configurable), with a 64 m spatial grid, a 220 m area-of-interest radius,
per-address FNV-1a UDP worker affinity, a reliable ACK/retransmit queue, per-IP
rate limiting, transform anti-cheat validation, and a Windows named-pipe (Unix
socket fallback) admin interface supporting `status`, `kick`, `ban` and
`broadcast`.

**UDP binary protocol v2.** The wire format uses a 12-byte little-endian header
and 25 opcodes:

- Core: `0x0001`-`0x0007` (handshake request/response, disconnect, heartbeat,
  ACK, server query request/response)
- Transform and entities: `0x0010`-`0x0013` (client transform, server snapshot,
  entity enter/leave AoI)
- State: `0x0020` safe zone, `0x0030` world event, `0x0040`/`0x0041` stash
  interact/response, `0x0050` damage notify, `0x0060` chat, `0x0070` AI action
- Join and replication: `0x0071` show start, `0x0072` character select,
  `0x0073` load level, `0x0074` level change, `0x0075` player visual,
  `0x0076` inventory sync, `0x0077` error

**Seamless join, `l01_escape` only.** A new player handshakes, receives
`SHOW_START`, selects a faction and loadout in the native dialog, and the server
creates the character plus a starter inventory in SQLite before sending
`LOAD_LEVEL` and `INVENTORY_SYNC`. The client starts the stock singleplayer
level with the console `start` command, applies faction, spawn position and the
server-sent kit, and persists a pending-join marker across the Lua VM restart
that a level load triggers. Returning players skip the dialog and go directly
to their saved spawn. Only `l01_escape` is hosted today. This is an engine
limitation, not a design choice: the stock start path runs Anomaly's new-game
flow through the `all.spawn` spawn registry, which picks the start level, and no
Lua API can load an arbitrary level by name while another level is loaded
(`KERNEL:start` asserts no level is loaded, and `ChangeLevel` requires graph
vertex identifiers).

**Client-side proxy rendering.** The injected `ZoneClient.dll` (17 `ZN_*`
exports consumed through LuaJIT FFI) sends transforms at 30 Hz, heartbeats once
per second, and answers server queries synchronously for the browser. Remote
players are rendered locally as alife proxy stalkers with cubic Hermite
(Catmull-Rom) interpolation, a 100 ms jitter buffer and an 8-sample position
history. Level and visual changes are replicated through `0x0074`/`0x0075`.
Gameplay logic lives in 11 native X-Ray Lua scripts (1,127 lines in
`zone_net.script` alone, 1591 in the server browser). The server query response
and the browser's Internet/LAN/Favorites/Direct Connect tabs are implemented;
favorites and history are persisted in `%APPDATA%\zone_identity.ltx`.

**Safe zones.** Twelve canonical cylindrical zones are seeded into the
`safe_zones` table and loaded into memory at startup. The server recomputes
membership from transforms using 2D distance plus a vertical half-extent, sends
`SAFEZONE_STATE`, and rejects damage when either the attacker or the target is
inside a zone. The client enforces the same rule locally: it lowers and hides
the weapon, blocks fire and quick-use binds, hard-cancels damage involving a
protected actor, and suppresses AI hostility by wrapping
`xr_combat_ignore.is_enemy`, overriding `on_enemy_eval`, and installing a
monster enemy callback. AI suppression is client-side; the server currently
sees players only.

**Economy and stashes.** Ruble balances, atomic buy/sell transactions, tier
progression, and stash CRUD with JSON contents, ownership and passcodes are
backed by SQLite. Stash interactions are distance-, level- and passcode-gated,
but there is no client UI that consumes `STASH_RESPONSE` yet, so the feature is
backend-only.

**Verification status.** `go test ./...` is green, including the join-flow,
protocol-v2, safe-zone and damage suites. Lua scripts are syntax-checked with
LuaJIT 2.1. The MSVC Release client build is green. CI runs the Go suite with
the race detector and builds the client on Windows.

---

## Near-Term Milestones

1. **Two-client runtime verification on the live install.** Run two real
   clients against one server and verify proxy spawning and despawn, smooth
   interpolation, mutual visibility, safe-zone enforcement on both sides,
   join and reconnect (including the sleeper proxy path), starter inventory
   delivery, level filtering, chat delivery, and measured latency. This is the
   gate for calling the current feature set stable.
2. **Arbitrary level loading.** Replace the single-world assumption with
   either xrRazom-style engine support or a save-based spawn mechanism. Until
   then, a level change from in-game is refused with a user-facing message and
   the player must reconnect from the main menu.
3. **Full inventory sync.** Today only the server-to-client direction exists
   (starter kit and restored inventory, streamed in MTU-sized chunks with
   fingerprint duplicate suppression). The next step is client-to-server item
   moves, equipped slots round-tripping, containers, trader stock, and
   persistence of the resulting state. Stash contents also need a client UI
   that consumes `STASH_RESPONSE`.
4. **Server-authoritative damage validation for NPCs and AI.** Player-vs-player
   damage is already validated (level match, 300 m range, 250 damage sanity
   ceiling, 150 clamp, safe-zone immunity, combat tracking). NPC and mutant
   damage can only become authoritative once AI simulation exists on the
   server.

---

## Server-Authoritative World State

- **Entity replication (`BATCH_UPDATE 0x0014`, planned).** Replace per-entity
  `ENTITY_ENTER_AOI` broadcasts and the fixed 32-entry player snapshot with a
  consolidated batch update carrying entity deltas, priority tiers and a
  per-tick bandwidth budget. The current snapshot already serializes sparsely;
  the batch opcode extends this to AI entities, health, animation state and
  removal in one packet.
- **AI puppet control.** The server owns squad state (`ai_squads` table,
  `SquadManager`, A* pathfinding) and publishes actions through
  `AI_ACTION_EVENT`; clients render puppets. The next stage is full server-side
  simulation of NPC and mutant behavior so clients never run AI logic for
  replicated entities.
- **Tick-rate tiers (20 / 30 / 60 Hz).** `tick_rate_hz` already parameterizes
  the loop. Define per-tier behavior: 20 Hz as the low-bandwidth tier for
  movement and AoI maintenance, 30 Hz as the default, and 60 Hz as the combat
  tier for damage validation and close-range replication. Snapshot and
  retransmit budgets must be derived from the tier rather than hardcoded.
- **Deterministic simulation boundaries.** The server remains the only
  authority for health, damage, inventory, spawn positions and world events.
  Clients send inputs and intent only. Every replicated value must have a
  single writer on the server, and the simulation must be reproducible from a
  fixed timestep plus the ordered input stream for a given session.

---

## Faction Warfare (Design)

- **Nine factions.** The faction dialog exposes stalker, bandit, csky, dolg,
  freedom, killer, army, ecolog and monolith, with renegade, greh and isg
  unlockable through the `unlocked_factions` configuration. The server
  validates the faction and loadout on `CHARACTER_SELECT`.
- **Whitelist and relation matrix.** Server-side whitelist of playable
  factions and a per-pair relation matrix that drives hostility, friendly fire
  rules, shared base access and trader pricing. Client relation display already
  reads `relation_registry.community_relation`.
- **Safe zones neutral for all factions.** No player or NPC takes damage inside
  a safe zone regardless of faction, reputation or war state. This matches the
  current safe-zone rule and keeps hubs usable across the whole player base.
- **Faction reputation persistence.** Per-character reputation is stored in the
  `characters.reputation` column. Kills, quests, captures and losses adjust it;
  it drives faction-specific loadouts, trader tiers and territory permissions.
- **Territory capture and raid events.** Contested points per level with
  capture windows and raid events that temporarily lift safe-zone protections
  outside hubs. Territory state, ownership and event history live in SQLite.
- **Scoreboard and war state.** Aggregate faction score, active objective
  state and per-player contribution exposed to clients and the admin pipe.
- **Admin controls.** War-state overrides, faction lock/unlock, reputation
  adjustment and territory reset through the existing admin server.

---

## Persistence

- **Inventories.** `character_inventory` becomes the single authoritative item
  table. Every client-visible item must map to one row; moves, drops, splits
  and container transfers round-trip to the server and are persisted before the
  client acknowledges them.
- **Stashes.** World stashes keep JSON contents plus ownership and passcode.
  Add a client UI, per-stash access logging in `audit_log`, and cleanup rules
  for abandoned stashes.
- **Death and insurance rules.** Define what survives death (equipped gear,
  quest items, stash contents), how insurance claims are filed and paid, and how
  the dead flag is cleared on respawn or reconnection.
- **Character reconnection.** Reconnecting reclaims the live player (or the
  sleeper proxy if the disconnect left one), restoring position, health,
  inventory and combat state without duplicating entities.
- **Anti-dupe and idempotency.** Item grants, trades and stash transfers carry
  idempotency keys; the server enforces unique constraints and transaction
  boundaries so retries cannot duplicate currency or items. The client-side
  inventory fingerprint is a defense only; the database is the source of truth.

---

## Infrastructure

- **Distribution packaging.** The Makefile `dist` target assembles server,
  client binaries, scripts and configs. It must also copy the localization
  directories (`gamedata/configs/text/{eng,rus}`) and validate that the
  packaged DLL and scripts match the tagged version.
- **CI.** Extend the current GitHub Actions pipeline (Go tests with race
  detection, MSVC Release client build) with LuaJIT syntax checks for all
  scripts, a dist-package smoke test, and checksum generation for releases.
- **Versioning and tagging.** Keep `VERSION.txt` authoritative, tag releases,
  embed the version in the DLL and server startup log, and stamp the dist
  directory name from the tag.
- **Master server listing (optional).** The UDP `SERVER_QUERY` opcode already
  provides per-server name, map, player counts, lock state, protocol version
  and tick rate. A lightweight central HTTP listing can index community
  servers without changing the game protocol.
- **Deployment.** Windows and Linux server builds, documented firewall rules
  for UDP `:27015`, graceful shutdown, log rotation, database backup, and an
  operator runbook.

---

## Known Gaps and Risks

- **Menu-level load limitation.** A level start can only be created from the
  main menu; an in-game level change (for example, a server moving a player to
  another map) is impossible with the stock console start path and the
  available Lua engine APIs. Until engine support or save-based spawns exist,
  Zone hosts a single level (`l01_escape`) and requires a reconnect for any
  level change.
- **Single-level hosting.** All players in all factions share `l01_escape`.
  Spatial separation, questing and faction warfare design must account for this
  until arbitrary level loading lands.
- **DoT damage cannot be fully zeroed client-side.** The client nullifies
  `s_hit.power` and hard-cancels direct hits involving protected actors, but
  script-driven damage-over-time ticks can still apply in edge cases. The
  server-side damage rejection is authoritative for player-vs-player damage;
  NPC and environmental sources remain client-simulated for now.
- **Engine APIs absent for actor class spawning.** There is no engine API to
  spawn arbitrary actor classes on demand, which constrains AI puppet fidelity,
  quest actors and dynamic world content. Proxies are limited to the stalker
  sections the client can resolve.
- **Stash UI missing.** `STASH_RESPONSE` is parsed and surfaced as an error
  notice only; there is no container window for viewing or transferring items.
- **Two-client verification pending.** Most multiplayer behaviors are covered
  by unit tests and single-client runs only. Live two-client testing is the next
  required gate.
- **AI simulation not yet authoritative.** `SquadManager` and A* exist, but
  NPC and mutant behavior is not yet simulated server-side, so NPC combat
  cannot be validated by the server.
