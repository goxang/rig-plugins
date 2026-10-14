#!/usr/bin/env python3
"""One room of one online game: serves the game page and the websocket it talks to. Standard library only."""
import argparse
import asyncio
import base64
import hashlib
import json
import signal
import struct
import sys
import time
import traceback
from pathlib import Path

from urllib.parse import parse_qsl

from games import CATALOG

WEB = Path(__file__).resolve().parent / "web"
GUID = b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
MAX_FRAME = 64 * 1024
IDLE = 60
LOOPBACK = ("127.0.0.1", "::1")
TYPES = {".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
         ".json": "application/json", ".png": "image/png", ".glb": "model/gltf-binary", ".wav": "audio/wav"}


def frame(payload, op=1):
    n = len(payload)
    head = bytes([0x80 | op])
    if n < 126:
        head += bytes([n])
    elif n < 65536:
        head += bytes([126]) + struct.pack(">H", n)
    else:
        head += bytes([127]) + struct.pack(">Q", n)
    return head + payload


async def read_frame(reader):
    """Returns (opcode, payload) of one complete message; control frames come back as they arrive."""
    message, first = b"", None
    while True:
        b0, b1 = await reader.readexactly(2)
        op, fin, masked, n = b0 & 15, b0 >> 7, b1 >> 7, b1 & 127
        if not masked:
            raise ValueError("unmasked frame")
        if n == 126:
            n = struct.unpack(">H", await reader.readexactly(2))[0]
        elif n == 127:
            n = struct.unpack(">Q", await reader.readexactly(8))[0]
        if n > MAX_FRAME:
            raise ValueError("frame too large")
        key = await reader.readexactly(4)
        data = await reader.readexactly(n)
        data = bytes(b ^ key[i & 3] for i, b in enumerate(data))
        if op >= 8:
            return op, data
        first = op if first is None else first
        message += data
        if len(message) > MAX_FRAME:
            raise ValueError("message too large")
        if fin:
            return first, message


class Player:
    def __init__(self, pid, name, writer, ip=""):
        self.id, self.name, self.writer, self.ip = pid, name, writer, ip

    def write(self, data):
        t = self.writer.transport
        if t.is_closing():
            return
        if t.get_write_buffer_size() > 2_000_000:
            t.abort()
            return
        self.writer.write(data)


class Room:
    def __init__(self, game, name):
        self.kind, self.name, self.started = game, name, time.time()
        self.players, self.next_id = {}, 1
        self.locked, self.banned = False, set()
        self.cap = CATALOG[game]["cap"]
        self.game = CATALOG[game]["cls"](self)

    def send(self, p, obj):
        p.write(frame(json.dumps(obj, separators=(",", ":")).encode()))

    def broadcast(self, obj):
        data = frame(json.dumps(obj, separators=(",", ":")).encode())
        for p in list(self.players.values()):
            p.write(data)

    def unique(self, name):
        name = " ".join(str(name).split())[:16] or "Player"
        taken = {p.name for p in self.players.values()}
        out, n = name, 2
        while out in taken:
            out, n = f"{name} {n}", n + 1
        return out

    def add(self, name, writer, ip=""):
        p = Player(self.next_id, self.unique(name), writer, ip)
        self.next_id += 1
        self.players[p.id] = p
        return p

    def info(self):
        return {"game": self.kind, "title": CATALOG[self.kind]["title"], "room": self.name,
                "players": len(self.players), "cap": self.cap, "names": [p.name for p in self.players.values()],
                "started": int(self.started), "locked": self.locked}

    def host_id(self):
        return min(self.players, default=None)

    def kick(self, pid):
        p = self.players.get(pid)
        if p is None:
            return False
        if p.ip and p.ip not in LOOPBACK:
            self.banned.add(p.ip)
        p.write(frame(b'{"t":"kicked"}'))
        p.writer.transport.close()
        return True

    def close_all(self):
        self.broadcast({"t": "closing"})
        for p in list(self.players.values()):
            p.writer.transport.close()

    def admin(self, cmd, args):
        """Host and operator commands; returns a result dict, or None for an unknown command."""
        if cmd == "players":
            return {"players": [{"id": p.id, "name": p.name, "host": p.id == self.host_id()} for p in self.players.values()]}
        if cmd == "kick":
            try:
                return {"ok": self.kick(int(args.get("id")))}
            except (TypeError, ValueError):
                return {"ok": False}
        if cmd == "lock":
            self.locked = str(args.get("on", "1")) not in ("0", "false")
            return {"locked": self.locked}
        if cmd == "unban":
            self.banned.clear()
            return {"ok": True}
        return self.game.admin(cmd, args)


def guarded_admin(room, cmd, args):
    try:
        return room.admin(cmd, args)
    except Exception:
        traceback.print_exc(file=sys.stderr)
        return {"error": "failed"}


def guarded(fn, *args):
    try:
        fn(*args)
    except Exception:
        traceback.print_exc(file=sys.stderr)


async def ticker(room):
    while True:
        await asyncio.sleep(room.game.tick)
        if room.players:
            guarded(room.game.step)


async def read_head(reader):
    raw = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), 10)
    lines = raw.decode("latin-1").split("\r\n")
    method, target, _ = lines[0].split(" ", 2)
    path, _, query = target.partition("?")
    headers = {":query": dict(parse_qsl(query))}
    for line in lines[1:]:
        if ":" in line:
            k, v = line.split(":", 1)
            headers[k.strip().lower()] = v.strip()
    return method, path, headers


