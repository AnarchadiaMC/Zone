# Level Travel

Server-side handling of `OpLevelChange` (0x0074) and the initial spawn flow.

## Initial menu spawn

From the main menu the client always starts the first singleplayer game on
`l01_escape` (the engine turns the first `OpLoadLevel` / `OpShowStart` into a
console `start` of the vanilla Cordon). This is an engine limitation: the menu
does not expose a level picker for the co-op client yet. The server documents
this by hosting the initial load on the Cordon and never claiming otherwise.

## In-game travel

Once a level is loaded, the client travels by loading another level and
reporting it with `OpLevelChange`. The report is **client-trusted**: the server
requires no level transition token, exactly like movement. The handler:

1. Rejects only an empty, control-character or >31-byte level name.
2. Sets `CurrentLevel` to the reported name. Any printable level name is
   accepted, including levels absent from `supportedLevels`; the map only
   supplies the best-effort wire `u8` id used by `OpLoadLevel` (unknown names
   fall back to 0).
3. Keeps the client-reported position. Movement is client-authoritative, and
   the first transform after the load refines the position. The server does not
   teleport the player to a level spawn.
4. Resets the per-level caches: the spatial-grid entry follows the accepted
   position, `AoIManager` entries and the AI visibility set for the session are
   dropped, and `Gvid` is cleared so the mover is re-announced on the new level.
5. Sends the mover the new level's safe-zone state (computed from the accepted
   position, so `l02_garbage` reports `sz_garbage_flea` at the flea market).
6. Exchanges `ENTITY_ENTER_AOI`:
   - the mover receives one enter for every same-level peer with `Gvid > 0`
     and a known visual;
   - the mover's own enter is broadcast to same-level peers by the next
     transform or `OpPlayerVisual` once its `Gvid` is set again (never
     immediately, so the reset entity id cannot leak).
7. When the level actually changed, sends exactly one `ENTITY_LEAVE_AOI` for
   the old level to its remaining peers and re-sends the world items in range.

Repeating a report for the current level is not a transition: no leave, no
enter replay, no inventory resync.

## Supported level names

`supportedLevels` maps the canonical Anomaly levels
(`l01_escape`, `l02_garbage`, `l03_agroprom`, `l04_darkvalley`,
`l05_bar_rostok`, `l07_military`, `l08_yantar`, `l09_deadcity`, `k00_marsh`,
`zaton`, `jupiter`) onto wire ids. It is intentionally incomplete and
best-effort: servers with mod levels or different wire ids keep working because
unknown names are accepted and only the id falls back to 0. Safe-zone lookups
use the level name itself, so per-level zones apply to every level that has
entries in `safe_zones`, known or not.

## Tests

`TestLevelTravel_LeaveAndEnterExactlyOnce`,
`TestLevelTravel_BothInNewLevelSeeEachOther`,
`TestLevelTravel_NoCrossLevelTraffic` (game), plus the updated
`TestLevelChange_KeepsClientPosition` and
`TestJoinFlow_UnknownPrintableLevelAccepted`.
