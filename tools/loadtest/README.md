# Zone Load/SOAK Test

`zone_loadtest.py` is a self-contained UDP load harness for `zone-server`. It
spawns concurrent simulated clients that perform the real binary protocol
handshake and then exercise the full session lifecycle.

## Coverage

- Handshake (UUID/HWID/nickname) and reconnect/retransmit path
- Visual telemetry and 30 Hz transform updates
- AoI snapshot delivery with near/far spawn split
- Group invite/accept via chat commands, plus chat broadcast
- Heartbeat RTT tracking
- Item drop, pickup, and duplicate-pickup rejection
- AI AoI replication (observed through snapshots/entity enters)
- Malformed packet fuzz (300 random datagrams + one truncated transform)
- Graceful disconnect and `ENTITY_LEAVE` propagation

## AI combat note

The simulated clients spawn near the Cordon, where the four seeded AI squads
patrol. With `ai_combat_enabled: true` (the default) a hostile squad can acquire
a test client, chase and melee it, producing `OpDamageNotify` relays and puppet
aggro/chase state in the observed traffic. That is expected and does not change
the pass criteria below; the harness tolerates damage packets. To keep a run
strictly patrol-only, start the server with `ai_combat_enabled: false` in
`zone_server.yaml`.

## Prerequisites

- Python 3.10 or newer (standard library only; no third-party packages)
- A running `zone-server` reachable on the loopback interface

## Run the server

Build and start the server first. By default it listens on UDP port `27015`
(`port:` in `zone_server.yaml`).

```bat
cd zone-server
make build
zone-server.exe
```

Or with an explicit port:

```bat
zone-server.exe --port 27015 --db-path zone_world.db
```

## Run the harness

```bat
python tools/loadtest/zone_loadtest.py 27015 --clients 12 --duration 12
```

Usage: `python zone_loadtest.py <port> --clients N --duration S`

- `<port>` is the server's UDP port (required in practice; the built-in
  default is `27016`).
- `--clients N` defaults to `12`. Use at least 3 clients: the group flow
  targets client 2 and the item flow runs on client 2.
- `--duration S` defaults to `12.0` seconds. The group, item, and fuzz phases run
  inside that window, so keep `S >= 8`.

All clients bind to distinct loopback source addresses (`127.0.0.2`,
`127.0.0.3`, ...). Windows and Linux route the entire `127.0.0.0/8` block to
the loopback interface, so no interface aliases or routes are needed. The
server must listen on `127.0.0.1` or `0.0.0.0`; a container that only forwards
port `27015` to a non-loopback address will not receive the simulated clients.

## Expected output

A healthy run prints counters and ends with `LOAD TEST PASSED` (exit code 0):

```text
connected: 12/12
snapshots received: 4213, enters: 96, chat: 14
invites: 2, group states: 8, leaves: 3, safezone: 24
handshake errors: none
AoI: near clients with peers seen: 6/6, far clients with peers seen: 0/6
group state observed by invited pair: True
item drop result: (0, 1, 1234, 1), pickup: (0, 2, 1234, 0), duplicate pickup: (1, 2, 1234, 0)
LOAD TEST PASSED
```

Counter values vary per run. `FAILED` (exit code 1) means at least one of the
PASS conditions below was not met:

- all clients connected with a zero handshake status
- snapshots received and near-spawn clients observed peers
- both invited clients observed group state
- drop succeeded, pickup succeeded, and the duplicate pickup was rejected
  (result code `1`)
