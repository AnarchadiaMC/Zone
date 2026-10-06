# Authority Model — Movement, Hits, Items, AI, LOS

How Zone Online decides what is true, what is validated, and what the client is
allowed to say. Written against the current tree: the v0.6.0 package plus the
level-travel, AI-combat, line-of-sight gating and stash/trade/wallet waves on
`feature/travel-ai-stash-trade`.

---

## Principles

1. **The server owns persistent state.** Characters, faction, money, inventory,
   world items, stashes, groups, safe zone membership and death are decided
   server-side and persisted in SQLite (WAL).
2. **The client is a presenter, an input device, and the source of truth for its
   own position.** It renders, plays sounds, reports movement, and reports hits
   for the server to validate.
3. **Movement is deliberately unpoliced; combat claims are validated.** The owner
   removed the movement guard and anti-cheat strike system entirely. Hit
   registration is the trust boundary: the server validates what it can without
   world geometry.
4. **Every accepted or rejected combat/item claim is bounded and auditable.**

---

## Movement

- Clients send `ClientTransform` (0x0010, 29 B) at 30 Hz: position, yaw/pitch,
  velocity, anim flags, graph vertex.
- The server keeps the latest transform per session, updates the spatial grid,
  drives AoI, recomputes safe-zone membership and checkpoints position to SQLite.
  It accepts any finite, in-bounds position. It does **not** validate
  displacement, speed, teleports or vertical movement.
- **No movement guard.** `movement_guard.go`, `anticheat.go` and
  `transform_history.go` were deleted. Opcode `OpPositionCorrection` (0x007B) is
  removed from the protocol, and the config keys `max_speed_mps`,
  `movement_grace`, `fall_allowance_m`, `lagswitch_gap_ms`, `lagswitch_strikes`,
  `anticheat_violation_window_s`, `anticheat_violation_kick_count` and
  `spawn_grace_s` are removed. No transform is rejected for distance, no
  correction is sent, no kick is issued and no lag-switch strike exists.
- **Trust model (accepted trade-off).** A modified client can move arbitrarily
  fast, teleport or hover; the server does not dispute it. This is an explicit
  owner decision, not an oversight.
- **Client smoothing only.** Remote proxies are Hermite-interpolated off a 100 ms
  jitter buffer with velocity extrapolation up to 250 ms / 1.5 m when packets
  lapse, blending back over 150 ms when data resumes. Smoothing is presentation;
  it never changes server state.

## Hit registration

- **Client hit path.** A hit on a spawned player proxy triggers
  `npc_on_before_hit`. The script verifies the hit came from the local actor,
  maps the proxy object to its session id, builds a 13-byte `OpDamageNotify`
  (0x0050: TargetID, AttackerID, Damage, BoneID) and sends it reliably. The
  client rate-limits sends to one per 50 ms (≤ 20/s). The local engine hit is
  then cancelled (`s_hit.power = 0`, `flags.ret_value = false`) because proxy HP
  is server-authoritative. A hit on a server AI puppet uses the same packet with
  `TargetID >= 1_000_000` (the AI entity id base).
- **Server validation** (`damage.go`) per hit, in order:
  1. attacker and target sessions exist; the attacker session is resolved from
     the UDP source address, and the packet's AttackerID/TargetID must match
     both sessions (session mismatch rejected);
  2. attacker and target on the same level (cross-level rejected);
  3. self-damage rejected;
  4. safe-zone immunity: either party protected → rejected;
  5. same group or same faction → rejected (friendly fire off);
  6. 3D range ≤ 300 m, computed from the last known server positions;
  7. line-of-sight: when an occluder exists for the level, the hit is rejected
     if attacker-eye to head, chest and pelvis are all blocked (see LOS below);
  8. damage sanity: finite, > 0 and ≤ 250 (ceiling); larger values rejected;
  9. single-hit clamp: applied damage is clamped to 150;
  10. rolling 1 s damage budget `damage_budget_per_s` (default 400): the hit is
      clamped to the remaining budget and rejected once exhausted.
- **Relay.** An accepted hit is applied to the target session's server-side
  health, then `OpDamageNotify` is relayed reliably to the victim only, carrying
  the attacker's real session id and the clamped amount actually applied. The
  server persists `characters.dead = 1` when health reaches 0. Sleeper (offline
  player) damage is routed through the same validator, so safe-zone, friendly
  fire, range and sanity gates cannot be bypassed.
