# Zone Online — Requirements Traceability Status

Traceability matrix for every user requirement raised during the development of
this project, verified against the source tree at the time of writing (working
tree on top of commit `6f95ae2` on branch `feature/los-inventory-ai`). Status
values are **DONE**, **PARTIAL** and **NOT STARTED**. "Evidence" names the files,
tests and documents that back the status; it does not replace the live
two-client gate recorded in [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md).

## Verification performed for this document

- **Opcodes:** 29 constants in `zone-server/internal/protocol/opcodes.go` plus
  `OpAck` (`0x0005`) in `zone-server/internal/protocol/packets.go` = **30**.
  Opcodes `0x0040`, `0x0041`, `0x0070` and `0x007B` are absent from the server,
  and no server or Lua script references them.
- **Configuration:** **22** keys in `zone-server/internal/config/config.go`;
  `zone-server/zone_server.yaml` sets **19** of them. The removed movement keys
  (`max_speed_mps`, `movement_grace`, `fall_allowance_m`, `lagswitch_gap_ms`,
  `lagswitch_strikes`, `anticheat_violation_window_s`,
  `anticheat_violation_kick_count`, `spawn_grace_s`) and
  `correction_tolerance_m` are not present in either file.
- **Tests:** `go test -count=1 ./...` is green across all eight packages. Counted
  via `go test -list`: **264 Test functions, 17 benchmarks, 1 fuzz target**
  across **51** `_test.go` files. `gofmt -l` is clean.
- **Lua:** 11 scripts in `gamedata/scripts`, 9,863 lines total; the committed
  validator `tools/check_lua_syntax.py` compiles all of them under LuaJIT 2.1,
  and CI runs it as the `lua-syntax` job.
- **Harness:** the 12-client UDP load harness is committed at
  `tools/loadtest/zone_loadtest.py` with `tools/loadtest/README.md`.

## Matrix

