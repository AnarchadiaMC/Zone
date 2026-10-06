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

## Replay, rate and corrections

- The item ledger keeps two bounded result rings (item and container) behind
  one shared monotonic ActionID floor and one rolling rate window. A replayed
  `0x007F` echoes its cached `OpContainerUpdate`; reusing an ActionID across
  opcodes is rejected as stale (`IsStale`) and never re-applies.
- Success and failure are sent to the sender only — container contents are
  never broadcast.
- Deposit with insufficient inventory returns `result=2` (corrected) with the
  authoritative negative remaining count and no inventory sync: nothing changed
  server-side, so the correction is authoritative on its own. Other failures
  return `result=1` and force a resync (they may follow a client-local
  mutation, so the client must reconcile). Pure rate-limit rejections never
  force a sync.
- Validation order: known action, section format, non-zero count, stash exists,
  same level, ≤ 5 m from the player's last server position, owner match,
  passcode fail-closed (the wire carries no passcode yet, so protected stashes
  reject). All checks use `StashManager.ValidateAccess`, and each packet is
  self-contained: there is no separate open-stash session state.

## Ownership

- A stash whose `world_stashes.owner_uuid` is non-empty is personal: only the
  requesting character (`session.AccountID`) may interact. A NULL/empty owner
  is an ownerless world stash, allowed to anyone within range. `0x007F`
  enforces this through `ValidateAccess` (`ErrStashAccessDenied`).

## Transaction semantics

Deposit and withdraw are each ONE SQLite transaction serialized with the
existing stash APIs through `StashManager.rmwMu`:

- **Deposit**: lock, load stash level+contents, remove `Count` of `Section`
  from `character_inventory` with the stack-preserving helper
  `RemoveInventoryItemsLocked` (rows are decremented and deleted only at
  zero; `ErrInsufficientItems` changes nothing). The stored condition bucket is
  derived from the inventory row actually consumed — the client `Condition`
  byte only selects which stack is consumed first. Merge into the stash JSON
  entry matching `section + condition bucket` (or append), enforce the 4 KiB /
  256-entry stash cap, update `contents_json`, commit.
- **Withdraw**: lock, load stash, find `section + condition bucket`
  (`ErrItemNotFoundStash`) with enough count (`ErrInsufficientCount`). Remove
  it from the JSON, credit `character_inventory` with the stash bucket's own
  condition (merge by section + bucket, else insert), commit. Concurrent
  withdrawals serialize; the second sees the entry gone and loses cleanly.

`StashItem` carries `condition` (0-100, `omitempty`), so stash JSON without the
field decodes to bucket 0. All live paths merge by section + condition bucket,
so a 5% item can never repair a 100% stack.

## Condition provenance

- Drop (`0x007D` action=1): the world item's condition is the removed inventory
  row's condition; the packet's `Condition` only hints which stack to consume.
- Pickup (`0x007D` action=2): the credited row gets the world item's
  condition; merges key section + bucket.
- Stash deposit/withdraw: derived from the inventory row / stash bucket
  respectively.

## Consumption

`OpItemAction` action=3 runs `DB.ConsumeItem` (one transaction, same
remove-in-id-order helper as drop), then echoes `OpItemUpdate{Action:3,
Result:0, Count:-N}` to the sender only. Insufficient stock returns
`result=2` with the negative authoritative remaining count and no inventory
sync (server state did not change); a rejected consume for any other reason
forces a sync so a ghost effect cannot survive. Replays echo the cached update;
nothing is removed twice.

## World items

- **Join/level-change sync**: a client receives one reliable `OpItemUpdate`
  (result=0, action=1 drop shape) per persisted `world_items` row inside the
  AoI radius of its position on the current level.
- **AoI broadcasts**: drop/pickup updates go to the actor (always, for the
  ledger ack) and to same-level sessions inside the AoI radius via the spatial
  grid, not to every player on the level.
- **TTL**: `world_item_ttl_min` (omitted = 60, `0` disables) expires rows via a
  throttled 30 s sweep; same-level clients receive a pickup-shaped
  `OpItemUpdate` with `count=0` to remove the visual.
- **Cap**: `world_item_max_per_level` (default 500) rejects new drops beyond
  the per-level row count inside the drop transaction; the inventory is
  untouched and the client is resynced.

## Ammo limitation

The wire protocol has no ammo field, so a credit (pickup or stash withdraw) of
a section whose existing inventory row is a single object with
`ammo_current > 0` adds the credited count to `ammo_current` instead of
materialising `count` zero-ammo copies. Sections with no such row are credited
as plain stack copies; the database holds no per-section type table, so this
residual ambiguity is documented (see `database.CreditInventoryLocked`).

## Audit

`DB.InsertAudit` records `item_deposit`, `item_withdraw` and `item_consume`
with container id, section and count in the detail string, plus
`item_deposit_rejected`, `item_withdraw_rejected`, `item_consume_rejected` and
`item_container_rejected` (access-gate) rejections.

## Tests

`TestContainerDepositWithdrawRoundTrip`, `TestContainerDepositConditionBuckets`,
`TestContainerDepositInsufficientRejected`,
`TestContainerOutOfRangeAndWrongLevelRejected`,
`TestContainerReplaySharesItemLedgerCache`,
`TestContainerConcurrentWithdrawSingleWinner`, `TestContainerRateLimit`,
`TestConsumeRemovesExactlyOnce`,
`TestStashConditionRoundTripPreservesInventoryCondition`,
`TestStashWithdrawCreditsAmmoRounds`, `TestValidateAccessRejectsForeignOwner`,
`TestItemDropConditionDerivedFromInventory`,
`TestItemDropCorrectionDoesNotResyncInventory`,
`TestItemPickupMergesByConditionBucket`, `TestItemPickupRejectedForcesSync`,
`TestItemDropPerLevelCapRejected`, `TestItemBroadcastAoIOnly`,
`TestItemPickupCreditsAmmoRounds`, `TestWorldItemTTLSweep`,
`TestJoinFlow_SendsWorldItemsInAoI`, `TestJoinFlow_SameUUIDEvictsOldSession`,
plus the protocol layout/round-trip tests. The existing item-ledger and stash
suites stay green.
