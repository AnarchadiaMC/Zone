# Zone — Production Roadmap

This document tracks what Zone does today, what lands next, and the hardening
work required before a production multiplayer survival server. It is written
against the October 2026 repository state (`0.3.0`).

The backlog below is informed by four research passes completed against
Unturned (source released in 2026), Luanti/Minetest, Valve A2S, and DDNet; the
patterns borrowed from each are named at the point where they apply.

---

## Done

**Dedicated Go server.** `zone-server` is a single headless binary. The game
loop runs at a configurable 30 Hz default, with a 64 m spatial grid, a 220 m
area-of-interest radius, 8 UDP worker goroutines keyed by FNV-1a source
address, a per-IP token bucket (100 pkt/s, burst 150), reliable ACK and
retransmit (500 ms, 5 retries), transform anti-cheat validation, and a
Windows named-pipe admin interface (Unix socket fallback) supporting `status`,
`kick`, `ban`, and `broadcast`. SQLite runs in WAL mode with eight tables
(`accounts`, `characters`, `character_inventory`, `world_stashes`,
`safe_zones`, `audit_log`, `faction_relations`, `ai_squads`) behind an async
write-behind queue that drains on shutdown.

**Wire protocol.** A 12-byte little-endian header carries magic, protocol byte,
flags, sequence, opcode, and payload length. There are 28 opcodes: 27 declared
in `internal/protocol/opcodes.go` plus `OpAck` (`0x0005`) in `packets.go`.
Payload revisions in use: client transform v2 (appended `gvid`), server query
v2 (frozen 70-byte reply), entity-enter-AoI v3 (frozen 132 bytes with
`name[32]`), and the group set `0x0078`/`0x0079`/`0x007A` (`GroupState` is
variable length, 1 + 53 × N bytes). `WritePacket` enforces the 1200-byte safe
MTU and the uint16 payload ceiling; snapshots serialize sparsely; reliable
inbound packets are ACKed immediately and replayed sequences dropped.

**Seamless join, `l01_escape` only.** A new player handshakes, receives
`SHOW_START`, picks a faction and loadout in the native dialog, and the server
creates the character plus starter inventory before sending `LOAD_LEVEL` and
`INVENTORY_SYNC`. Returning players skip the dialog. The client persists a
pending-join marker across the Lua VM restart and applies faction, spawn, and
kit once the level loads. Only `l01_escape` is hosted. This is an engine
limitation: the stock start path runs Anomaly's new-game flow through the
`all.spawn` registry, and no Lua API can load an arbitrary level by name
(`KERNEL:start` asserts no level is loaded and `ChangeLevel` requires graph
vertex identifiers). Any level change currently requires a reconnect.

**Client DLL and Lua layer.** `ZoneClient.dll` exports 21 `ZN_*` functions
consumed through LuaJIT FFI, with a background UDP thread (30 Hz transform,
1 Hz heartbeat), a lock-free SPSC receive ring (256 × 1500 bytes), identity
persistence, and an `AssetProvisioner` that writes missing configs and
atomically refreshes scripts, UI XML, and localization. MinHook is
initialized at DLL startup but installs no detours: the engine links LuaJIT
statically, so the old `luaL_openlibs` hook path was removed and all Lua
interop goes through the FFI polyfill in `zone_net.script`. The injector
stages the DLL next to the game executable and, under `--launch`,
pre-provisions all 19 Zone gamedata files before engine start; with `--wait`
or `--pid` on an already-running game, first-time provisioning needs a
restart because the engine caches its filesystem mounts at init. Gameplay
logic lives in 11 native scripts (~7,460 lines total; `zone_net.script` 1,224
and the server browser 2,576). Remote players render as alife proxies with
cubic Hermite (Catmull-Rom) interpolation, a 100 ms jitter buffer, and an
8-sample history.

**Async server browser.** The native `CUIScriptWnd` browser scans through
`ZN_QueryStart`/`ZN_QueryPoll`/`ZN_QueryCancel` and never blocks the UI thread.
Results stream into rows incrementally; scan generations are aborted cleanly
when the user leaves a tab or starts a new scan; unreachable endpoints are
marked stale or offline with retry backoff. Supports sortable headers
(name/map/players/ping, with the Mode header cycling Mode → Proto → Lock),
name/map/ping filters with hide-full/empty/locked switches, a details pane
showing protocol and tick rate, atomic favorites (max 32) and history (max
20), extended keyboard navigation, and double-click-to-connect.

