# Zone Online — Production Readiness Assessment

Read-only assessment of `F:\AnomalyDev\zone-online` on branch `feature/los-inventory-ai`
(HEAD `6f95ae2`, 88 tracked files with uncommitted changes plus 9 untracked files,
+8744/−3487 vs the v0.5.0 release commit `b9b995d`; the legacy cleanup pass and the
movement-guard removal/hit-registration wave are part of this uncommitted set).
`VERSION.txt` is `0.5.0`; `dist/zone-online-v0.5.0` is assembled but predates the
AI-replication, hit-registration, LOS and inventory-v2 work on this branch.
Nothing here is implemented by this document; it is a prioritized gap list with
current-state evidence.

---

## 1. Current verified state

**What works today (evidence in tree):**

- Dedicated Go server: UDP `:27015`, 12-byte header, opcodes `0x0001`–`0x0080`
  (`internal/protocol/opcodes.go`), 30 Hz configurable tick, 8 UDP workers with
  FNV-1a address affinity (`internal/network/udp_listener.go`), per-IP token bucket
  (100 pkt/s, burst 150), reliable ACK/retransmit, SQLite WAL with 10 tables
  (`internal/database/schema.go`: accounts, characters, character_inventory,
  world_items, schema_version, world_stashes, safe_zones, audit_log,
  faction_relations, ai_squads).
- Seamless join on `l01_escape`: `SHOW_START` → native faction/loadout dialog →
  server-created character + starter inventory → `LOAD_LEVEL` + `INVENTORY_SYNC`;
  returning players go straight to the saved spawn (`internal/game/server.go:456-654`,
  `:1268-1326`).
- Movement is client-authoritative by owner decision: transforms are accepted
  (no displacement guard, no `OpPositionCorrection`, no lag-switch strikes;
  `movement_guard.go`, `anticheat.go` and `transform_history.go` deleted). Hit
  registration is implemented end to end: the client reports proxy hits
  (`0x0050`), the server validates session match, safe zones, friendly fire,
  300 m range from last known positions, sanity, a 150 single-hit clamp and the
  rolling 1 s damage budget, then relays the accepted hit to the victim only
  (`damage.go`, `damage_relay_test.go`, `damage_audit.go`), and the victim's
  client applies it via `change_health`. Authoritative item ledger
  (`0x007D`/`0x007E`) and container/stash transactions (`0x007F`/`0x0080`,
  per-packet access validation) — `docs/AUTHORITY_MODEL.md`,
  `zone-server/docs/INVENTORY_V2.md`. The legacy stash opcodes
  `0x0040`/`0x0041` and the open-state gate were removed in the cleanup pass.
- AI replication wave A: patrol-only puppets, `OpAIState` (`0x007C`), four seeded
  Cordon squads, ONLINE/OFFLINE tiers — `zone-server/docs/AI_REPLICATION.md`.
- LOS pipeline: `internal/los` (ZLOS v1 occluder), `tools/levelgeom`, benchmarks and
  fuzz target; **not wired into gameplay, no `los_enabled` flag**
  (`docs/LOS_GEOMETRY.md:1-9`, `:224-284`).
- Client: 22 `ZN_*` exports (`zone-client/src/lua/zone_bindings.cpp`), `version.dll`
  proxy + injector install paths, native server browser (async query, favorites/history),
  social HUD, safe-zone enforcement, item ledger send path (`zone_net.script:545-900`).

**Validation performed:**

- `go test ./...` passes on this tree (Go 1.26.2; 264 `Test` funcs, 17 benchmarks,
  1 fuzz target in `internal/los`, re-counted after the movement-guard removal and
  hit-registration wave). `gofmt -l` is clean across `zone-server`.
- 48-session in-process coverage: `internal/game/scale_test.go`
  (`TestSnapshotChunking_AllNeighborsDeliveredExactlyOnce`,
  `TestTick48SessionsSmoke_NoDeadlock`, `TestScale_MaxPlayersFull`) and
  `scale_bench_test.go` (`BenchmarkTick48Dense/Spread`).