| # | Requirement | Status | Evidence | Notes |
|:--|:---|:---:|:---|:---|
| a | Fix merge crash / missing player models | DONE | `EntityEnterAoI` v3 carries section, faction, health, gvid and name (`zone-server/internal/protocol/packets.go:147`); the server emits it only once visual and gvid are known (`protocol_v2_test.go:TestProtocolV2_EntityEnterWaitsForGvidAndVisual`, `TestProtocolV2_PlayerVisualBroadcastsEntityEnter`); the client spawns real alife proxies from that data (`gamedata/scripts/zone_dummy.script:296`); `TestEntityEnterV3_132BytesWithName`, `TestJoinFlow_NewcomerSkipsPeersWithoutVisual`. | Remote players use the peer's real outfit and graph vertex; no generic AI NPC stand-ins. Model/animation fidelity beyond the alife proxy remains a documented limitation. |
| b | Safe zone weapon system (holster, fire veto, immunity both directions, AI hostility suppression; vendor UI clicking guarded) | DONE | Server safe-zone checks `zone-server/internal/game/safezone.go` + `safezone_test.go` (6 tests, including all canonical zones); damage immunity `damage.go` + `TestDamage_SafeZoneImmunity`, `TestLifecycle_DamageSafeZoneImmunity`, `TestProtocolV2_DamageRejectedWhenAttackerInSafeZone`; client `gamedata/scripts/zone_safezone.script` holsters, blocks only `WEAPON_ONLY` binds, and keeps `kTRADE`/`kBUY`/`kINVENTORY` and other UI keys in `NEVER_BLOCK` so vendor/trade windows stay clickable; INSTALL.md Part 6. | The vendor-UI guard is script-level and covered by Lua syntax validation, not by a Lua unit test. |
| c | Server browser UI redesign (native X-Ray, async queries, tabs/sort/filter) | DONE | `gamedata/scripts/zone_ui_server_list.script` (2,758 lines) + `zone_ui_server_list.xml`; asynchronous query exports `ZN_QueryStart`/`ZN_QueryPoll`/`ZN_QueryCancel`/`ZN_QueryCancelAll` in `zone-client/src/lua/zone_bindings.cpp:78-95`; INSTALL.md Part 6. | Validated by Lua syntax and XML parse; there is no automated UI test because the window is engine-hosted. |
| d | Seamless join: faction + loadout in-flow, no main-menu detour | DONE | `server_join_flow_test.go` (12 tests: new and returning players, invalid faction/loadout, MTU-chunked inventory sync, same-UUID eviction); `gamedata/scripts/zone_main.script`; README "Seamless Join". | Only `l01_escape` is hosted (see requirement y). |
| e | Faction warfare: vanilla-style relations + all-faction safe zones | PARTIAL | Relations: `zone-server/internal/database/faction_relations.go` + 5 tests, `zone-server/internal/game/faction_relations.go`, DLTX config `mod_system_zone_faction_relations.ltx`; PvP gates `groups_test.go:TestDamageGate_SameFactionRejected`/`TestDamageGate_DifferentFactionAllowed`; all-faction safe zones `TestSafeZone_AllCanonicalZones`. | Territory, capture windows, raids, reputation effects and persistent war state are **not started** (ROADMAP backlog; PRODUCTION_READINESS.md section 3). |
| f | Player groups / party with mixed factions | DONE | `zone-server/internal/game/groups.go` + `groups_test.go` (17 tests: invite/accept/leave, invite TTL, size cap, mixed-faction flows, chat commands); reliable `0x0078`/`0x0079`/`0x007A`. | Groups are in-memory only; persistence across restarts is not implemented (PRODUCTION_READINESS.md section 3, P1 item 19). |
| g | Server-authoritative world: movement/items/economy/stash/safezone/groups | PARTIAL | Movement is client-authoritative by owner decision (no validator; `netcode_transform_test.go:TestTransform_AnyDistanceAccepted`); hit registration `damage.go` + `damage_relay_test.go` + `damage_audit.go`, client 0x0050 send/apply in `gamedata/scripts/zone_dummy.script` and dispatch in `zone_net.script`; items `item_ledger.go` + tests; containers/stash `container_ledger.go`, `stash_manager.go` + tests; economy `economy.go` + `TestEconomyManager`; safe zones `safezone.go`; groups `groups.go`. | Movement is trusted, not policed (owner choice). Hit registration is wired end-to-end but not yet exercised by two live clients, and damage is applied 1:1 with no server-side armor/scaling. AI combat is not enabled (s) and damage LOS is not enabled (t) (PRODUCTION_READINESS.md P0 items 2, 6, 7, 8). |
| h | Persistence for inventories/progression per player | DONE | `characters` / `character_inventory` schema; `db_isolation_test.go:TestInventoryIsolationBetweenAccounts`, `TestProgressionIsolationBetweenAccounts`; session-level `isolation_test.go` (7 tests). | Isolation is verified by committed suites; the progression system itself is minimal (rubles, tier, rank and reputation columns with little gameplay wiring; PRODUCTION_READINESS.md section 3). |
| i | Server-side ticks configurable | DONE | `config.go` validates 1–240 Hz and `tick.go` drives the loop; `config_test.go:TestValidate_TickRateBounds`; time-based logic `TestTick_PlayTimeIncrement_TimeBased`; CLI `--tick-rate`; the rate is advertised in the server query reply. | — |
| j | Git discipline: commit often, push, merge to main | PARTIAL | `git log` shows 15 commits on `feature/los-inventory-ai`, sized by wave (v0.2–v0.5 releases, authority, engine-compat, LOS/AI/inventory). | The cleanup pass is uncommitted on the feature branch at the time of writing, and the branch has not been merged to `main`. No git operations were performed by this documentation pass. |
| k | Anti-lag mechanics / smoothing | DONE | Interpolation and extrapolation in `gamedata/scripts/zone_dummy.script` (Hermite spline, 100 ms jitter buffer, 250 ms / 1.5 m extrapolation cap, 150 ms blend back); the server accepts any finite transform (`netcode_transform_test.go:TestTransform_AnyDistanceAccepted`, `TestTransform_SessionIDMismatchIgnored`) and sends no correction traffic. | The server no longer corrects or strikes on packet loss by owner decision; a packet-loss/jitter soak (netem or Clumsy) remains on the PRODUCTION_READINESS test plan (section 5, item 4). |
| l | Duplication prevention / server-authoritative items | DONE | `item_ledger.go` (ActionID cache, monotonic floor, rate limit), `container_ledger.go`; tests `TestItemReplayActionIDEchoesWithoutDuplicate`, `TestItemLedgerOldActionIDRejected`, `TestPickupWorldItemConcurrentSingleWinner`, `TestContainerConcurrentWithdrawSingleWinner`, `TestContainerReplaySharesItemLedgerCache`; the committed load harness rejects a duplicate pickup. | Verified in-process and by the committed 12-client harness. |
| m | Hit registration: client hit report → server validation → victim apply | DONE | Client send/cancel and victim apply in `gamedata/scripts/zone_dummy.script` (0x0050) with dispatch in `zone_net.script`; validation (session match, cross-level, self-damage, safe zones, friendly fire, 300 m range, sanity, 150 single-hit clamp, rolling budget) in `damage.go` + `damage_budget_test.go`; victim-only relay in `server.go` + `damage_relay_test.go`; audit rows in `damage_audit.go`. | Movement is not policed (owner decision), so lag-switch strikes are gone. True LOS is PARTIAL (t). Hit registration is not yet exercised by two live clients, and damage is applied 1:1 without server-side armor/scaling. |
| n | xrRazom co-op coexistence, both menu buttons | DONE | Single dormancy gate `zone_main.xrr_is_active()` (also checks `XrrNet`) in `gamedata/scripts/zone_main.script`; `modxml_zone_main_menu.script` inserts `btn_zone_online` without deleting xrRazom's `btn_multiplayer`; INSTALL.md Part 3. | Static verification only; a live two-mod session test is still pending (PRODUCTION_READINESS.md P0 item 1). |
| o | Proxy DLL drag-and-drop install | DONE | `zone-client/src/proxy/version_proxy.cpp` forwards all 15 `VERSION.dll` exports and loads `ZoneClient.dll` from its own directory; injector `injector/injector_main.cpp` supports `--launch`/`--wait`/`--pid`/`--dll`/`--ephemeral`; INSTALL.md Parts 3–4. | — |
| p | Error handling everywhere | DONE | Server `OpError` `0x0077` and join-flow rejection tests (`TestJoinFlow_InvalidFactionRejectedExplicitly`, `TestJoinFlow_InvalidLoadoutRejectedExplicitly`); client error surfacing through `ZN_GetLastError` (`zone_bindings.cpp:142`); handshake/status/version errors documented in INSTALL.md Part 5. | Residuals: UDP worker loops have no `recover()` and there is no client crash reporting (PRODUCTION_READINESS.md item 14 and section 4). |
| q | 48-player scalability | DONE | `scale_test.go` (exactly-once chunking, 48-session smoke, max-players), `scale_bench_test.go` (6 benchmarks); recorded dense tick 0.974 ms with 980 B and 1 allocation per tick (README). | No real 48-player soak has been run (PRODUCTION_READINESS.md section 5, items 2–3). |
| r | Full inventory transactions including containers/traders | PARTIAL | Containers and stashes are server-complete: `container_ledger.go` + 8 container tests, `stash_manager.go` + 4 stash tests. Trader backend `economy.go` (`BuyItem`/`SellItem`) + `TestEconomyManager`. | No trader opcode and no client container/stash/trader UI; `zone_net.script:1139-1151` logs `0x007F`/`0x0080` as active stubs (PRODUCTION_READINESS.md P0 item 7). |
| s | AI replication | PARTIAL | `zone-server/internal/game/ai_replication.go` + 9 tests (hysteresis, 30 Hz decimation, entity cap, offline 1 Hz macro-step, seeded squads); `internal/ai/puppet.go` + tests; client `gamedata/scripts/zone_ai_proxy.script`; four seeded Cordon squads. | Patrol replication only. Combat, damage, hostility and loot are not implemented; attack/death animation values exist on the wire but are never produced (PRODUCTION_READINESS.md items 7 and 18). |
| t | LOS geometry | PARTIAL | `zone-server/internal/los` (hardened loader + `SegmentBlocked`, 15 tests, 1 fuzz target, 3 benchmarks), `zone-server/tools/levelgeom` (2 tests), real `l01_escape` artifact 2,949,773/2,949,775 triangles, 50.51 MiB, SHA-256 recorded in `docs/LOS_GEOMETRY.md`. | Not wired: no `los_enabled` flag; needs multi-sample body points, stance from `AnimFlags` and attacker/target time alignment (PRODUCTION_READINESS.md P0 item 6). |
| u | No legacy code | DONE | Server opcodes `0x0040`/`0x0041`/`0x0070`/`0x007B` absent from `opcodes.go` (29 entries + `OpAck` = 30); no server or script references remain; open-stash gate removed (`container_ledger.go:22-27`); `correction_tolerance_m` and the eight movement/lag-switch keys absent from `config.go` and the YAML; dead files deleted (`internal/database/admin.go`, `server_stash_test.go`, `zone_bindings.h`, `3rdparty/lua`, `movement_guard.go`, `anticheat.go`, `transform_history.go`); `zone-client/src/protocol/packets.h` carries no stash or AI-action leftovers. | — |
| v | Player isolation of inventory/progression | DONE | `db_isolation_test.go` (2 tests) and `isolation_test.go` (7 tests); per-account UUID keys; a same-account reconnect evicts the previous session. | Same evidence base as h. |
| w | Documentation current | DONE | This pass updates `README.md`, `INSTALL.md`, `ROADMAP.md`, `docs/PROJECT_BRIEF.md`, `docs/AUTHORITY_MODEL.md`, `docs/PRODUCTION_READINESS.md` and this file to the movement-guard removal and hit-registration state; opcode (29 + `OpAck` = 30), config (22 keys, 19 in the shipped YAML), test (264/17/1 across 51 files) and Lua (9,863 lines) counts were re-verified against source. | Residual: the assembled `dist/zone-online-v0.5.0` copies of README/INSTALL/ROADMAP/PROJECT_BRIEF/AUTHORITY_MODEL are release snapshots and were not updated. |
| x | Master server for public listing | NOT STARTED | No HTTP announce/list service exists; the Internet tab probes favorites and history only (PRODUCTION_READINESS.md P0 item 5). | Explained and tracked in the backlog; release must either ship the service or scope the browser to direct/favorites. |
| y | Arbitrary level hosting beyond `l01_escape` | NOT STARTED | `supportedLevels` validates names but resolves every id to 0 and only `l01_escape` is hosted (`zone-server/internal/game/server.go:1193-1218`); README Known Limitations; PRODUCTION_READINESS.md P0 item 4. | Documented engine limitation: the stock start path runs `all.spawn`, and no Lua API loads an arbitrary level by name. |
| z | Live two-player in-game validation | NOT STARTED | No two-real-client session has been recorded; the 12-client simulation and all committed suites pass. | Pending user test; this is the gate for calling the feature set stable (PRODUCTION_READINESS.md P0 item 1). |

## Top remaining P0 before public release

Taken from the prioritized table in [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md) section 6:

1. **Hit-registration polish** — server-side armor/damage scaling (damage is currently applied 1:1 from client-reported power) and live two-client validation of the 0x0050 path (m).
2. **Two-client live runtime validation** (requirement z). Movement remains client-authoritative by owner decision; no server-side movement validation is planned in this wave.
3. **Respawn / home spawn / death rules** (g).
4. **Level scope decision or engine work** (y).
5. **Master server, or an explicitly scoped-out browser** (x).
6. **LOS multi-sample / stance / time alignment** (t).
7. **Stash / container / trader client UI** (r).
8. **Protocol revision negotiation** (protocol hardening).
9. **Shutdown flush and drain-before-close** (ops).
10. **Backup/restore plus a drilled restore** (ops).
11. **Ban/unban/whitelist and a mandatory admin token** (admin).
12. **Packaging v0.6.0 with checksums and version stamping** (release).
