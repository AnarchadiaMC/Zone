# Authority Model — Movement, Damage, Items

How Zone Online decides what is true, what is validated, and what the client is
allowed to say. Written for the v0.5 authority wave.

---

## Principles

1. **The server owns persistent state.** Characters, faction, money, inventory,
   world items, stashes, groups, safe zone membership, and positions are decided
   server-side and persisted in SQLite (WAL).
2. **The client is a presenter and an input device.** It renders, plays sounds,
   and reports intent (movement, item actions, damage events) which the server
   validates before applying.
3. **Fast-paced gunplay stays client-side for latency reasons.** See "Damage"
   below for what the server can and cannot verify without world geometry.
4. **Every client claim is rate-limited, bounded, and auditable.**

---

## Movement

- Clients send `ClientTransform` (0x0010, 29 B) at 30 Hz: position, yaw/pitch,
  velocity placeholder, anim flags, graph vertex.
- The server keeps a bounded per-session **transform history ring** (~2 s, zero
  alloc). The server position is authoritative and persisted via periodic
  checkpoints.
- **Displacement guard**: displacement since the last accepted transform must be
  ≤ `max_speed_mps` × elapsed + `correction_tolerance_m` (defaults 25 m/s, 2 m).
  Violations are rejected and answered with `OpPositionCorrection` (0x007B,
  x/y/z, unreliable). The client snaps at >8 m deviation, otherwise blends over
  ~0.5 s. Corrections below 1 m are ignored to avoid fighting normal movement.
- **Lag-switch detection**: a transform gap > `lagswitch_gap_ms` (1500) while
  the session is otherwise alive (heartbeats/ACKs keep arriving) counts as a
  strike; `lagswitch_strikes` (3) within 60 s kicks with an audit entry
  ("network abuse suspected"). Plain packet loss — where heartbeats also stop —
  never strikes. Deliberate tradeoff: a lag switch that blocks *all* traffic
  escapes the strike but also freezes the player out of the world, so it cannot
  be used to gain position.
- **Client smoothing**: remote proxies are Hermite-interpolated off a 100 ms
  jitter buffer with velocity extrapolation up to 250 ms / 1.5 m when packets
  lapse, blending back over 150 ms when data resumes.

## Damage

- Hit detection stays **client-side** because 30 Hz server-side rewind+raycast
  is both heavy and impossible without server-side world geometry (the server
  holds no collision mesh). This is the honest limit; the mitigation stack makes
  abuse expensive rather than pretending it is solved.
- Server validation per damage event (`OpDamageNotify` 0x0050):
  - Either party in a safe zone → rejected.
  - Same faction or same group → rejected (friendly fire off).
  - Range ≤ 300 m.
  - Attacker inside a **lag-switch kill window** (strike within the last 5 s) →
    rejected.
  - Attacker displacement over the last 1 s exceeding the speed budget
    (stall-then-burst) → rejected.
  - Rolling 1 s **damage budget** `damage_budget_per_s` (default 400) clamps
    sustained output; single-hit cap still applies.
- Melee stays viable: budgets are generous and no fire event is required.
- **Roadmap**: extract level collision/heightfield data to enable server-side
  line-of-sight and rewind verification (this is the real fix; tracked in
  ROADMAP.md). Optional `OpActionFire` telemetry (origin/direction per shot)
  would enable post-hoc audit once geometry exists.

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
| `max_speed_mps` | 25 | movement budget |
| `correction_tolerance_m` | 2.0 | allowed slack before correction |
| `lagswitch_gap_ms` | 1500 | transform-gap signature threshold |
| `lagswitch_strikes` | 3 | strikes before kick (60 s window) |
| `damage_budget_per_s` | 400 | sustained damage clamp per attacker |
| `item_rate_per_s` | 5 | item action rate limit |

## Still client-side (and why)

- Local movement prediction and hit detection (latency).
- Animations/FX (cosmetic; wrong values never affect state).
- Safe zone *presentation* (banner), while enforcement is server+client double
  sided (client hooks are defence-in-depth; the server gate is the authority).

## Audit

Item accept/reject and lag-switch kicks write to `audit_log` with account,
action, section, count, world item id, or kick reason — the foundation for
rollback tooling and moderation.