- The 12-client UDP load test is now **committed** as
  `tools/loadtest/zone_loadtest.py` with `tools/loadtest/README.md`; it is
  standard-library-only and re-runnable against a live server. Last recorded run:
  12 handshakes, ~4,200 snapshots, group round-trip, drop/pickup/duplicate-pickup
  rejection, AI AoI transitions, 300 malformed packets, no panics.
- Lua validation is now committed: `tools/check_lua_syntax.py` compiles all 11
  scripts with the LuaJIT 2.1 runtime bundled by `lupa`, and the CI `lua-syntax`
  job runs it. XML configs parse clean but no XML validation tooling exists.
- CI (`.github/workflows/ci.yml`): Go tests with `-race` on push/PR to `main`;
  LuaJIT syntax validation (`lua-syntax` job); MSVC Release client build. No XML
  validation, no lint, no dist packaging, no Linux build job.

**Install paths:** drag-and-drop `version.dll` proxy (recommended) and injector
(`--launch`/`--wait`/`--pid`) with 19-file gamedata provisioning — `INSTALL.md`,
`dist/zone-online-v0.5.0/client/README.txt`.

---

## 2. Must-fix before any public server

Each item: area → why → acceptance criteria.

1. **Two-client live runtime validation.** No two real clients have ever played
   together (`ROADMAP.md` Next; `README.md` Known Limitations). → Gate for calling
   anything stable. **AC:** two clients on one server verify proxy spawn/despawn,
   interpolation, mutual visibility, safe-zone both sides, faction colors, group
   invite/accept/leave/roster, chat, reconnect + sleeper reclaim, starter kit,
   level filtering, hit registration (mutual damage, death persistence, safe-zone
   rejection), measured latency; results recorded in-repo.

2. **PvP hit registration is implemented but not validated in-game.** The client
   sends `OpDamageNotify` (`0x0050`) from the proxy hit path, cancels the local
   proxy damage and applies the relayed result (`gamedata/scripts/zone_dummy.script`),
   and the server validates and relays to the victim only (`damage.go`,
   `damage_relay_test.go`, `damage_audit.go`). No two live clients have exercised
   the path, and the server-side damage model is missing (client-reported power is
   applied 1:1 with no armor, hit-location or weapon scaling). → PvP balance and
   death behavior are unproven. **AC:** two live clients damage/kill each other;
   safe-zone and friendly-fire rejection verified both ways; kill persists
   `characters.dead=1`; damage units/scaling defined server-side.

3. **Respawn/home-spawn/death rules not built.** The only respawn is an implicit
   handshake reset of a dead character to Rookie Village at full health
   (`server.go:494-499`); no home-spawn persistence, no client respawn UI/packet,
   no death penalty or insurance. → A dead player must reconnect and gets a free
   teleport home. **AC:** server-owned respawn flow, persisted home spawn, death
   rules enforced server-side, client UI, no reconnect required.

4. **Arbitrary level hosting (engine limitation).** Only `l01_escape` is hosted;
   stock start path runs `all.spawn` and no Lua API loads a level by name
   (`ROADMAP.md`, Done — seamless join). **AC:** either a documented launch scope of one level plus
   reconnect-on-change, or engine-level level loading (xrRazom-style patch or
   save-based spawns) with a tested level-change path.

5. **Master server absent.** The Internet tab only probes favorites + history
   (`zone_ui_server_list.script:1748-1773`); there is no HTTP announce or list
   endpoint anywhere. **AC:** either announce + `GET /list` service reusing the
   70-byte `SERVER_QUERY_RES` fields, or release notes/UI explicitly state
   direct/favorites-only and the Internet tab is renamed/removed.

6. **LOS not enabled.** Pipeline exists but `los_enabled` does not; enabling the
   single-offset example produces false blocked rejects
   (`docs/LOS_GEOMETRY.md:224-284`). **AC:** multi-sample body points, stance from
   `AnimFlags`, transform time alignment, flag default off; differential test vs
   in-engine raycasts with measured false-reject rate.

7. **AI combat absent.** Puppets patrol only; no damage, no hostility, attack/death
   wire values unused (`AI_REPLICATION.md:8-16`, `ai_replication.go:69-70`). **AC:**
   either launch copy states AI is passive, or server-authoritative AI hostility +
   damage ships (depends on item 6 and per-member puppets).