- **Victim apply.** The victim's client accepts a relay only when the wire target
  is its own session id (the engine actor id is also accepted). It refuses to
  apply damage inside a safe zone, clamps the hit to current health, applies it
  with `db.actor:change_health(-damage)` and shows a throttled HUD notice
  ("<attacker> hit you"). Death itself is left to the engine's condition update.
- **Audit.** Accepted hits write `damage_applied` rows with both session ids and
  the clamped amount; rejected hits write `damage_rejected` rows with the reason,
  throttled to one per 5 s per attacker so a forged-packet flood cannot fill
  `audit_log`.
- **Limits.** Hit detection is still the client's. The server has no collision
  mesh and does not raycast the shot; it can only test the reported attacker and
  victim positions against the static occluder, so bone id is carried on the
  wire but not used in validation, and damage is applied 1:1 from the
  client-reported power with no armor or hit-location scaling server-side. Levels
  without an occluder fail open and therefore remain wall-shootable.

## Line-of-sight gating

- **Wired and enabled.** `damage.go` calls the shared LOS checker
  (`los_gate.go`) for player-vs-player and player-vs-AI hits; AI melee runs
  through the same `DamageHandler`. `los_enabled` defaults to `true` and
  `los_data_dir` to `zone_los`.
- **Multi-sample check.** Attacker eye (`pos + 1.6`) against the victim's head
  (`+1.6`), chest (`+1.0`) and pelvis (`+0.5`). The hit is rejected only when
  **all three** segments are blocked, so shooting over low cover still lands. A
  rejection is audited with the reason `no line of sight`.
- **Fail-open.** Occluders are loaded on demand per level and cached for the
  process lifetime. A missing, unreadable or corrupt `<level>.occl`, or a level
  name that is unsafe as a file name, allows the hit and logs one warning per
  level. Geometry only ever subtracts hits; it never invents them.
- **Artifacts.** The `l01_escape` occluder (52,967,834 bytes) is produced by
  `tools/levelgeom` from `level.cform` and ships with the release package under
  `server/zone_los/` (not in git). Every other level fails open until its
  occluder is generated.

## Items (authoritative ledger, duplication-proof)

- Wire: `OpItemAction` (0x007D, client → server, reliable, 88 B) and
  `OpItemUpdate` (0x007E, server → client, reliable, 89 B).
- Server tables: `character_inventory` (owner ledger) and `world_items`
  (dropped items with server-assigned ids).
- **Drop**: validate the inventory row has enough count → in ONE SQL transaction
  decrement/remove the row and insert the world item → broadcast the accepted
  update to same-level peers.
- **Pickup**: the server *never* trusts client section/count. It validates the
  world item exists, is on the picker's level, and is within 3 m of the picker's
  last server position; one transaction deletes the row and credits the
  inventory. Concurrent pickups: `DELETE` affected-rows decides the single
  winner; the loser gets `result=1`.
- **Anti-duplication**:
  - Every action carries a client-monotonic `ActionID`; a bounded per-session
    cache makes replays idempotent (replay echoes the cached result, never
    re-applies).
  - Old ActionIDs (below the session floor) without a cache entry are rejected.
  - Item rate limit `item_rate_per_s` (default 5).
  - Corrections resend authoritative inventory sync so divergent clients
    reconcile instead of drifting.
- Verified end-to-end: 12-client load test drop → pickup → duplicate pickup
  rejected, with audit entries.

## Stash, trade and wallet

- Wire: `OpStashAction`/`OpStashResult` (0x0081/0x0082, 84/73 B),
  `OpTradeAction`/`OpTradeResult` (0x0083/0x0084, 76/75 B) and `OpWalletUpdate`
  (0x0085, 4 B). All client actions are reliable; results go to the sender only —
  stash state is never broadcast.
- **Position-keyed stashes.** A stash is addressed by level plus the client's
  raw XYZ rounded onto a 0.5 m grid; the first store at an empty key creates an
  ownerless world stash. The player must be within 5 m of the key and on the
  same level, checked before any create so an out-of-reach key cannot materialise
  an unusable stash. Store debits the inventory and credits the stash; take does
  the reverse, each in one SQLite transaction through the shared stash manager.
- **Trader buy/sell.** The server derives the item movement from its own
  inventory rows: sell removes the held section and credits rubles, buy debits
  rubles and adds the section. The client-asserted `MoneyDelta` is trusted only
  up to `trade_max_money_delta` (200000) per action; a clamped action is echoed
  as `result=corrected` so the client sees the server moved less than requested.
  Insufficient funds/stock is rejected and audited.
