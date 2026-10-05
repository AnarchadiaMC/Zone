# Zone Online — Project Brief & Session Refresher

This document is the single-page reference for what Zone Online is, what was decided, what exists today, and what comes next. It summarizes the full development conversation for quick reference.

---

## 1. Vision

A production-ready **dedicated multiplayer survival experience** for S.T.A.L.K.E.R. Anomaly 1.5.3 — "DayZ/Rust/Tarkov in the Zone" — where:

- A **headless, authoritative server** runs the world; players join seamlessly and play.
- **Factions matter**: out in the world, hostile factions fight (PvP and, later, AI). Every faction's hideout is a **safe zone for all players** to trade and meet.
- **Mixed-faction groups are allowed**: players can invite anyone to their party, so groups can organically form even across faction lines.
- The world is **persistent and server-authoritative**: characters, inventories, world items, stashes, positions and world events survive restarts.
- Target scale: **32–64 concurrent players**, with 48 as a concrete production goal (48-session scaling is done).
- It **coexists with xrRazom co-op**: both are installed side by side; players choose the "Zone" (MMO-style dedicated server) button or the xrRazom co-op button in the main menu. Zone goes dormant while a co-op session is active; nothing conflicts.

Guiding constraints from the project owner:

- Production quality first: no jank, no crashes, real error handling, clean UI.
- Commit often; push regularly; maintain a clear history.
- Use many **parallel sub-agents** with non-overlapping file scopes; the orchestrator coordinates and integrates.
- Compatibility must be validated against the actual engine source (xray-monolith) and the actual game binaries/assets, never assumed.

---

## 2. Architecture (as built)

| Layer | Technology | Notes |
|---|---|---|
| Server | Go (`zone-server`) | 30 Hz configurable tick, UDP binary protocol, SQLite WAL (10 tables), spatial grid 64 m, AoI 220 m, safe zones, factions, groups, admin pipe, client-authoritative movement, hit registration, item/container ledgers, AI replication, `internal/los` (available, flag off) |
| Client | C++ DLL (`ZoneClient.dll`) | 22 `ZN_*` exports, background UDP thread, ring buffer, async server query, static CRT |
| Injection | Injector exe **and** proxy DLL (`version.dll`) | Injector stages DLL + pre-provisions assets (`--launch`/`--wait`/`--pid`/`--dll`/`--ephemeral`); proxy forwards 15 VERSION exports and loads ZoneClient from game `bin` |
| Game logic | Lua scripts (11) in `gamedata/scripts` | 9,863 lines total; LuaJIT FFI binds `ZoneClient.dll`; native X-Ray UI (`CUIScriptWnd`) |
| UI | Native X-Ray XML + Lua | Server browser, faction/loadout select, chat bar, HUD panels, safe zone alerts |
| Persistence | SQLite (`zone_world.db`, WAL) | accounts, characters, character_inventory, world_items, schema_version, world_stashes, safe_zones, audit_log, faction_relations, ai_squads |
| LOS geometry | Go (`tools/levelgeom` + `internal/los`) | `level.cform` → `ZLOS` v1 `.occl`; zero-alloc raycast; not wired into damage |

Key protocol facts:

- **30 opcodes**: 29 in `internal/protocol/opcodes.go` (0x0001–0x0080 with gaps excluded) plus `OpAck` 0x0005 in `packets.go`. The legacy `OpStashInteract` (0x0040), `OpStashResponse` (0x0041) and `OpAIActionEvent` (0x0070) were removed in the cleanup pass, and `OpPositionCorrection` (0x007B) was removed with the movement guard.
- 12-byte header: magic `0x5A4F`, proto 1, flags (reliable/unreliable/compressed), sequence, opcode, payload length.
- `ClientTransform` 29 B; `ENTITY_ENTER_AOI` 132 B (v3); `SNAPSHOT` 1 + 22×N (N ≤ 32); `SAFEZONE_STATE` 33 B; `INVENTORY_SYNC` 1 + 70×N (N ≤ 16, MTU-chunked); `GROUP_STATE` 1 + 53×N (N ≤ 8); `DAMAGE_NOTIFY` 13 B (client report / server relay); `AI_STATE` 1 + 19×N (N ≤ 32); item/container ledgers 88/89/88/77 B.
- Server query 0x0006/0x0007 = 70 B frozen (name, map, players, max, mode, locked, proto, tick); game and query share UDP port 27015.

---

## 3. Features that work today