8. **Client-side hit detection and trusted movement.** Hit detection stays
   client-side because the server has no collision mesh (`docs/AUTHORITY_MODEL.md`,
   Damage). Movement is not validated at all by owner decision. → Wall-shooting is
   unverifiable until LOS, and a modified client can move freely; current gates
   only bound hit range, friendly fire, sanity and rate. **AC:** LOS-backed
   verification or a written risk acceptance with monitoring.

9. **Stash/container/trader client UI missing.** Server stash/container transactions
   are complete, but `zone_net.script` logs `0x007F`/`0x0080` as active stubs with no
   container sender or consumer UI (`zone_net.script:1139-1151`), and the legacy
   stash response opcode was removed in the cleanup pass. `EconomyManager`
   (`economy.go`) is instantiated in `server.go:135` and never called outside tests;
   no trader opcode exists. **AC:** in-game stash window and trader buy/sell with
   authoritative ledger, audits, and reconciliation on rejection.

10. **Chat incomplete.** One level-filtered channel (`server.go:762-805`), no
    whisper/group channels, mentions, mute/ignore, or moderation beyond admin
    `broadcast` (`admin.go:160-168`). **AC:** channel model + mute/ignore + admin
    chat commands, or an explicit scope statement.

11. **Anti-cheat telemetry / audit review tooling.** `audit_log` is written
    (`item_ledger.go`, accepted/rejected hits in `damage_audit.go`) but there is
    no admin query/export command and no metrics. **AC:** admin audit tail/export,
    review runbook, retention policy.

12. **Ban/whitelist admin flows.** Pipe commands are `auth`, `status`, `kick`,
    `ban`, `broadcast` (`admin.go:106-172`); no unban, ban list, or whitelist. With
    `ZONE_ADMIN_TOKEN` unset, auth is a no-op (open pipe). **AC:** unban/list/whitelist,
    token mandatory for public servers, every admin action audited.

13. **Backup/restore absent.** No `VACUUM INTO`, no backup command, no retention, no
    restore runbook (grep across `zone-server` finds none). **AC:** admin `backup`
    with timestamped rotation, documented + drilled restore into a copy.

14. **Crash/restart resilience partial.** WAL is on, sessions are memory-only, no
    restart resume, groups are in-memory (`groups.go`), and UDP worker loops have no
    `recover()` (`udp_listener.go:160-174`) so a handler panic kills the process.
    Sleeper reclaim covers 30 s reconnects only. **AC:** kill-9 drill with DB
    integrity check and bounded data loss (≤ checkpoint interval), panic isolation
    per worker, documented restart behavior.

15. **Graceful shutdown / save flush unverified and racy.** On signal, `main.go:67-108`
    cancels ctx and then defers `db.Close()`; the write-queue drain (`db.go:901-926`)
    races `Close`, and no code flushes dirty sessions (up to 60 s of movement lost,
    `Tick` checkpoint at `server.go:2148-2155`). Clients receive no disconnect
    message. **AC:** shutdown flushes dirty sessions, drains the queue before
    closing the DB, sends a disconnect reason, and a restart soak proves no loss.

16. **Config validation/completeness.** `Validate()` checks only port, tick rate and
    max players (`config.go:152-168`); an invalid `log_level` silently falls back to
    info; `map_name` is not checked against `supportedLevels`; the three AI tuning
    keys (`ai_enter_radius_m`, `ai_leave_radius_m`, `ai_max_entities`) are absent
    from the sample YAML (`INSTALL.md` documents them). The movement keys and the
    deprecated `correction_tolerance_m` key were removed with the guard
    (`config.go` now carries 22 keys; the shipped YAML sets 19). **AC:** full schema
    validation with errors on unknown/invalid values, `--check-config`, docs and
    sample YAML match the struct.

17. **Per-opcode rate limits partial.** Beyond the per-IP bucket: chat 5/5 s (shared
    with group responses), item ledger 5/s (shared with container actions), one
    transform per tick, damage budget. Level change, player visual, handshake and
    query have no per-session limit. **AC:** per-session buckets for
    level/visual/group/stash, drops logged and audited.

