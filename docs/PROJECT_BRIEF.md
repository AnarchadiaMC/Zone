# Zone Online — Project Brief & Session Refresher

This document is the single-page reference for what Zone Online is, what was decided,
what exists today, and what comes next. It summarizes the full development conversation
for quick reference.

---

## 1. Vision

A production-ready **dedicated multiplayer survival experience** for S.T.A.L.K.E.R. Anomaly
1.5.3 — "DayZ/Rust/Tarkov in the Zone" — where:

- A **headless, authoritative server** runs the world; players join seamlessly and play.
- **Factions matter**: out in the world, hostile factions fight (PvP and, later, AI). Every
  faction's hideout is a **safe zone for all players** to trade and meet.
- **Mixed-faction groups are allowed**: players can invite anyone to their party, so groups
  can organically form even across faction lines.
- The world is **persistent and server-authoritative**: characters, inventories, stashes,
  positions, and world events survive restarts.
- Target scale: **32–64 concurrent players**, with 48 as a concrete production goal.
- It **coexists with xrRazom co-op**: both are installed side by side; players choose the
  "Zone" (MMO-style dedicated server) button or the xrRazom co-op button in the main menu.
  Zone goes dormant while a co-op session is active; nothing conflicts.

Guiding constraints from the project owner:

- Production quality first: no jank, no crashes, real error handling, clean UI.
- Commit often; push regularly; maintain a clear history.
- Use many **parallel sub-agents** with non-overlapping file scopes; the orchestrator
  coordinates and integrates.
- Compatibility must be validated against the actual engine source (xray-monolith) and
  the actual game binaries/assets, never assumed.

---

## 2. Architecture (as built)

| Layer | Technology | Notes |
|---|---|---|
| Server | Go (`zone-server`) | 30 Hz configurable tick, UDP binary protocol, SQLite WAL, spatial grid 64 m, AoI 220 m, safe zones, factions, groups, admin pipe |
| Client | C++ DLL (`ZoneClient.dll`) | 21 C exports (`ZN_*`), background UDP thread, ring buffer, async server query, static CRT |
| Injection | Injector exe **and** proxy DLL (`version.dll`) | Injector stages DLL + pre-provisions assets; proxy executes ZoneClient from game `bin` |
| Game logic | Lua scripts (11) in `gamedata/scripts` | LuaJIT FFI binds `ZoneClient.dll`; native X-Ray UI (`CUIScriptWnd`) |
| UI | Native X-Ray XML + Lua | Server browser, faction/loadout select, chat bar, HUD panels, safe zone alerts |
| Persistence | SQLite (`zone_world.db`, WAL) | accounts, characters, inventory, stashes, safe zones, faction relations, audit log, AI squads |

Key protocol facts (v3 revision):

- 28 opcodes total (0x0001–0x0007, 0x0010–0x0013, 0x0020, 0x0030, 0x0040/41,
  0x0050, 0x0060, 0x0070–0x007A).
- 12-byte header: magic `0x5A4F`, proto 1, flags, sequence, opcode, payload length.
- `ClientTransform` 29 B (position, yaw/pitch, velocity, anim flags, gvid).
- `ENTITY_ENTER_AOI` 132 B (entity, type, section/visual, pos, faction, health, gvid, name).
- `SNAPSHOT` 22 B per entry; `SAFEZONE_STATE` 33 B; `INVENTORY_SYNC` 70 B per item;
  group opcodes 0x0078/0x0079/0x007A.
- Server query 0x0006/0x0007 = 70 B (name, map, players, max, mode, locked, proto, tick).

---

## 3. Features that work today

1. **Dedicated Go server** with config file + CLI flags, graceful shutdown, admin pipe,
   rate limiting, replay protection, ACK/retransmit, SQLite persistence.
2. **Seamless join**: connect from the native server browser → if new, faction + loadout
   select in-flow → server creates character, money, starter inventory → level starts.
   No main-menu detour. (Engine limit: hosted level is `l01_escape` until engine-level
   level loading exists — tracked in ROADMAP.)
3. **Remote player proxies**: spawn only from server `ENTITY_ENTER_AOI` with the peer's
   real outfit visual model and correct graph vertex; Hermite-spline interpolation.
4. **Safe zones for all factions**: server-authoritative distance checks, weapon
   holster/safemode, fire veto, damage immunity both directions, AI hostility suppression,
   HUD banner, state resent on join/level change/2 s cadence.
