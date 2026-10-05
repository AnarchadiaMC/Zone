# Zone — Roadmap

This document is the high-level view of what Zone does today, what lands next, and where the project is heading. The detailed production-hardening backlog — infrastructure, protocol evolution, observability and operations — lives in [docs/PRODUCTION_READINESS.md](docs/PRODUCTION_READINESS.md) and is not duplicated here.

Written against the current tree: the v0.5.0 authority release plus the hit-registration, server-authoritative AI replication, inventory v2 and line-of-sight geometry waves.

---

## Done

**Dedicated Go server.** `zone-server` is a single headless binary. The game loop runs at a configurable 30 Hz default (1–240 Hz validated), with a 64 m spatial grid, a 220 m area-of-interest radius, 8 UDP worker goroutines keyed by FNV-1a source address, a per-IP token bucket (100 pkt/s, burst 150), reliable ACK and retransmit (500 ms, 5 retries), and a Windows named-pipe admin interface (Unix socket fallback) supporting `status`, `kick`, `ban` and `broadcast`. SQLite runs in WAL mode with ten tables behind an async write-behind queue that drains on shutdown.

**Wire protocol.** A 12-byte little-endian header carries magic, protocol byte, flags, sequence, opcode and payload length. There are **30 opcodes**: 29 declared in `internal/protocol/opcodes.go` (0x0001–0x0080 with gaps excluded) plus `OpAck` (`0x0005`) in `packets.go`. Payload revisions in use: client transform v2 (appended `gvid`), server query v2 (frozen 70-byte reply), entity-enter-AoI v3 (frozen 132 bytes), variable-length group state (1 + 53 × N), damage notify (13 bytes), AI state (1 + 19 × N) and the item/container ledgers (0x007D–0x0080). `WritePacket` enforces the 1200-byte safe MTU and the uint16 payload ceiling; reliable inbound packets are ACKed immediately and replayed reliable sequences dropped.

