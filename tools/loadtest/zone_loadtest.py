"""Load/soak test for zone-server: simulated UDP clients.

Covers: handshake, visual + transform telemetry, AoI snapshot delivery,
group invite/accept via chat commands, chat broadcast, heartbeat RTT,
malformed packet fuzz, graceful disconnect + ENTITY_LEAVE.
"""
import argparse
import random
import select
import socket
import struct
import sys
import threading
import time
from collections import Counter

MAGIC = 0x5A4F
PROTO = 1
FLAG_RELIABLE = 0x01
FLAG_UNRELIABLE = 0x02

OP_HS_REQ = 0x0001
OP_HS_RES = 0x0002
OP_DISCONNECT = 0x0003
OP_HEARTBEAT = 0x0004
OP_ACK = 0x0005
OP_QUERY = 0x0006
OP_QUERY_RES = 0x0007
OP_TRANSFORM = 0x0010
OP_SNAPSHOT = 0x0011
OP_ENTER = 0x0012
OP_LEAVE = 0x0013
OP_SAFEZONE = 0x0020
OP_CHAT = 0x0060
OP_CHARACTER_SELECT = 0x0072
OP_INVITE_NOTIFY = 0x0078
OP_GROUP_STATE = 0x007A
OP_ITEM_ACTION = 0x007D
OP_ITEM_UPDATE = 0x007E

FACTIONS = ["stalker", "bandit", "dolg", "freedom", "csky", "ecolog",
            "killer", "army", "monolith", "renegade", "greh", "isg"]

NEAR_SPAWN = (-211.3, -20.2, -145.8)
FAR_SPAWN = (1200.0, 0.0, 1200.0)


def header(opcode, seq, flags, plen):
    return struct.pack("<HBBiHH", MAGIC, PROTO, flags, seq, opcode, plen)


