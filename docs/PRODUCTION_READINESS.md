# Zone Online — Production Readiness Assessment

Read-only assessment of `F:\AnomalyDev\zone-online` on branch
`feature/travel-ai-stash-trade` (working tree on top of the v0.6.0 package,
88 tracked files modified plus new AI-combat, LOS-gating, level-travel and
stash/trade source files). `VERSION.txt` is `0.6.0`; `dist/zone-online-v0.6.0`
is the movement-unpoliced package prepared for the first live player test and
predates the travel/AI-combat/LOS/stash-trade waves on this branch. The
`l01_escape.occl` artifact (52,967,834 bytes) is not in git and is absent from
the in-tree v0.6.0 folder; it is expected to ship in the v0.7.0 package under
`server/zone_los/`. Nothing here is implemented by this document; it is a
prioritized gap list with current-state evidence.

---

## 1. Current verified state

**What works today (evidence in tree):**

- Dedicated Go server: UDP `:27015`, 12-byte header, opcodes `0x0001`–`0x0085`
  (`internal/protocol/opcodes.go`; 34 declared plus `OpAck` = 35), 30 Hz
  configurable tick, 8 UDP workers with FNV-1a address affinity
  (`internal/network/udp_listener.go`), per-IP token bucket (100 pkt/s, burst
  150), reliable ACK/retransmit, SQLite WAL with 10 tables
  (`internal/database/schema.go`: accounts, characters, character_inventory,
  world_items, schema_version, world_stashes, safe_zones, audit_log,
  faction_relations, ai_squads).
- Seamless join on `l01_escape` and in-game level travel: `SHOW_START` → native
  faction/loadout dialog → server-created character + starter inventory →
  `LOAD_LEVEL` + `INVENTORY_SYNC`; returning players go straight to the saved
  spawn (`internal/game/server.go:456-654`, `:1268-1326`). `OpLevelChange`
  (`server.go:1007-1072`) switches any installed level client-trusted: grid/AoI/AI
  caches reset, per-level entity enters/leaves exchange, safe-zone state is
  recomputed and world items re-sent; tests in `server_level_travel_test.go`,
  doc `zone-server/docs/LEVEL_TRAVEL.md`. The initial menu spawn remains
  `l01_escape` (engine limitation).
- Movement is client-authoritative by owner decision: transforms are accepted
  (no displacement guard, no `OpPositionCorrection`, no lag-switch strikes;
  `movement_guard.go`, `anticheat.go` and `transform_history.go` deleted). Hit
  registration is implemented end to end: the client reports proxy hits
  (`0x0050`), the server validates session match, cross-level, safe zones,
  friendly fire, 300 m range from last known positions, sanity, a 150 single-hit
  clamp, the rolling 1 s damage budget and the line-of-sight gate, then relays
  the accepted hit to the victim only (`damage.go`, `damage_relay_test.go`,
  `damage_audit.go`), and the victim's client applies it via `change_health`.
- LOS gating is wired and enabled by default: `los_gate.go` +
  `los_gate_test.go`, `DamageHandler.SetLOSChecker` in `server.go:171`,
  multi-sample attacker-eye → head/chest/pelvis, reject only when all three
  segments are blocked, fail-open when `<level>.occl` is missing or corrupt, one
  warning per level; config `los_enabled` (true) / `los_data_dir` (`zone_los`);
  only `l01_escape` has an artifact; doc `zone-server/docs/LOS_GATING.md`.
- AI combat wave B: server-side FSM (aggro 40 m, chase, melee 2 m / 1.5 s /
  10 dmg, alert cool-down, corpse) over the existing patrol replication,
  faction-hostility gating from the persisted relation table, safe-zone
  suppression, AI→player damage through the shared validator, player→AI damage
  via `0x0050` with `TargetID >= 1_000_000`; config `ai_combat_enabled` and six
  `ai_*` knobs; no loot/pathfinding; doc `zone-server/docs/AI_REPLICATION.md`.
  Patrol replication (`OpAIState` `0x007C`, four seeded Cordon squads,
  ONLINE/OFFLINE tiers) is as before.
