# Inventory V2 — Containers and Consumption

Extends the authoritative item ledger (`0x007D`/`0x007E`) with container
transfers (`0x007F`/`0x0080`) and inventory consumption (`0x007D` action=3).
Built on the same per-session ActionID idempotency cache, monotonic replay
floor and `item_rate_per_s` limiting as drops/pickups.

## Wire (frozen v5)

| Packet | Op | Dir | Flags | Size |
|---|---|---|---|---|
| `OpContainerAction` | `0x007F` | C->S | reliable | 88 B: `ActionID u32`, `ContainerID u32`, `Action u8` (1=deposit, 2=withdraw), `Section [64]byte`, `Count u16`, `X f32`, `Y f32`, `Z f32`, `Condition u8` |
| `OpContainerUpdate` | `0x0080` | S->C | reliable | 77 B: `ActionID u32`, `Result u8` (0=ok, 1=rejected, 2=corrected), `Action u8`, `ContainerID u32`, `Count i16` (signed delta), `Section [64]byte`, `Condition u8` |

Consumption reuses `OpItemAction` action=3 and echoes `OpItemUpdate` action=3
with a negative count; the client applies the local effect (healing, etc.).

## Container ID mapping

`world_stashes.stash_id` is already `INTEGER PRIMARY KEY AUTOINCREMENT` (SQLite
rowid alias), so the wire u32 `ContainerID` is that id directly — no mapping
table, no change to the existing stash APIs. Auto-created stashes
(`SaveStash(0, ...)`) also receive a rowid, so every stash is addressable.
`OpStashInteract` (`0x0040`) keeps working against the same ids.

## Replay, rate and corrections

- The item ledger keeps two bounded result rings (item and container) behind
  one shared monotonic ActionID floor and one rolling rate window. A replayed
  `0x007F` echoes its cached `OpContainerUpdate`; reusing an ActionID across
  opcodes is rejected as stale (`IsStale`) and never re-applies.
- Success and failure are sent to the sender only — container contents are
  never broadcast.
- Deposit with insufficient inventory returns `result=2` (corrected) with the
  authoritative negative remaining count plus a forced `OpInventorySync`;
  other failures return `result=1`.
- Validation order: known action, section format, non-zero count, stash exists,
  same level, ≤ 5 m from the player's last server position, passcode fail-closed
  (the wire carries no passcode yet, so protected stashes reject). All checks
  use `StashManager.ValidateAccess`.

## Transaction semantics

Deposit and withdraw are each ONE SQLite transaction serialized with the
existing stash APIs through `StashManager.rmwMu`:

- **Deposit**: lock, load stash level+contents, sum and remove `Count` of
  `Section` from `character_inventory` in id order (insufficient →
  `ErrInsufficientItems`, nothing changed), merge into the stash JSON entry
  matching `section + condition bucket` (or append), enforce the 4 KiB /
  256-entry stash cap, update `contents_json`, commit.
- **Withdraw**: lock, load stash, find `section + condition bucket`
  (`ErrItemNotFoundStash`) with enough count (`ErrInsufficientCount`), remove
  it from the JSON, credit `character_inventory` (merge into an existing
  same-section row, else insert with `condition/100`), commit. Concurrent
  withdrawals serialize; the second sees the entry gone and loses cleanly.

`StashItem` gained `condition` (0-100, `omitempty`), so legacy stash JSON
without the field decodes to bucket 0 and the legacy `ModifyStashItem` /
`StoreStashItem` section-only behavior is unchanged.

## Consumption

`OpItemAction` action=3 runs `DB.ConsumeItem` (one transaction, same
remove-in-id-order helper as drop), then echoes `OpItemUpdate{Action:3,
Result:0, Count:-N}` to the sender only. Insufficient stock returns
`result=2` with the negative authoritative remaining count and forces an
inventory sync. Replays echo the cached update; nothing is removed twice.

## Audit

`DB.InsertAudit` records `item_deposit`, `item_withdraw`, `item_consume` with
container id, section and count in the detail string, plus
`item_deposit_rejected`, `item_withdraw_rejected`, `item_consume_rejected`
(with the failure cause). `item_container_rejected` covers stash lookup and
access-gate failures.

## Tests

`TestContainerDepositWithdrawRoundTrip`, `TestContainerDepositConditionBuckets`,
`TestContainerDepositInsufficientRejected`,
`TestContainerOutOfRangeAndWrongLevelRejected`,
`TestContainerReplaySharesItemLedgerCache`,
`TestContainerConcurrentWithdrawSingleWinner`, `TestContainerRateLimit`,
`TestConsumeRemovesExactlyOnce`, plus protocol layout/round-trip tests. The
existing item-ledger and stash suites stay green.