18. **Protocol version pinning absent.** `ProtocolVer = 0x01` is fixed and the
    handshake rejects only values ≠ 1 (`server.go:413-431`), while payload revisions
    v2–v5 have already changed layouts (`ENTITY_ENTER_AOI` v3, group set, AI, item
    and container opcodes). → A v0.3 client and a v0.6 client both pass the
    handshake and desync. **AC:** handshake carries min/max revision, server picks
    highest common and rejects mismatches with a distinct reason.

19. **Release packaging v0.6.0.** The root `Makefile` `dist` target omits
    `gamedata/configs/text/{eng,rus}`, does not verify the faction-relation config,
    emits no checksums, and does not stamp the version into the server log or DLL
    (`ROADMAP.md`, Next — packaging and tagging); `dist/zone-online-v0.5.0` predates the branch features;
    `dist/.../client/README.txt` still says "protocol v3". **AC:** reproducible
    `make dist`, all 19 gamedata files verified by hash, SHA256SUMS, version stamped
    at startup and in binaries.

---

## 3. Feature-complete for the vision (post-launch backlog)

- **Faction warfare:** territory, capture windows, raids, reputation effects, war
  state persistence, admin overrides. Today only `characters.reputation` and
  `economy_tier` columns exist; no territory/war tables (`ROADMAP.md`, Backlog — faction warfare).
- **Server-authoritative AI combat + damage:** hostility, per-member puppets, loot,
  health/state persistence, server-side pathfinding against level geometry
  (`AI_REPLICATION.md:93-96`).
- **Server-side damage model:** hit registration currently applies the
  client-reported hit power 1:1 with a 150 single-hit clamp; armor, hit location,
  weapon damage and cover (LOS) are server-blind.
- **Level transitions / arbitrary levels:** multi-map world, questing and territory
  design depend on it (`ROADMAP.md`, Backlog).
- **Master server + moderation:** listing service, ban/whitelist tooling, report
  flow.
- **VOIP/radio:** no code exists anywhere in the repo.
- **Group/party persistence:** in-memory only; no leave-rejoin window or ranks
  (`ROADMAP.md`, Backlog — group persistence).
- **Quests/events replication:** only emission and mutant-raid sync
  (`zone_worldevent.script`); no quest state.
- **Economy breadth:** trader stock, ammo ambiguity (documented residual in
  `INVENTORY_V2.md:123-130`), pricing, per-tier inventory.
- **Per-player progression:** rubles/tier/reputation/rank exist as columns with no
  gameplay wiring beyond starter money.

---

## 4. Operations

- **Service install/autostart:** none — no systemd unit, NSSM/service script, or
  Windows service documentation.
- **Logging/metrics:** zap production JSON to stdout only; no file sink, rotation,
  Prometheus, expvar or `/metrics` endpoint. Only counter exposed is
  `DroppedPackets` (`udp_listener.go:112`) and `status` (active/tick/uptime).
- **Monitoring:** none; no health endpoint, alerting, or dashboards.
- **Admin console UX:** raw line protocol on one named pipe/socket; no interactive
  console, history, or remote-safe transport.
- **Update/versioning:** `VERSION.txt` only; no build stamping, no protocol
  revision negotiation (item 18), no client auto-update.
- **Release packaging:** v0.5.0 assembled by hand; `make dist` incomplete (item 19).
- **CI gaps:** race suite only on `main` pushes; Lua syntax validation exists
  (`lua-syntax` job) but no XML validation, no `gofmt`/vet gate (the tree is
  `gofmt`-clean but nothing enforces it), no dist build, no Linux server artifact.
- **Crash reporting:** none server-side (no panic recovery) or client-side (no
  minidumps/telemetry upload).

---

## 5. Testing & validation plan

1. **Two-client in-game matrix** (gate): join, reconnect, sleeper reclaim, group
   lifecycle, chat, safe zone, PvP kill/death/respawn, inventory sync, world items.
2. **Soak test:** 6–24 h, 8–16 clients, world events, churn (join/leave/kick),
   watch goroutines, RSS, WAL growth, dropped-packet counters, audit growth.