- Authoritative item ledger (`0x007D`/`0x007E`), container/stash transactions
  (`0x007F`/`0x0080`, per-packet access validation) and the native stash/trader/
  wallet path (`stash_trade_ledger.go`: `OpStashAction/Result` `0x0081/0x0082`,
  `OpTradeAction/Result` `0x0083/0x0084` with the `trade_max_money_delta` cap,
  `OpWalletUpdate` `0x0085`) — `docs/AUTHORITY_MODEL.md`,
  `zone-server/docs/INVENTORY_V2.md`. The legacy stash opcodes `0x0040`/`0x0041`
  and the open-state gate were removed.
- Client: 22 `ZN_*` exports (`zone-client/src/lua/zone_bindings.cpp`), `version.dll`
  proxy + injector install paths, native server browser (async query, favorites/history),
  social HUD, safe-zone enforcement, item/stash/trade send paths and wallet
  reconcile in `zone_net.script`, AI puppet rendering in `zone_ai_proxy.script`.

**Validation performed:**

- `go test ./...` passes on this tree (Go 1.26.2; 308 `Test` funcs, 17
  benchmarks, 1 fuzz target in `internal/los`, across 57 `_test.go` files).
  `gofmt -l` is clean across `zone-server`.
- 48-session in-process coverage: `internal/game/scale_test.go`
  (`TestSnapshotChunking_AllNeighborsDeliveredExactlyOnce`,
  `TestTick48SessionsSmoke_NoDeadlock`, `TestScale_MaxPlayersFull`) and
  `scale_bench_test.go` (`BenchmarkTick48Dense/Spread`).
- The 12-client UDP load test is committed as
  `tools/loadtest/zone_loadtest.py` with `tools/loadtest/README.md`; the final
  run on this branch passed: 12 handshakes, 5,052 snapshots, 228 entity enters,
  group round-trip, drop/pickup/duplicate-pickup rejection, AI AoI transitions,
  300 malformed packets, no panics.
- Lua validation: `tools/check_lua_syntax.py` compiles all 11 scripts (9,776
  lines) with the LuaJIT 2.1 runtime bundled by `lupa`, and the CI `lua-syntax`
  job runs it. XML configs parse clean but no XML validation tooling exists.
- CI (`.github/workflows/ci.yml`): Go tests with `-race` on push/PR to `main`;
  LuaJIT syntax validation (`lua-syntax` job); MSVC Release client build. No XML
  validation, no lint, no dist packaging, no Linux build job.

**Install paths:** drag-and-drop `version.dll` proxy (recommended) and injector
(`--launch`/`--wait`/`--pid`) with 19-file gamedata provisioning — `INSTALL.md`,
`dist/zone-online-v0.6.0/client/README.txt`.

---

## 2. Must-fix before any public server

Each item: area → why → acceptance criteria.

1. **Two-client live runtime validation.** No two real clients have ever played
   together (`ROADMAP.md` Next; `README.md` Known Limitations). → Gate for calling
   anything stable. **AC:** two clients on one server verify proxy spawn/despawn,
   interpolation, mutual visibility, safe-zone both sides, faction colors, group
   invite/accept/leave/roster, chat, reconnect + sleeper reclaim, starter kit,
   level filtering, level travel, stash/trader flows, hit registration (mutual
   damage, death persistence, safe-zone rejection), AI combat, measured latency;
   results recorded in-repo. The in-tree v0.6.0 package is the movement-unpoliced
   build for this first test; v0.7.0 adds the newer waves.