1. **Dedicated Go server** with config file (22 keys) + CLI flags (`--config`, `--port`, `--db-path`, `--tick-rate`), graceful shutdown, admin pipe, rate limiting, replay protection, ACK/retransmit, SQLite persistence.
2. **Seamless join**: connect from the native server browser → if new, faction + loadout select in-flow → server creates character, money, starter inventory → level starts. No main-menu detour. (Engine limit: hosted level is `l01_escape` until engine-level level loading exists.)
3. **Authority wave**: movement is client-authoritative by owner decision (finite, in-bounds transforms accepted; no speed/teleport validation, no 0x007B corrections, no lag-switch strikes), plus hit registration end-to-end — a client hit on a player proxy sends `OpDamageNotify` 0x0050 and cancels the local proxy damage; the server validates session match, safe-zone immunity either side, same faction/group, 3D range ≤ 300 m from last known positions, damage sanity, a 150 single-hit clamp and the rolling `damage_budget_per_s` budget, relays the accepted hit to the victim only and audits it; the victim's client applies it to `db.actor` via `change_health` with a safe-zone guard and HUD notice. The authoritative item ledger 0x007D/0x007E keeps idempotent ActionIDs and verified duplicate-pickup rejection. Hit detection itself stays client-side, damage is 1:1 with no armor scaling, and the hit path is not yet validated with two live clients (see gaps).
4. **Inventory v2 (server side)**: containers 0x007F/0x0080 deposit/withdraw, owner enforcement, per-packet access validation (no open-stash session state), atomic transactions through the shared stash manager, condition-bucket stacking, ammo semantics, forced resync on rejection; world-item persistence with AoI join sync, TTL sweep and per-level cap. No client container UI yet.
5. **Remote player proxies**: spawn only from server `ENTITY_ENTER_AOI` (v3) with the peer's real outfit visual and correct graph vertex — no generic AI NPC stand-ins; Hermite-spline interpolation with a 100 ms jitter buffer; proxy hits are reported to the server with 0x0050 and the local engine hit is cancelled.
6. **Safe zones for all factions**: server-authoritative distance checks, weapon holster/safemode, fire veto (only proven weapon binds, never while UI/chat owns input), damage immunity both directions, AI hostility suppression, HUD banner, state resent on join/level change/2 s cadence.
7. **Faction relations**: vanilla-derived matrix (DLTX config + SQLite), same-faction friendly fire off.
8. **Player groups**: `/invite`, `/accept`, `/decline`, `/leave`, `/group`; mixed factions; group HUD; group members cannot damage each other; default 4 (clamped to the 8-member wire capacity).
9. **Chat**: bottom chat bar + history, localized; server-side commands consumed before broadcast; 5 msgs/5 s per session; level-filtered.
10. **Server browser**: async UDP probes (no UI freeze), tabs (Internet/LAN/Favorites/Direct), sortable columns, filters, details pane, atomic favorites/history persistence.
11. **Persistence**: account auto-provision, character, faction, starter kit, position checkpointing (60 s + transitions), stashes, economy backend, world items, death handling.
12. **Player isolation**: one character and one inventory per account UUID; position, faction, health, economy tier, rank, reputation, rubles and play time are per character; a same-account reconnect evicts the old session.
13. **AI replication wave**: four seeded Cordon patrol squads, ONLINE/OFFLINE two-tier simulation (online while a same-level player is inside the 220 m leave radius, offline macro-stepped at 1 Hz), 180/220 m AoI hysteresis, `OpAIState` 0x007C streams capped ~30 Hz, client puppets with rotation/anims. No combat.
14. **LOS geometry pipeline**: `cform` parser + occluder builder (ZLOS v1), hardened loader + fuzz/table-driven tests, `internal/los` zero-allocation raycast. Real `l01_escape` artifact exact (2,949,773/2,949,775 triangles, 50.51 MiB, SHA-256 recorded). Not enabled.
15. **xrRazom coexistence**: both menu buttons preserved, single dormancy gate (`zone_main.xrr_is_active()`), callback/key/hook chains compose; remaining risk is a live two-mod session test.
16. **48-player scaling**: chunked nearest-first snapshots (no peer drop), pooled buffers, zero-alloc snapshot paths; 48-session dense tick 0.974 ms (~3% of the 33.3 ms budget), 980 B and 1 alloc per tick.
17. **Install paths**: `version.dll` proxy (15 forwarded exports, drop-in) and the injector exe (stages `ZoneClient.dll`, pre-provisions 19 gamedata files); static CRT everywhere.
18. **Validation tooling**: committed LuaJIT 2.1 syntax validator (`tools/check_lua_syntax.py`, run by the CI `lua-syntax` job) for all 11 scripts; XML configs parse; Go test suite (264 Test functions, 1 fuzz target, 17 benchmarks across 51 files, all eight packages green, including per-account inventory/progression isolation suites; `gofmt -l` clean); committed 12-client UDP load harness (`tools/loadtest`); byte-level protocol audits across Go/C++/Lua.

---

## 4. Engine-compatibility work (validated against xray-monolith)

Six parallel audits cross-checked every engine call, hook, and path:

- **Hooks**: LuaJIT is statically linked — the old MinHook `luaL_openlibs` detour could never install; it was removed. The real mechanism is LuaJIT FFI loading `ZoneClient.dll`.
- **Identity**: canonical `[zone_identity]` in `appdata/zone_identity.ltx`, merge-preserving writers (C++ and Lua), no more UUID churn or lost favorites.
- **Callbacks fixed to real signatures**: `actor_on_before_hit(shit, bone, flags)` vs npc/monster variants; `game.actor_lower_weapon` module; save/load veto via `flags.ret`; `actor_item_to_slot`; `CActor_Fire` chaining.
- **UI**: column sorting moved to real buttons (`CUIStatic` never emits clicks); relations file renamed for DLTX auto-merge with a direct-file fallback reader.
- **Input safety**: blocking `kWPN_FIRE` would eat left-click globally (the engine maps `MOUSE_1` to that action), so keys are consumed only when no UI or chat owns input, only for proven weapon bindings, and never under xrRazom.
- **Paths**: loose `gamedata/` overrides packed `db0`; provisioning targets are correct; the engine FS caches at init, so first-time provisioning requires a restart when injecting into an already-running game.
- **LOS geometry**: the `level.cform` layout was confirmed against `xrLevel.h` (`hdrCFORM`) and `xr_area.cpp` (`CObjectSpace::Load`), and the exact conversion is asserted by the size equation in `docs/LOS_GEOMETRY.md`.

---

## 5. Status and known gaps

**Validated / done:**

- **Proxy DLL** (`version.dll`) install path — done (shipped in v0.4.0).
- **xrRazom coexistence** — verified: both menu buttons preserved, single dormancy gate, callback/key/hook chains compose.
- **48-player scalability** — done (benchmarks above).
- **Hit registration end-to-end (v0.5+)** — client 0x0050 proxy-hit report and local cancellation, server validation (session, safe zones, friendly fire, range, sanity, 150 single-hit clamp, rolling budget), victim-only relay, client `change_health` apply with HUD notice, and audit rows. Movement is client-authoritative by owner decision: no guard, no corrections, no lag-switch strikes. Authoritative item ledger with duplicate-pickup rejection.
- **Inventory v2 server side** — containers, per-packet access validation, ownership, atomic transactions, world items (AoI/TTL/cap), ammo semantics; committed suites green.
- **AI replication** — seeded squads, two-tier simulation, hysteresis, state streams, client puppets; no combat.
- **LOS pipeline** — implemented and tested; not wired into damage.
- **Player isolation** — per-account/character inventory and progression, independent and enforced.
- **Movement-guard removal and hit-registration wave (this pass)** — deleted `movement_guard.go`, `anticheat.go` and `transform_history.go`, removed opcode `0x007B` (opcode count now 30: 29 in `opcodes.go` + `OpAck`) and the eight movement/lag-switch config keys (22 keys in `config.go`, 19 set by the shipped YAML), and implemented hit registration client→server→victim. The earlier cleanup pass had already removed opcodes 0x0040/0x0041/0x0070, `correction_tolerance_m`, the stash open-state gate and dead files. Player isolation is verified by committed suites; the 12-client harness and Lua validator are committed; `gofmt -l` is clean.
- **Validation results** — `go test ./...` green (264 Test functions, 17 benchmarks, 51 files, including isolation suites); `gofmt -l` clean; 48-session dense tick 0.974 ms, 980 B, 1 alloc; 12-client UDP load harness committed at `tools/loadtest` (last recorded pass predates the movement-guard removal: ~4,200 snapshots, group round-trip, drop/pickup/dupe rejection, AI AoI, 300 malformed packets, no panics); LuaJIT 2.1 syntax validator committed and run by CI; XML parse clean; MSVC Release builds clean.

**Still open:**

- **Live two-client runtime test** (pending: needs a second player) — the gate for calling the feature set stable.
- **Hit-registration live validation and damage model** — the path is wired end-to-end (client report, server validation and victim-only relay, client apply), but no two live clients have exercised it yet. Damage is applied 1:1 from the client-reported hit power with no server-side armor, hit-location or weapon scaling.
- **LOS not enabled** — no `los_enabled` flag; multi-sample body points, stance from `AnimFlags` and attacker/target time alignment are required before it is safe to turn on.
- **No AI combat** — puppets patrol and replicate only.
- **No container client UI** — 0x007F/0x0080 are logged as active stubs and have no client sender or consumer yet.
- **Master server** for public server lists (HTTP announce + JSON list) — absent.
- **Arbitrary level hosting** (engine limitation; needs engine support or save-based spawns).
- **Faction warfare**: territory, raids, reputation, war state.
- **Anti-cheat depth**: content hash at join, telemetry, LOS-backed damage verification. Movement is trusted by owner decision (no speed/teleport validation), so anti-cheat work targets the validated hit and item paths.
- **UI polish passes** and localization completeness.

The detailed production-hardening backlog is tracked in `docs/PRODUCTION_READINESS.md`; the high-level Done/Next/Backlog view is `ROADMAP.md`.

---

## 6. Working agreements

- Multi-agent parallelism with disjoint file scopes; orchestrator integrates and verifies.
- Every wave ends with: builds/tests green (`go test ./...`), Lua syntax validation, protocol consistency checks where touched, commit + push.
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
- `f2a1aa7` 48-player scaling, xrRazom coexistence, version.dll proxy, error surfacing
- `15585c9` v0.4.0 package with version.dll proxy and drag-and-drop install
- `a04ac0c` authority wave: movement guard, lag-switch detection, damage validation, item ledger
- `b9b995d` v0.5.0 package (authority model, item ledger, 22 client exports)
- `6f95ae2` LOS geometry pipeline, AI replication, inventory v2 (feature branch)