3. **48-player synthetic load:** extend `scale_test.go`/`scale_bench_test.go` to a
   UDP-level harness with real sockets; record tick time, bandwidth, queue drops.
4. **Packet-loss/jitter:** `netem` (Linux) or Clumsy (Windows) at 5/10/20% loss and
   50–200 ms jitter; verify ACK retransmit, proxy smoothing and hit-registration
   behavior. The server no longer corrects movement or strikes on gaps (owner
   decision), so there are no movement-correction or lag-switch false positives to
   measure.
5. **Fuzz/adversarial:** extend the existing `FuzzLoad` to header parsing and every
   opcode handler; run malformed/oversized/truncated matrices under `-race`; assert
   no panic (and add worker `recover()` first).
6. **LOS differential:** random and in-combat rays compared against engine raycasts
   per level; measure false-block/false-clear; only then enable.
7. **Save-corruption drills:** kill -9 mid-write, truncate WAL, delete shm; verify
   SQLite recovery and bounded data loss; document outcomes.
8. **Restore drill:** `VACUUM INTO` backup → restore into a clean dir → boot server
   → verify characters, inventory, stashes, audit; keep the runbook in `INSTALL.md`.
9. **Protocol upgrade/rollback:** with revision negotiation, prove old/new client
   rejection and server rollback with an existing DB.

---

## 6. Prioritized table

| # | Item | Category | Pri | Effort | Owner |
|---|------|----------|-----|--------|-------|
| 1 | Two-client live validation | Testing | P0 | M | QA/docs |
| 2 | Hit-registration live validation + server-side damage model | Gameplay | P0 | M | Go + Lua |
| 3 | Respawn/home spawn/death rules | Gameplay | P0 | M | Go + Lua |
| 4 | Level scope decision / engine work | Engine | P0 | L | C++ |
| 5 | Master server or scoped-out browser | Net | P0 | M | Go + Lua |
| 6 | LOS multi-sample/stance/time alignment | Anti-cheat | P0 | M | Go |
| 7 | Stash/container/trader client UI | Gameplay | P0 | L | Lua + Go |
| 8 | Protocol revision negotiation | Protocol | P0 | S | Go + C++ |
| 9 | Shutdown flush + drain-before-close | Ops | P0 | S | Go |
| 10 | Backup/restore + drill | Ops | P0 | S | Go/docs |
| 11 | Ban/unban/whitelist + mandatory token | Admin | P0 | S | Go |
| 12 | Packaging v0.6.0 + checksums + stamping | Release | P0 | S | ops/docs |
| 13 | Chat channels/mute/mentions | Gameplay | P1 | M | Lua + Go |
| 14 | Per-opcode rate limits + drop audit | Security | P1 | M | Go |
| 15 | Audit review tooling + retention | Admin | P1 | S | Go |
| 16 | Crash resilience: worker recover, kill-9 drill | Ops | P1 | S | Go |
| 17 | Config schema validation + docs sync | Ops | P1 | S | Go/docs |
| 18 | AI combat + hostility (needs 6) | Gameplay | P1 | L | Go |
| 19 | Group persistence + rejoin window | Gameplay | P1 | M | Go |
| 20 | Client hit-detection risk acceptance (or LOS) | Anti-cheat | P1 | S | docs |
| 21 | CI: Lua/XML/gofmt gates, dist, Linux build | CI | P1 | S | ops |
| 22 | File logging/rotation + `/metrics` | Ops | P2 | M | Go |
| 23 | Service install/autostart docs | Ops | P2 | S | ops/docs |
| 24 | Client crash reporting | Ops | P2 | M | C++ |
| 25 | VOIP/radio | Feature | P2 | L | C++ + Go |
| 26 | Quests/events replication | Feature | P2 | L | Go + Lua |
| 27 | Trader economy breadth | Feature | P2 | L | Go + Lua |
| 28 | Faction warfare (territory/war state) | Feature | P2 | L | Go + Lua |
| 29 | Doc drift sweep (protocol v3/v5, table counts) | Docs | P2 | S | docs |