2. **PvP hit registration is shipped but not validated in-game.** The client
   sends `OpDamageNotify` (`0x0050`) from the proxy hit path, cancels the local
   proxy damage and applies the relayed result (`gamedata/scripts/zone_dummy.script`),
   and the server validates and relays to the victim only (`damage.go`,
   `damage_relay_test.go`, `damage_audit.go`), now including the multi-sample LOS
   gate when an occluder exists. No two live clients have exercised the path, and
   the server-side damage model is missing (client-reported power is applied 1:1
   with no armor, hit-location or weapon scaling). → PvP balance and death
   behavior are unproven. **AC:** two live clients damage/kill each other;
   safe-zone, friendly-fire and wall-shot rejection verified both ways; kill
   persists `characters.dead=1`; damage units/scaling defined server-side.

3. **Respawn/home-spawn/death rules not built.** The only respawn is an implicit
   handshake reset of a dead character to Rookie Village at full health
   (`server.go:494-499`); no home-spawn persistence, no client respawn UI/packet,
   no death penalty or insurance. → A dead player must reconnect and gets a free
   teleport home. **AC:** server-owned respawn flow, persisted home spawn, death
   rules enforced server-side, client UI, no reconnect required.

4. **Initial menu-level scope (engine limitation).** In-game travel between all
   installed levels now works via `OpLevelChange` (`zone-server/docs/LEVEL_TRAVEL.md`),
   but the first menu start is still `l01_escape`: the stock start path runs
   `all.spawn` and no Lua API loads a level by name at menu time
   (`ROADMAP.md`, Done — seamless join and travel). **AC:** either document the
   menu-start scope explicitly in release notes, or add engine-level menu level
   loading (xrRazom-style patch or save-based spawns) with a tested path.

5. **Master server absent.** The Internet tab only probes favorites + history
   (`zone_ui_server_list.script:1748-1773`); there is no HTTP announce or list
   endpoint anywhere. **AC:** either announce + `GET /list` service reusing the
   70-byte `SERVER_QUERY_RES` fields, or release notes/UI explicitly state
   direct/favorites-only and the Internet tab is renamed/removed.

6. **LOS gating enabled; occluders incomplete.** The pipeline is wired into the
   shared `DamageHandler` and on by default: multi-sample attacker-eye to
   head/chest/pelvis, reject only when all three are blocked, fail-open for
   missing/corrupt files (`zone-server/docs/LOS_GATING.md`, `los_gate.go`).
   Only `l01_escape` has an artifact (52,967,834 B, shipped under
   `server/zone_los/`, not in git). **AC:** generate and ship `.occl` files for
   every installed level, then run the differential test vs in-engine raycasts
   and record the false-reject/false-clear rate.

7. **AI combat is basic.** The server-side FSM (aggro/chase/melee/alert/corpse,
   faction-hostility gating, safe-zone suppression, player→AI damage over
   `0x0050`) shipped with the patrol replication wave
   (`zone-server/docs/AI_REPLICATION.md`, `ai_combat.go`, `internal/ai/combat.go`).
   Puppets do not loot, do not path around level geometry, and fight one entity
   per squad. **AC:** loot on death, server-side pathfinding during chase,
   per-member puppets and faction-aware AI-vs-AI combat, or release notes state
   the basic scope.

8. **Client-side hit detection and trusted movement.** Hit detection stays
   client-side because the server has no collision mesh; it validates the claim
   against range, sanity, budget and the static LOS occluder, but cannot raycast
   the shot itself. Movement is not validated at all by owner decision. → A
   modified client can move freely, and levels without an occluder remain
   wall-shootable. **AC:** LOS-backed verification for all levels plus a written
   risk acceptance with monitoring.

9. **Stash/trader UI shipped; numeric container UI missing.** Native stash/trade/
   wallet flows are wired end-to-end (`stash_trade_ledger.go`, 0x0081–0x0085,
   client senders/consumers in `zone_net.script`) with replay protection, rate
   limiting, the `trade_max_money_delta` cap and wallet reconciliation. The
   numeric rowid container path (`0x007F`/`0x0080`) is still server-only and
   logged as a stub by the client. **AC:** implement the rowid container UI or
   explicitly document that the native position-keyed path supersedes it.

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