5. **Faction relations**: vanilla-derived matrix (`mod_system_zone_faction_relations.ltx`
   + SQLite), same-faction friendly fire off.
6. **Player groups**: `/invite`, `/accept`, `/decline`, `/leave`, `/group`; mixed factions;
   group HUD; group members cannot damage each other; max 4 (configurable).
7. **Chat**: bottom chat bar + history, localized; server-side commands consumed before
   broadcast.
8. **Server browser**: async UDP probes (no UI freeze), tabs (Internet/LAN/Favorites/Direct),
   sortable columns, filters, details pane, atomic favorites/history persistence.
9. **Persistence**: account auto-provision, character, faction, starter kit, position
   checkpointing, stashes, economy backend, death handling.
10. **xrRazom coexistence scaffolding**: zone refuses to start while xrRazom co-op is
    active; script chains preserve other mods (under active verification, see below).
11. **Install paths**: injector exe (stages `ZoneClient.dll` next to game exe and
    pre-provisions 19 gamedata files), plus a `version.dll` proxy DLL for drag-and-drop
    install.
12. **Validation tooling**: LuaJIT 2.1 syntax validation for all scripts; XML validation;
    Go test suite; 12-client UDP load test; byte-level protocol audits across Go/C++/Lua.

---

## 4. Engine-compatibility work (validated against xray-monolith)

Six parallel audits cross-checked every engine call, hook, and path:

- **Hooks**: LuaJIT is statically linked — the old MinHook `luaL_openlibs` detour could
  never install; it was removed. The real mechanism is LuaJIT FFI loading `ZoneClient.dll`.
- **Identity**: canonical `[zone_identity]` in `appdata/zone_identity.ltx`, merge-preserving
  writers (C++ and Lua), no more UUID churn / lost favorites.
- **Callbacks fixed to real signatures**: `actor_on_before_hit(shit, bone, flags)` vs
  npc/monster variants; `game.actor_lower_weapon` module; save/load veto via `flags.ret`;
  `actor_item_to_slot`; `CActor_Fire` chaining.
- **UI**: column sorting moved to real buttons (`CUIStatic` never emits clicks); relations
  file renamed for DLTX auto-merge with a direct-file fallback reader.
- **Input safety**: the old regression (blocking `kWPN_FIRE` ate left-click globally because
  the engine maps `MOUSE_1` to that action) is guarded: only proven weapon bindings are
  blocked, never while UI/chat is open, chat focus is watchdog-released.
- **Paths**: loose `gamedata/` overrides packed `db0`; provisioning targets are correct;
  engine FS caches at init, so first-time provisioning requires a restart when injecting
  into an already-running game.

---

## 5. Known gaps / roadmap highlights

- Two-client **live runtime test** (pending: needs a second player).
- **Proxy DLL** (`version.dll`) install path — in progress.
- **xrRazom coexistence** full verification (both menu buttons, key/hook chains) — in progress.
- **48-player scalability**: snapshot chunking >32 neighbors, per-client send budget,
  allocation profiling, benchmarks — in progress.
- **Error handling polish**: every failure surfaced in UI/chat, no silent drops.
- **Master server** for public server lists (HTTP announce + JSON list).
- **Arbitrary level hosting** (engine limitation; needs xrRazom-style engine support or
  save-based spawns).
- **Server-authoritative AI** replication (entity batch stream 0x0014), combat validation.
- **Full inventory sync** (item moves, containers, trader stock).
- **Faction warfare**: territory, raids, reputation, war state.
- **Anti-cheat depth**: fire/damage validation, telemetry, content hash at join.
- **UI polish passes** and localization completeness.

---

## 6. Working agreements

- Multi-agent parallelism with disjoint file scopes; orchestrator integrates and verifies.
- Every wave ends with: builds/tests green, Lua syntax validation, protocol consistency
  checks where touched, commit + push.
- No claims without evidence: engine source, live assets, or a test must back it.
- Player-facing text is normal prose; internal communication is compressed.

---

## 7. Git history (high level)

- `8725d29` protocol v2: server query, level/visual sync, safezone enforcement
- `3bae603` seamless join, real player models, browser redesign
- `cef9fbe` CLI flags, docs, roadmap
- `cc7b203` v0.2.0 package
- `722c99a` factions, groups, async browser
- `3c812d0` security hardening (groups/session/nickname/damage)
- `ba25db8` engine-compat validation fixes
- `docs/PROJECT_BRIEF.md` this document; proxy DLL + coexistence + scalability waves follow