def respond(writer, status, body=b"", ctype="text/plain; charset=utf-8"):
    head = (f"HTTP/1.1 {status}\r\nContent-Type: {ctype}\r\nContent-Length: {len(body)}\r\n"
            "Cache-Control: no-store\r\nAccess-Control-Allow-Origin: *\r\nConnection: close\r\n\r\n")
    writer.write(head.encode() + body)


def static(room, path):
    if path == "/":
        path = f"/{room.kind}/index.html" if (WEB / room.kind).is_dir() else f"/{room.kind}.html"
    parts = path.split("/")[1:]
    if not parts or any(not p or p.startswith(".") for p in parts):
        return None
    file = WEB.joinpath(*parts)
    return (file.read_bytes(), TYPES[file.suffix]) if file.suffix in TYPES and file.is_file() else None


async def handle(reader, writer, room):
    try:
        method, path, headers = await read_head(reader)
        if "websocket" in headers.get("upgrade", "").lower() and path == "/ws":
            await websocket(reader, writer, room, headers)
        elif method != "GET":
            respond(writer, "405 Method Not Allowed")
        elif path == "/info":
            respond(writer, "200 OK", json.dumps(room.info()).encode(), "application/json")
        elif path.startswith("/admin/"):
            peer = (writer.get_extra_info("peername") or ("",))[0]
            # loopback only, and a header a cross-site page cannot send without a preflight we refuse
            if peer not in LOOPBACK or headers.get("x-rig-admin") != "1":
                respond(writer, "403 Forbidden", b"operator only")
            else:
                out = guarded_admin(room, path[7:], headers[":query"])
                respond(writer, "200 OK" if out is not None else "404 Not Found",
                        json.dumps(out if out is not None else {"error": "unknown command"}).encode(), "application/json")
        else:
            hit = static(room, path)
            if hit:
                respond(writer, "200 OK", *hit)
            else:
                respond(writer, "404 Not Found", b"not found")
    except (asyncio.IncompleteReadError, asyncio.TimeoutError, asyncio.LimitOverrunError, ConnectionError, ValueError):
        pass
    except Exception:
        traceback.print_exc(file=sys.stderr)
    finally:
        try:
            await writer.drain()
        except Exception:
            pass
        writer.close()


async def websocket(reader, writer, room, headers):
    origin, host = headers.get("origin"), headers.get("host", "")
    if origin and origin.split("://", 1)[-1] != host:
        respond(writer, "403 Forbidden", b"cross-origin")
        return
    ip = (writer.get_extra_info("peername") or ("",))[0]
    key = headers.get("sec-websocket-key", "")
    if not key:
        respond(writer, "400 Bad Request", b"no key")
        return
    accept = base64.b64encode(hashlib.sha1(key.encode() + GUID).digest()).decode()
    writer.write(("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                  f"Sec-WebSocket-Accept: {accept}\r\n\r\n").encode())
    player = None
    try:
        while True:
            op, data = await asyncio.wait_for(read_frame(reader), IDLE if player else 10)
            if op == 8:
                break
            if op == 9:
                writer.write(frame(data, 10))
                continue
            if op != 1:
                continue
            try:
                m = json.loads(data)
            except ValueError:
                continue
            if not isinstance(m, dict):
                continue
            if player is None:
                if m.get("t") != "join":
                    break
                if ip in room.banned or (room.locked and ip not in LOOPBACK):
                    writer.write(frame(json.dumps({"t": "banned" if ip in room.banned else "locked"}).encode()))
                    break
                if len(room.players) >= room.cap:
                    writer.write(frame(json.dumps({"t": "full", "cap": room.cap}).encode()))
                    break
                player = room.add(m.get("name", ""), writer, ip)
                room.send(player, {"t": "welcome", "id": player.id, "name": player.name, "game": room.kind,
                                   "room": room.name, "title": CATALOG[room.kind]["title"]})
                guarded(room.game.join, player)
            elif m.get("t") == "host":
                if player.id == room.host_id():
                    guarded_admin(room, str(m.get("cmd", "")), m)
            elif m.get("t") != "ping":
                guarded(room.game.msg, player, m)
    except (asyncio.IncompleteReadError, asyncio.TimeoutError, ConnectionError, ValueError):
        pass
    finally:
        if player:
            room.players.pop(player.id, None)
            guarded(room.game.leave, player)


async def serve(args):
    room = Room(args.game, args.room)
    server = await asyncio.start_server(lambda r, w: handle(r, w, room), args.host, args.port)
    task = asyncio.create_task(ticker(room)) if room.game.tick else None
    stop = asyncio.Event()
    for sig in (signal.SIGTERM, signal.SIGINT):
        asyncio.get_running_loop().add_signal_handler(sig, stop.set)
    print(f"{CATALOG[args.game]['title']} room '{args.room}' on {args.host}:{args.port}", flush=True)
    await stop.wait()
    if task:
        task.cancel()
    server.close()
    room.close_all()
    await asyncio.sleep(0.2)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--game", required=True, choices=sorted(CATALOG))
    ap.add_argument("--room", default="Room")
    ap.add_argument("--port", type=int, default=9100)
    ap.add_argument("--host", default="0.0.0.0")
    args = ap.parse_args()
    args.room = " ".join(args.room.split())[:32] or "Room"
    try:
        asyncio.run(serve(args))
    except OSError as e:
        sys.exit(f"cannot listen on {args.host}:{args.port}: {e.strerror}")


if __name__ == "__main__":
    main()