15. **Graceful shutdown / save flush partially hardened.** On SIGINT/SIGTERM,
    `main.go` cancels ctx; shutdown now waits for the final game tick
    (`Server.WaitGameLoop`), closes the DB write queue and joins its drain
    (`DB.WaitWriteQueue`) before the deferred `db.Close()`, so the previous
    drain-vs-Close race is closed. Still open: dirty sessions are only flushed by
    the 60 s checkpoint, so a shutdown can lose up to that window of movement, and
    clients receive no disconnect message. **AC:** shutdown flushes dirty sessions,
    sends a disconnect reason, and a restart soak proves no loss.

16. **Config validation/completeness.** `Validate()` checks only port, tick rate and
    max players (`config.go:257-272`); an invalid `log_level` silently falls back to
    info; `map_name` is not checked against `supportedLevels`. The movement keys and
    the deprecated `correction_tolerance_m` key were removed with the guard, and the
    sample YAML now sets all 32 keys (`config.go`, `zone_server.yaml`). **AC:** full
    schema validation with errors on unknown/invalid values, `--check-config`, docs
    and sample YAML match the struct.

17. **Per-opcode rate limits partial.** Beyond the per-IP bucket: chat 5/5 s (shared
    with group responses), item ledger 5/s (shared with container, stash and trade
    actions), one transform per tick, damage budget. Level change, player visual,
    handshake and query have no per-session limit. **AC:** per-session buckets for
    level/visual/group/stash, drops logged and audited.

18. **Protocol version pinning absent.** `ProtocolVer = 0x01` is fixed and the
    handshake rejects only values ≠ 1 (`server.go:413-431`), while payload revisions
    v2–v5 have already changed layouts (`ENTITY_ENTER_AOI` v3, group set, AI, item
    and container opcodes). → A v0.3 client and a v0.6 client both pass the
    handshake and desync. **AC:** handshake carries min/max revision, server picks
    highest common and rejects mismatches with a distinct reason.

19. **Release packaging v0.7.0.** The root `Makefile` `dist` target omits
    `gamedata/configs/text/{eng,rus}`, does not verify the faction-relation config,
    does not include `server/zone_los/`, emits no checksums, and does not stamp the
    version into the server log or DLL (`ROADMAP.md`, Next — packaging and
    tagging); `dist/zone-online-v0.6.0` is the movement-unpoliced first-live-test
    package and predates the branch features; `dist/.../client/README.txt` still
    says "protocol v3". **AC:** reproducible `make dist`, all 19 gamedata files and
    the `l01_escape.occl` artifact verified by hash, SHA256SUMS, version stamped
    at startup and in binaries.

---

## 3. Feature-complete for the vision (post-launch backlog)

- **Faction warfare:** territory, capture windows, raids, reputation effects, war
  state persistence, admin overrides. Today only `characters.reputation` and
  `economy_tier` columns exist; no territory/war tables (`ROADMAP.md`, Backlog — faction warfare).
- **AI behaviour depth:** loot, server-side pathfinding against level geometry,
  per-member puppets, AI-vs-AI combat, health/state persistence. Basic
  aggro/chase/melee/death combat has shipped (`AI_REPLICATION.md`, Wave B).
- **Server-side damage model:** hit registration currently applies the
  client-reported hit power 1:1 with a 150 single-hit clamp; armor, hit location,
  weapon damage and stance remain server-blind (cover is gated by the LOS
  occluder where one exists).
- **Initial menu level loading:** in-game travel shipped; the menu start remains
  `l01_escape` until engine-level level loading exists (`ROADMAP.md`, Backlog).
- **Master server + moderation:** listing service, ban/whitelist tooling, report
  flow.