**Safe zones.** Twelve canonical cylindrical zones are seeded into the
`safe_zones` table and loaded at startup. The server recomputes membership
from transforms (2D distance plus vertical half-extent), sends
`SAFEZONE_STATE`, and rejects damage when either side is protected. The client
lowers and hides the weapon, blocks fire and quick-use binds, hard-cancels
damage, and suppresses client-side AI hostility. Safe zones are
faction-agnostic: protection does not depend on faction, group, or war state.

**Faction relations.** The vanilla `[communities_relations]` matrix is
embedded in the Go build, seeded into `faction_relations`, and reloaded on
startup so operator edits win. Keys are normalized (case and optional
`actor_` prefix). Values run from `-2000` (enemy) through `0` (neutral) to
`+300`/`+2000` (ally). The same matrix ships as
`gamedata/configs/mod_system_zone_faction_relations.ltx` (a DLTX
`mod_system_` file the engine auto-merges) for HUD relation colors.
Server-side damage between the same faction or the same group is rejected.

**Groups and social HUD.** `GroupManager` keeps join-ordered rosters with an
authoritative leader. Chat commands `/invite`, `/accept`, `/decline`, `/leave`,
and `/group` drive party state; reliable `GROUP_INVITE_NOTIFY` (`0x0078`),
optional binary `GROUP_RESPONSE` (`0x0079`), and `GROUP_STATE` (`0x007A`)
carry it on the wire. Groups default to 4 players (clamped to the 8-member
wire capacity), invitations expire after 60 s (swept each tick), groups may
mix factions, and the leader leaving — or a roster dropping to one — dissolves
the group. The HUD adds a nearby-players panel with relation colors, a group
roster with leader marker, invite notices, and a chat bar with fading history
(`zone_ui_chat.xml`), localized in English and Russian.

**Economy and stashes.** Ruble balances, atomic buy/sell, tier progression,
and stash CRUD with JSON contents, ownership, and passcodes are backed by
SQLite. Stash interactions are distance-, level-, and passcode-gated, but no
client UI consumes `STASH_RESPONSE` yet, so the feature is backend-only.

**Validation.** A 12-client UDP load test passed: 12 concurrent simulated
clients completed handshakes, exchanged roughly 4,200 snapshots, exercised the
group invite/accept/state round-trip, and survived 300 malformed packets with
no panics. `go test ./...` is green, including the join-flow, protocol, group,
faction-relation, safe-zone, and damage suites; CI runs the Go suite with the
race detector and builds the MSVC Release client. All 11 Lua scripts parse
under LuaJIT 2.1, the XML configs parse clean, and the Release DLL and
injector build clean.

**Engine limits we still live with.** One hosted level (`l01_escape`) until
engine-level level loading exists. No engine API to spawn arbitrary actor
classes on demand, which caps AI puppet fidelity. Script-driven damage-over-
time can still tick in edge cases client-side. Stash UI missing. NPC and
mutant combat is not server-authoritative because AI simulation does not run
server-side yet.

---

## Next

1. **Two-client runtime verification (tonight).** Run two real clients against
   one server and verify proxy spawn/despawn, smooth interpolation, mutual
   visibility, safe-zone enforcement on both sides, faction relation colors,
   group invite/accept/leave/roster, chat delivery and history, join and
   reconnect (including the sleeper proxy path), starter inventory delivery,
   level filtering, and measured latency. This is the gate for calling the
   current feature set stable.

2. **Packaging and tagging.** The `dist/zone-online-v0.3.0` package is
   assembled, but the root `Makefile` `dist` target still omits the
   localization tree (`gamedata/configs/text/{eng,rus}`) and does not verify
   that `gamedata/configs/mod_system_zone_faction_relations.ltx` made it into the
   package. Add that copy plus a package validation step that compares
   packaged DLL and script hashes against the tag, generate a checksum file,
   stamp the version into the DLL and server startup log, and keep
   `VERSION.txt` as the single source of truth for the directory name.

3. **UI polish from research P1/P2.** Apply the priority items already triaged
   from the Unturned, Luanti, A2S, and DDNet passes, scoped to presentation
   only: clearer stale/offline row states and retry affordances in the server
   browser, denser details pane (protocol/tick/mode first), keyboard-only flow
   for scan/sort/filter/connect, HUD panel layout at non-4:3 aspect ratios,
   chat history fade tuning, and nearby/group panel legibility during combat.