- **Wallet.** Money is a server-owned column. Every accepted or rejected trade
  and every join/character-select pushes `OpWalletUpdate` with the absolute
  balance, so an optimistic client-side money mutation is overwritten by the
  authoritative value on the next packet.
- **Replay protection.** Stash and trade actions share the item ledger's
  client-monotonic ActionID cache, stale-ID floor and `item_rate_per_s` budget
  with `OpItemAction`/`OpContainerAction`, so a replayed ActionID echoes its
  cached result and can never re-apply or cross-replay another opcode.
- **Native UI.** The native inventory/stash and trader windows send 0x0081/0x0083
  and consume 0x0082/0x0084/0x0085; the numeric `OpContainerAction` path
  (0x007F/0x0080, SQLite rowid addressing) remains server-side only.

## AI combat

- **Server-owned FSM.** Puppets are simulated on the server (IDLE/PATROL, CHASE,
  ATTACK, ALERT, DEAD); clients only render `OpAIState` (0x007C) and puppet
  health via `ENTITY_ENTER_AOI`. Health, position, aggro, attack timing and death
  are all decided server-side.
- **Hostility.** Faction relation from the persisted matrix: strictly negative =
  enemy, same normalized faction never engages, and a squad with an empty or
  `monster` faction is hostile to everyone. Safe zones suppress the FSM on both
  sides; a safe-zoned player is never a target and a safe-zoned squad never
  engages.
- **AI → player damage.** The FSM emits an attack event; the game validates it
  through the same `ValidateAndApplyDamage` used for PvP (level, safe zone,
  group/faction, range, LOS, sanity, clamp, rolling budget) using a proxy
  session with the AI entity id, then relays `OpDamageNotify` (0x0050) to the
  victim only. A lethal hit persists `characters.dead = 1`.
- **Player → AI damage.** `OpDamageNotify` with `TargetID >= 1_000_000` is
  routed to the puppet path (`ValidateAndApplyPuppetDamage`): attacker session
  and level, ≤ 300 m to the puppet, LOS when an occluder exists, sanity/clamp,
  the same rolling budget. Puppet HP is decremented server-side; non-lethal hits
  re-send `ENTITY_ENTER_AOI` with updated health (0x007C has no health field) and
  a lethal hit starts the corpse timer.
- **No loot.** A dead puppet streams the death animation for
  `ai_corpse_seconds` and is released; it drops nothing, and there is no
  server-side pathfinding around geometry yet.
- **Player claims are validated, never trusted.** As with PvP, a player's
  puppet hit is accepted only when every gate passes; a forged, out-of-range or
  blocked claim is rejected and audited. AI attack events are generated
  server-side and run through the same gates before delivery.

## Configuration keys (zone_server.yaml)

| Key | Default | Purpose |
|---|---|---|
| `damage_budget_per_s` | 400 | sustained hit-registration clamp per attacker (1 s window) |
| `item_rate_per_s` | 5 | item/stash/trade action rate limit |
| `trade_max_money_delta` | 200000 | per-action ruble cap on a client-asserted trade price |
| `ai_combat_enabled` | true | puppet combat FSM master switch (`false` = patrol only) |
| `ai_aggro_radius_m` | 40 | hostile-player acquire radius |
| `ai_attack_range_m` | 2.0 | melee reach |
| `ai_attack_cooldown_ms` | 1500 | melee interval |
| `ai_melee_damage` | 10 | melee damage per swing |
| `ai_corpse_seconds` | 5 | corpse animation time before `ENTITY_LEAVE_AOI` |
| `ai_patrol_resume_s` | 10 | ALERT cool-down before patrol resumes |
| `los_enabled` | true | server-side line-of-sight gating master switch |
| `los_data_dir` | zone_los | directory of per-level `<level>.occl` artifacts |

The movement and lag-switch keys were removed together with the guard; see
Movement above.

## Still client-side (and why)

- Local movement prediction and hit *detection* (latency; the server validates
  the hit claim against range, budget and static occluder geometry but cannot
  raycast the shot itself).
- Animations/FX (cosmetic; wrong values never affect state).
- Safe zone *presentation* (banner), while enforcement is server+client double
  sided (client hooks are defence-in-depth; the server gate is the authority).

## Audit

Accepted/rejected hits, item accept/reject, stash store/take and trade buy/sell
write to `audit_log` with account, action, section, count, world/stash id,
damage amount or rejection reason — the foundation for rollback tooling and
moderation. Rejection rows are throttled per attacker so a forged-packet flood
cannot fill the table.