- **VOIP/radio:** no code exists anywhere in the repo.
- **Group/party persistence:** in-memory only; no leave-rejoin window or ranks
  (`ROADMAP.md`, Backlog — group persistence).
- **Quests/events replication:** only emission and mutant-raid sync
  (`zone_worldevent.script`); no quest state.
- **Economy breadth:** trader stock, ammo ambiguity (documented residual in
  `INVENTORY_V2.md:123-130`), pricing, per-tier inventory. Basic server-authoritative
  trader buy/sell with the `trade_max_money_delta` cap and wallet reconcile has shipped.
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
- **Release packaging:** v0.6.0 assembled by hand as the movement-unpoliced first
  live-test build; `make dist` incomplete (item 19); the v0.7.0 package with
  `server/zone_los/` is not assembled.
- **CI gaps:** race suite only on `main` pushes; Lua syntax validation exists
  (`lua-syntax` job) but no XML validation, no `gofmt`/vet gate (the tree is
  `gofmt`-clean but nothing enforces it), no dist build, no Linux server artifact.
- **Crash reporting:** none server-side (no panic recovery) or client-side (no
  minidumps/telemetry upload).

---

## 5. Testing & validation plan

1. **Two-client in-game matrix** (gate): join, reconnect, sleeper reclaim, group
   lifecycle, chat, safe zone, PvP kill/death/respawn, inventory sync, world items,
   stash/trader flows, level travel, AI combat.
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
   per level with an artifact; measure false-block/false-clear; generate occluders
   for every installed level before widening coverage.
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
| 4 | Initial menu-level scope / engine work | Engine | P0 | L | C++ |
| 5 | Master server or scoped-out browser | Net | P0 | M | Go + Lua |
| 6 | Per-level occluders + LOS differential | Anti-cheat | P0 | M | Go |
| 7 | Numeric container UI (0x007F/0x0080) | Gameplay | P0 | L | Lua + Go |
| 8 | Protocol revision negotiation | Protocol | P0 | S | Go + C++ |
| 9 | Shutdown flush + drain-before-close | Ops | P0 | S | Go |
| 10 | Backup/restore + drill | Ops | P0 | S | Go/docs |
| 11 | Ban/unban/whitelist + mandatory token | Admin | P0 | S | Go |
| 12 | Packaging v0.7.0 + checksums + zone_los | Release | P0 | S | ops/docs |
| 13 | Chat channels/mute/mentions | Gameplay | P1 | M | Lua + Go |
| 14 | Per-opcode rate limits + drop audit | Security | P1 | M | Go |
| 15 | Audit review tooling + retention | Admin | P1 | S | Go |
| 16 | Crash resilience: worker recover, kill-9 drill | Ops | P1 | S | Go |
| 17 | Config schema validation + docs sync | Ops | P1 | S | Go/docs |
| 18 | AI loot + pathfinding + behaviour richness | Gameplay | P0 | L | Go |
| 19 | Group persistence + rejoin window | Gameplay | P1 | M | Go |
| 20 | Hit-detection risk acceptance for levels without occluders | Anti-cheat | P1 | S | docs |
| 21 | CI: Lua/XML/gofmt gates, dist, Linux build | CI | P1 | S | ops |
| 22 | File logging/rotation + `/metrics` | Ops | P2 | M | Go |
| 23 | Service install/autostart docs | Ops | P2 | S | ops/docs |
| 24 | Client crash reporting | Ops | P2 | M | C++ |
| 25 | VOIP/radio | Feature | P2 | L | C++ + Go |
| 26 | Quests/events replication | Feature | P2 | L | Go + Lua |
| 27 | Trader economy breadth | Feature | P2 | L | Go + Lua |
| 28 | Faction warfare (territory/war state) | Feature | P2 | L | Go + Lua |
| 29 | Doc drift sweep (protocol v3/v5, table counts) | Docs | P2 | S | docs |