---

## Production Hardening Backlog

Derived from the research passes; ordered roughly by dependency.

- **Master server listing.** Add an HTTP announce from each server plus a
  `GET /list` endpoint on a lightweight central service. Reuse the existing
  70-byte `SERVER_QUERY_RES` fields (name, map, players, lock, protocol, tick)
  so the game protocol does not change. Unturned and Luanti both separate
  announce from query; keep the same split.
- **Protocol min/max negotiation and per-release version bump.** Extend the
  handshake with the client's minimum and maximum supported protocol
  revisions, let the server pick the highest common revision, and bump the
  protocol byte on every release that changes payload layouts. Reject
  out-of-range clients with the existing version `Status` and
  `ZN_GetLastError` reason.
- **Per-opcode rate limits beyond chat.** The per-IP token bucket is a blunt
  instrument. Add per-session, per-opcode buckets (transform, group response,
  stash interact, damage notify), keep chat at its dedicated 5 msgs/5 s, and
  log bucket drops for the admin channel. DDNet's per-message pacing is the
  reference.
- **Ping-timeout kick.** Kick sessions that miss a configurable number of
  consecutive heartbeats instead of relying only on the 30 s stale sweep,
  emitting the sleeper proxy path so an in-combat dropout is logged and its
  damage persisted.
- **Entity batch updates (`0x0014`).** Replace per-entity `ENTITY_ENTER_AOI`
  broadcasts and the fixed 32-entry player snapshot with a consolidated batch
  opcode carrying entity deltas, health, animation state, and removals. Add a
  per-client send budget enforced each tick and tiered interest radii (for
  example 100 m / 220 m / 400 m by entity class) so dense areas degrade
  gracefully. Luanti's active-object and DDNet's snapshot-budget models are
  the references.
- **Group persistence.** Today groups are in-memory only. Persist membership
  and leader in SQLite, add a leave delay (rejoin window) so accidental
  disconnects do not drop a player instantly, and add simple ranks
  (leader/officer/member) with invite and kick permissions.
- **Server-owned respawn and home spawn.** Move respawn out of client trust:
  the server decides respawn points, persists a home spawn per character, and
  enforces death/insurance rules before returning the player to the world.
- **Backup and restore.** Use SQLite `VACUUM INTO` for consistent online
  backups, expose an admin `backup` command, rotate timestamped copies, and
  document a restore runbook in `INSTALL.md`.
- **Audit-log retention.** Give `audit_log` a retention window (for example 30
  days) with a periodic prune job, and stop unbounded growth on high-traffic
  servers. Keep ban and admin actions indefinitely.
- **Content and protocol hash at join.** Have the handshake (or first
  post-join packet) carry a hash of the client's scripts/configs and protocol
  revision; refuse or flag mismatched installs before they desync the world.
- **Anti-cheat telemetry.** Beyond the current speed/NaN/clamp validation,
  record per-session outliers (transform rate, distance-per-tick spikes,
  damage cadence, opcode mix) into `audit_log` or a dedicated metrics table,
  surface them through the admin pipe, and keep server-side rejection
  authoritative.

---

## Faction Warfare (Long-Term)

- **Territory and raid events.** Contested points per level with capture
  windows, ownership flips, and timed raid events that temporarily lift
  safe-zone protection outside hubs. Territory state and event history belong
  in SQLite.
- **Reputation.** Per-character reputation already has a `characters`
  column; make kills, quests, captures, and losses adjust it, and gate
  faction loadouts, trader tiers, and territory permissions on it.
- **War state persistence.** Aggregate faction score, active objectives, and
  per-player contribution persisted across restarts and exposed to clients and
  the admin pipe, with admin overrides for faction lock/unlock, reputation
  adjustment, and territory reset.
- **Nine-plus factions.** The dialog exposes stalker, bandit, csky, dolg,
  freedom, killer, army, ecolog, and monolith, with renegade, greh, and isg
  unlockable; the relation matrix and validation already cover them.
- **Safe zones stay neutral.** No faction, group, or war state changes
  protection inside a safe zone.

All of the above is scoped to `l01_escape` until engine-level level loading
exists: a single map means spatial separation, questing, and territory
warfare must be designed around one world, and any multi-level plan waits on
the engine work described in **Done → Engine limits**.