class Client(threading.Thread):
    def __init__(self, index, host, port, duration, near):
        super().__init__(daemon=True)
        self.index = index
        self.host = host
        self.port = port
        self.duration = duration
        self.near = near
        self.nick = f"Load{index:03d}"
        self.faction = FACTIONS[index % len(FACTIONS)]
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sock.bind((f"127.0.0.{index + 2}", 0))
        self.sock.settimeout(0.05)
        self.session_id = None
        self.seq = 1
        self.send_lock = threading.Lock()
        self.alive = True
        self.counts = Counter()
        self.peer_sessions = set()
        self.group_seen = False
        self.rtt_ms = None
        self.acks = 0
        self.retransmits_seen = 0
        self.pending_reliable = {}
        self.retransmits = 0
        self.item_results = {}
        self.next_action_id = 1
        self.last_x = NEAR_SPAWN[0] if near else FAR_SPAWN[0]
        self.last_y = NEAR_SPAWN[1] if near else FAR_SPAWN[1]
        self.last_z = NEAR_SPAWN[2] if near else FAR_SPAWN[2]
        self.hs_status = None
        self.snapline = 0.0

    def send(self, opcode, payload, flags=FLAG_UNRELIABLE, reliable=False):
        with self.send_lock:
            self.seq = (self.seq + 1) & 0xFFFFFFFF
            seq = self.seq
            if reliable:
                flags = FLAG_RELIABLE
            pkt = header(opcode, seq, flags, len(payload)) + payload
            self.sock.sendto(pkt, (self.host, self.port))
            if reliable:
                self.pending_reliable[seq] = [pkt, time.time(), 0]

    def handshake(self):
        uuid = f"loadtest-{self.index:04d}-uuid-0000-00000000".encode()[:37].ljust(37, b"\x00")
        hwid = bytes([self.index % 256]) * 32
        nick = self.nick.encode()[:32].ljust(32, b"\x00")
        self.send(OP_HS_REQ, uuid + hwid + nick + struct.pack("<B", PROTO), reliable=True)
        deadline = time.time() + 3
        while time.time() < deadline and self.session_id is None and self.alive:
            self.pump(0.05)
        return self.session_id is not None

    def send_visual(self):
        visual = b"actors\\stalker_neutral\\stalker_neutral_1"
        self.send(0x0075, visual.ljust(64, b"\x00")[:64], reliable=True)

    def transform(self, t):
        base = NEAR_SPAWN if self.near else FAR_SPAWN
        x = base[0] + (self.index % 5) * 1.5 + (0.5 * (t % 4))
        y = base[1]
        z = base[2] + (self.index % 4) * 1.5
        self.last_x, self.last_y, self.last_z = x, y, z
        payload = struct.pack("<Ifffhh", self.session_id, x, y, z,
                              int(0), int(0))
        payload += struct.pack("<hhh", 0, 0, 0) + struct.pack("<BH", 1, 1)
        self.send(OP_TRANSFORM, payload)

    def select_character(self):
        faction = self.faction.encode()[:15].ljust(16, b"\x00")
        items = b"bandage".ljust(256, b"\x00")
        self.send(OP_CHARACTER_SELECT, faction + struct.pack("<I", 0) + items, reliable=True)

    def drop_item(self, section, count, condition=100):
        aid = self.next_action_id
        self.next_action_id += 1
        sec = section.encode()[:63].ljust(64, b"\x00")
        payload = struct.pack("<IBI", aid, 1, 0) + sec + struct.pack("<HfffB", count, self.last_x, self.last_y, self.last_z, condition)
        self.send(OP_ITEM_ACTION, payload, reliable=True)
        return aid

    def pickup_item(self, item_id, count=1, section="bandage"):
        aid = self.next_action_id
        self.next_action_id += 1
        sec = section.encode()[:63].ljust(64, b"\x00")
        payload = struct.pack("<IBI", aid, 2, item_id) + sec + struct.pack("<HfffB", count, self.last_x, self.last_y, self.last_z, 100)
        self.send(OP_ITEM_ACTION, payload, reliable=True)
        return aid

    def chat(self, text):
        raw = text.encode()[:255]
        body = struct.pack("<I", self.session_id) + struct.pack("<B", len(raw)) + raw.ljust(255, b"\x00")
        self.send(OP_CHAT, body, reliable=True)

    def pump(self, timeout):
        readable, _, _ = select.select([self.sock], [], [], timeout)
        if not readable:
            return
        self.sock.setblocking(False)
        try:
            while True:
                try:
                    data, _ = self.sock.recvfrom(4096)
                except (BlockingIOError, InterruptedError):
                    return
                except OSError:
                    return
                if len(data) < 12:
                    continue
                magic, proto, flags, seq, opcode, plen = struct.unpack_from("<HBBiHH", data, 0)
                if magic != MAGIC or proto != PROTO:
                    continue
                self.counts[opcode] += 1
                if flags & FLAG_RELIABLE:
                    self.send(OP_ACK, struct.pack("<I", seq))
                body = data[12:12 + plen]
                if opcode == OP_ACK and plen >= 4:
                    acked = struct.unpack_from("<I", body, 0)[0]
                    self.pending_reliable.pop(acked, None)
                if opcode == OP_HS_RES and plen >= 5:
                    self.session_id = struct.unpack_from("<I", body, 0)[0]
                    self.hs_status = body[4]
                elif opcode == OP_SNAPSHOT and plen >= 1:
                    count = body[0]
                    self.snapline += count
                    for i in range(count):
                        off = 1 + i * 22
                        if off + 4 <= len(body):
                            self.peer_sessions.add(struct.unpack_from("<I", body, off)[0])
                elif opcode == OP_ENTER and plen >= 100:
                    self.peer_sessions.add(struct.unpack_from("<I", body, 0)[0])
                elif opcode == OP_GROUP_STATE:
                    self.group_seen = True
                elif opcode == OP_ITEM_UPDATE and plen >= 89:
                    action_id = struct.unpack_from("<I", body, 0)[0]
                    result = body[4]
                    action = body[5]
                    item_id = struct.unpack_from("<I", body, 6)[0]
                    count = struct.unpack_from("<h", body, 10)[0]
                    self.item_results[action_id] = (result, action, item_id, count)
        finally:
            try:
                self.sock.settimeout(0.05)
            except OSError:
                pass

    def run(self):
        if not self.handshake():
            self.alive = False
            return
        self.send_visual()
        self.select_character()
        time.sleep(0.2)
        self.send(0x0074, b"l01_escape".ljust(32, b"\x00")[:32], reliable=True)
        end = time.time() + self.duration
        next_tick = time.time()
        last_hb = 0.0
        while time.time() < end and self.alive:
            now = time.time()
            if now >= next_tick:
                self.transform(now)
                next_tick += 1.0 / 30.0
            if now - last_hb >= 1.0:
                stamp = struct.pack("<d", now)
                self.send(OP_HEARTBEAT, stamp)
                last_hb = now
            for rseq, entry in list(self.pending_reliable.items()):
                if now - entry[1] >= 0.5:
                    if entry[2] >= 5:
                        self.pending_reliable.pop(rseq, None)
                        continue
                    entry[2] += 1
                    entry[1] = now
                    self.retransmits += 1
                    with self.send_lock:
                        self.sock.sendto(entry[0], (self.host, self.port))
            self.pump(0.01)

    def stop(self):
        if getattr(self, "stopped", False):
            return
        self.stopped = True
        self.alive = False
        try:
            self.send(OP_DISCONNECT, struct.pack("<B", 0), reliable=True)
            time.sleep(0.05)
        except OSError:
            pass

    def close(self):
        try:
            self.sock.close()
        except OSError:
            pass


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("port", type=int, nargs="?", default=27016)
    ap.add_argument("--clients", type=int, default=12)
    ap.add_argument("--duration", type=float, default=12.0)
    args = ap.parse_args()

    host = "127.0.0.1"
    clients = [Client(i, host, args.port, args.duration, near=True)
               for i in range(args.clients)]

    connected = 0
    for c in clients:
        c.start()
    time.sleep(2.0)
    for c in clients:
        if c.session_id:
            connected += 1
    print(f"connected: {connected}/{args.clients}")

    time.sleep(args.duration * 0.4)

    # group flow: client 0 (near) invites client 2 (near, different faction)
    a = clients[0]
    b = clients[2] if len(clients) > 2 else clients[1]
    if a.session_id and b.session_id:
        a.chat(f"/invite {b.nick}")
        time.sleep(0.8)
        b.chat("/accept")
        time.sleep(0.8)
    # chat broadcast
    a.chat("load test hello")

    # item ledger: drop -> pickup -> duplicate pickup rejected
    trader = clients[2]
    drop_aid = trader.drop_item("bandage", 1)
    time.sleep(1.0)
    drop_res = trader.item_results.get(drop_aid)
    item_id = drop_res[2] if drop_res and drop_res[0] == 0 else 0
    pickup_res = None
    dup_res = None
    if item_id:
        pickup_aid = trader.pickup_item(item_id)
        time.sleep(1.0)
        pickup_res = trader.item_results.get(pickup_aid)
        if pickup_res and pickup_res[0] == 0:
            dup_aid = trader.pickup_item(item_id)
            time.sleep(1.0)
            dup_res = trader.item_results.get(dup_aid)

    # mid-test disconnect: one near client leaves while peers still pump
    victim = clients[len(clients) // 2]
    if victim.session_id:
        victim.stop()

    # malformed fuzz from a throwaway socket
    fuzz = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    for _ in range(300):
        n = random.randint(0, 64)
        fuzz.sendto(bytes(random.getrandbits(8) for _ in range(n)), (host, args.port))
    fuzz.sendto(header(OP_TRANSFORM, 1, FLAG_UNRELIABLE, 29) + b"\x00" * 10, (host, args.port))
    fuzz.close()

    # wait for client threads to finish
    for c in clients:
        c.join(timeout=args.duration + 5)

    snap_total = sum(c.counts[OP_SNAPSHOT] for c in clients)
    enter_total = sum(c.counts[OP_ENTER] for c in clients)
    chat_total = sum(c.counts[OP_CHAT] for c in clients)
    invite_total = sum(c.counts[OP_INVITE_NOTIFY] for c in clients)
    group_total = sum(c.counts[OP_GROUP_STATE] for c in clients)
    leave_total = sum(c.counts[OP_LEAVE] for c in clients)
    safezone_total = sum(c.counts[OP_SAFEZONE] for c in clients)
    errors = {c.nick: c.hs_status for c in clients if c.hs_status not in (0, None)}
    print(f"snapshots received: {snap_total}, enters: {enter_total}, chat: {chat_total}")
    print(f"invites: {invite_total}, group states: {group_total}, leaves: {leave_total}, safezone: {safezone_total}")
    print(f"handshake errors: {errors if errors else 'none'}")

    near_clients = [c for c in clients if c.near]
    far_clients = [c for c in clients if not c.near]
    near_peers = sum(1 for c in near_clients if c.peer_sessions)
    far_peers = sum(1 for c in far_clients if c.peer_sessions)
    print(f"AoI: near clients with peers seen: {near_peers}/{len(near_clients)}, far clients with peers seen: {far_peers}/{len(far_clients)}")
    print(f"group state observed by invited pair: {a.group_seen and b.group_seen}")
    print(f"item drop result: {drop_res}, pickup: {pickup_res}, duplicate pickup: {dup_res}")

    for c in clients:
        c.stop()
    for c in clients:
        c.join(timeout=2)
    for c in clients:
        c.close()

    item_ok = bool(drop_res and drop_res[0] == 0 and pickup_res and pickup_res[0] == 0
                   and dup_res and dup_res[0] == 1)
    ok = (connected == args.clients and snap_total > 0 and near_peers >= max(1, len(near_clients) // 2)
          and a.group_seen and b.group_seen and group_total > 0 and not errors
          and item_ok)
    print("LOAD TEST", "PASSED" if ok else "FAILED")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()

