# Authority Model — Movement, Hits, Items

How Zone Online decides what is true, what is validated, and what the client is
allowed to say. Written for the v0.5 authority wave with the movement guard
removed and hit registration added.

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
  is server-authoritative.
- **Server validation** (`damage.go`) per hit, in order:
  1. attacker and target sessions exist; the attacker session is resolved from
     the UDP source address, and the packet's AttackerID/TargetID must match
     both sessions (session mismatch rejected);
  2. attacker and target on the same level (cross-level rejected);
  3. self-damage rejected;
  4. safe-zone immunity: either party protected → rejected;
  5. same group or same faction → rejected (friendly fire off);
  6. 3D range ≤ 300 m, computed from the last known server positions;
  7. damage sanity: finite, > 0 and ≤ 250 (ceiling); larger values rejected;
  8. single-hit clamp: applied damage is clamped to 150;
  9. rolling 1 s damage budget `damage_budget_per_s` (default 400): the hit is
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
  mesh and does not raycast, so wall-shooting is unverifiable until LOS is
  enabled; bone id is carried on the wire but not used in validation; damage is
  applied 1:1 from the client-reported power with no armor or hit-location
  scaling server-side.

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

## Configuration keys (zone_server.yaml)

| Key | Default | Purpose |
|---|---|---|
| `damage_budget_per_s` | 400 | sustained hit-registration clamp per attacker (1 s window) |
| `item_rate_per_s` | 5 | item action rate limit |

The movement and lag-switch keys were removed together with the guard; see
Movement above.

## Still client-side (and why)

- Local movement prediction and hit detection (latency; the server validates the
  hit claim but cannot raycast it).
- Animations/FX (cosmetic; wrong values never affect state).
- Safe zone *presentation* (banner), while enforcement is server+client double
  sided (client hooks are defence-in-depth; the server gate is the authority).

## Audit

Accepted/rejected hits and item accept/reject write to `audit_log` with account,
action, section, count, world item id, damage amount or rejection reason — the
foundation for rollback tooling and moderation.