**Seamless join, `l01_escape` only.** A new player handshakes, receives `SHOW_START`, picks a faction and loadout in the native dialog, and the server creates the character plus starter inventory before sending `LOAD_LEVEL` and `INVENTORY_SYNC`. Returning players skip the dialog. The client persists a pending-join marker across the Lua VM restart and applies faction, spawn and kit once the level loads. Only `l01_escape` is hosted; this is an engine limitation (the stock start path runs Anomaly's `all.spawn` registry and no Lua API loads an arbitrary level by name), so any level change requires a reconnect.

**Client DLL, proxy and injector.** `ZoneClient.dll` exports **22 `ZN_*` functions** consumed through LuaJIT FFI, with a background UDP thread (30 Hz transform, 1 Hz heartbeat), a lock-free SPSC receive ring (256 × 1500 bytes), identity persistence and an embedded `AssetProvisioner` for all 19 Zone gamedata files. MinHook is initialized but installs no detours: the engine links LuaJIT statically, so all interop goes through the FFI polyfill. The `version.dll` proxy is a drop-in install that forwards all 15 `VERSION.dll` exports and loads `ZoneClient.dll` from its own directory; the injector supports `--launch`, `--wait`, `--pid`, `--dll` and `--ephemeral`. All binaries use the static CRT.

**xrRazom coexistence.** Both menu buttons are preserved and a single dormancy gate (`zone_main.xrr_is_active()`, which also checks `XrrNet`) disables Zone networking, input hooks, damage hooks, AI spawning and item actions while a co-op session is live. The remaining risk is a live two-mod session test.

**Safe zones, factions and groups.** Twelve canonical cylindrical zones are seeded into SQLite and loaded at startup; the server recomputes membership from transforms and rejects damage when either side is protected, while the client holsters weapons, blocks fire and suppresses AI hostility. The vanilla relation matrix seeds SQLite and ships as a DLTX config; same-faction and same-group damage is rejected. `/invite`, `/accept`, `/decline`, `/leave` and `/group` drive mixed-faction groups (default 4 players, 60 s invites) over reliable 0x0078/0x0079/0x007A.

**Economy, stashes and world items.** Ruble balances, atomic buy/sell and tier progression are backed by SQLite. `world_stashes` CRUD is distance-, level-, owner- and passcode-gated. World items persist in `world_items` with server-assigned ids, AoI join sync, a 30 s TTL sweep (`world_item_ttl_min`, 60 min default, 0 disables) and a per-level cap (`world_item_max_per_level`, 500 default).

**Authority wave (v0.5.0).** Movement is client-authoritative by owner decision: finite, in-bounds transforms are accepted as-is, with no speed/teleport validation, no `OpPositionCorrection` (0x007B removed), no movement kicks and no lag-switch strikes. The authoritative item ledger (`OpItemAction` 0x007D / `OpItemUpdate` 0x007E) carries client-monotonic ActionIDs, idempotent replay, rate limiting and transactional drop/pickup; duplicate-pickup rejection is verified end to end by the committed suites. See [docs/AUTHORITY_MODEL.md](docs/AUTHORITY_MODEL.md).

**Hit registration end-to-end.** Hitting a player proxy sends `OpDamageNotify` (0x0050) from the client and cancels the local proxy damage (`gamedata/scripts/zone_dummy.script`). The server validates attacker session match, safe-zone immunity on either side, same faction/group, 3D range ≤ 300 m from the last known positions, damage sanity, a 150 single-hit clamp and a rolling 1 s `damage_budget_per_s` (400) budget (`damage.go`, `damage_relay_test.go`, `damage_audit.go`), relays the accepted hit to the victim only and writes audit entries. The victim's client applies the validated damage to `db.actor` via `change_health` with a safe-zone guard and a HUD notice. Not yet exercised by two live clients (see Next).

**Inventory v2.** `OpContainerAction` (0x007F) / `OpContainerUpdate` (0x0080) deposit/withdraw against `world_stashes` rowids, with owner enforcement, per-packet validation, one-transaction semantics, condition-bucket stacking, ammo-aware credits, forced inventory resync on rejection, and container validation order (known action, section, count, stash, level, range, owner, passcode). The item ledger paths (drop, pickup, consume) are wired to the client; no container UI consumes 0x007F/0x0080 yet. See [zone-server/docs/INVENTORY_V2.md](zone-server/docs/INVENTORY_V2.md).

**AI replication wave.** Four Cordon patrol squads seeded into `ai_squads` and replicated as one leader puppet per squad. Two-tier simulation: ONLINE while a same-level player is inside the leave radius (220 m default, 250 ms dt clamp), OFFLINE macro-stepped at 1 Hz (2 s dt clamp). AoI hysteresis 180 m enter / 220 m leave with exactly-once entity transitions; `OpAIState` (0x007C) streams unreliable, nearest-first, in chunks of at most 32 at a ~30 Hz ceiling. Client puppets rotate and animate; combat is explicitly out of scope. See [zone-server/docs/AI_REPLICATION.md](zone-server/docs/AI_REPLICATION.md).

**Server-side LOS geometry pipeline.** `tools/levelgeom` parses `level.cform` directly and emits a grid-indexed `ZLOS` v1 occluder; `internal/los` loads it and answers `SegmentBlocked`/`Visible` with zero allocations, a hardened loader, table-driven corrupt-file tests and a fuzz target. The real `l01_escape` artifact is exact (2,949,773 of 2,949,775 triangles), 50.51 MiB, SHA-256 recorded. The pipeline is **not wired into damage**: no `los_enabled` flag exists. See [docs/LOS_GEOMETRY.md](docs/LOS_GEOMETRY.md).

**48-player scaling.** Snapshot chunking delivers every peer exactly once, nearest-first, across packets; pooled buffers and allocation-free encode paths. The 48-session dense benchmark runs a full tick in 0.974 ms (~3% of the 33.3 ms budget) with 980 B and 1 allocation per tick.

**Player isolation.** Accounts are keyed by client UUID; each account owns one character row and its own inventory. Position, faction, health, economy tier, rank, reputation, rubles and play time live on the character row, so inventory and progression are independent per account/character. A same-account reconnect evicts the previous session.

**Validation.** `go test ./...` is green across all eight packages (264 Test functions, 1 fuzz target, 17 benchmarks, 51 test files, including per-account inventory/progression isolation suites); CI runs the Go suite with the race detector and builds the MSVC Release client. The 12-client UDP load-test harness is committed (handshakes, ~4,200 snapshots, group round-trip, drop/pickup/duplicate-pickup rejection, AI AoI, 300 malformed packets, no panics); the last recorded pass predates the movement-guard removal. All 11 Lua scripts parse under LuaJIT 2.1 and the XML configs parse clean.

---

## Next

1. **Two-client runtime verification on a real install.** Run two real clients against one server and verify proxy spawn/despawn, interpolation, visibility, safe-zone enforcement on both sides, group flows, chat, starter inventory, item drop/pickup, hit registration (mutual damage, death and safe-zone rejection), AI puppet rendering and measured latency. This is the gate for calling the current feature set stable.

2. **Hit-registration live validation (0x0050).** The end-to-end path is implemented (client report → server validation → victim apply). Exercise it with two live clients: mutual kills, persisted `characters.dead=1`, relay-to-victim-only delivery, safe-zone and friendly-fire rejection, sleeper damage and the audit rows.

3. **Server-side damage units and scaling.** Damage currently flows 1:1 from the client-reported `s_hit.power` with a 150 single-hit clamp and the rolling budget. Define armor, hit-location and weapon-damage scaling server-side before balance work.

4. **Wire LOS into damage behind a flag (with the required hardening).** Add `los_enabled` (default false), multi-sample body points, stance from `AnimFlags` and attacker/target time alignment through the transform ring, then validate against live play before defaulting it on. The integration warning in [docs/LOS_GEOMETRY.md](docs/LOS_GEOMETRY.md) lists the exact preconditions.

5. **AI combat wave.** Give puppets health, damage, hostility and loot once LOS and server-side pathfinding are ready; stream attack/death animations already defined in the protocol.

6. **Container and stash UI.** Implement the client sender/consumer for 0x007F/0x0080; the server ledger and access rules are already tested.

7. **Packaging and tagging.** Add `version.dll` plus the localization and relation configs to the root `Makefile` `dist` target, verify packaged hashes against the tag, stamp the version into binaries and `VERSION.txt`, and cut the next release tag from this tree.

---

## Backlog

The detailed hardening backlog — master-server listing, protocol min/max negotiation, per-opcode rate limits, ping-timeout kicks, entity batch updates, group persistence, server-owned respawn, backups, audit retention, join-time content hashes and anti-cheat telemetry — is maintained in [docs/PRODUCTION_READINESS.md](docs/PRODUCTION_READINESS.md).

Longer-term direction not owned by that document:

- **Faction warfare.** Contested territory with capture windows and raid events, reputation-driven perks and loadouts, persistent war state and admin overrides, with safe zones staying neutral for all factions.
