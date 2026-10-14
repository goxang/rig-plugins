#!/usr/bin/env python3
"""Plays every online game against a real server process: python3 test_online.py"""
import asyncio
import base64
import json
import os
import socket
import struct
import subprocess
import sys
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))


class Client:
    def __init__(self, r, w):
        self.r, self.w, self.inbox = r, w, []

    @classmethod
    async def open(cls, port, name):
        r, w = await asyncio.open_connection("127.0.0.1", port)
        key = base64.b64encode(os.urandom(16)).decode()
        w.write(f"GET /ws HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n".encode())
        head = await r.readuntil(b"\r\n\r\n")
        assert b"101" in head.split(b"\r\n")[0], head
        c = cls(r, w)
        await c.send({"t": "join", "name": name})
        return c

    async def send(self, obj):
        data = json.dumps(obj).encode()
        key = os.urandom(4)
        n = len(data)
        head = bytes([0x81]) + (bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + struct.pack(">H", n))
        self.w.write(head + key + bytes(b ^ key[i & 3] for i, b in enumerate(data)))
        await self.w.drain()
        await asyncio.sleep(0.05)

    async def recv(self, timeout=3):
        b0, b1 = await asyncio.wait_for(self.r.readexactly(2), timeout)
        n = b1 & 127
        if n == 126:
            n = struct.unpack(">H", await self.r.readexactly(2))[0]
        return json.loads(await self.r.readexactly(n))

    async def until(self, pred, timeout=5):
        loop = asyncio.get_running_loop()
        end = loop.time() + timeout
        while True:
            m = await self.recv(max(0.05, end - loop.time()))
            if pred(m):
                return m

    async def state(self, pred=lambda m: True, timeout=5):
        return await self.until(lambda m: m.get("t") == "state" and pred(m), timeout)


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


async def run_server(game):
    port = free_port()
    proc = subprocess.Popen([sys.executable, os.path.join(HERE, "server.py"), "--game", game, "--room", "T",
                             "--port", str(port), "--host", "127.0.0.1"], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    proc.stdout.readline()
    return proc, port


def get(port, path):
    with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=3) as r:
        return r.status, r.read()


async def tictactoe(port):
    a, b, c = [await Client.open(port, n) for n in ("a", "b", "c")]
    s = await c.state(lambda m: m["watching"] == 1)
    assert s["seats"] == ["a", "b"], s
    for who, i in ((a, 0), (b, 3), (a, 1), (b, 4), (a, 2)):
        await who.send({"t": "move", "i": i})
    s = await a.state(lambda m: m["winner"] is not None)
    assert s["winner"] == 0 and s["line"] == [0, 1, 2] and s["wins"] == [1, 0], s
    await a.send({"t": "move", "i": 5})
    await a.send({"t": "rematch"})
    s = await a.state(lambda m: m["winner"] is None)
    assert s["board"] == [None] * 9 and s["turn"] == 1, s
    a.w.close()
    s = await c.state(lambda m: m["seats"][0] == "c")
    assert s["seats"] == ["c", "b"] and s["wins"] == [0, 0], s


async def connect4(port):
    a, b = await Client.open(port, "a"), await Client.open(port, "b")
    await b.state(lambda m: None not in m["seats"])
    for who, col in ((a, 0), (b, 1), (a, 0), (b, 1), (a, 0), (b, 1), (a, 0)):
        await who.send({"t": "drop", "c": col})
    s = await a.state(lambda m: m["winner"] is not None)
    assert s["winner"] == 0 and len(s["line"]) == 4, s
    await a.send({"t": "drop", "c": 99})
    await a.send({"t": "drop", "c": "x"})


async def pong(port):
    a, b = await Client.open(port, "a"), await Client.open(port, "b")
    s = await a.state(lambda m: m["phase"] == "play")
    await a.send({"t": "pos", "y": 0.1})
    s = await a.state(lambda m: m["pad"][0] < 100)
    assert s["pad"][0] >= 45, s
    s0 = await a.state()
    s1 = await a.state(lambda m: m["ball"] != s0["ball"])
    assert s1["ball"] != s0["ball"]


async def snake(port):
    a, b = await Client.open(port, "a"), await Client.open(port, "b")
    s = await a.state(lambda m: len(m["snakes"]) == 2 and all(x["a"] for x in m["snakes"]))
    me = next(x for x in s["snakes"] if x["n"] == "a")
    assert len(me["p"]) == 3 and s["food"]
    await a.send({"t": "dir", "d": 0})
    await a.send({"t": "dir", "d": 2})
    died = await a.until(lambda m: m.get("t") == "died", 15)
    assert died["by"], died
    await a.send({"t": "respawn"})
    await a.state(lambda m: next(x for x in m["snakes"] if x["n"] == "a")["a"])


async def draw(port):
    a, b = await Client.open(port, "a"), await Client.open(port, "b")
    first = await a.until(lambda m: m.get("t") == "pick")
    await a.send({"t": "pick", "word": first["options"][0]})
    word = (await a.until(lambda m: m.get("t") == "word"))["word"]
    await a.send({"t": "stroke", "c": 1, "w": 4, "p": [[0.1, 0.1], [0.2, 0.2]]})
    await a.send({"t": "stroke", "c": 1, "w": 4, "p": [[2, "x"]]})
    st = await b.until(lambda m: m.get("t") == "stroke")
    assert st["p"][1] == [0.2, 0.2]
    await b.send({"t": "chat", "text": word.upper()})
    ok = await b.until(lambda m: m.get("t") == "chat" and m["k"] == "ok")
    assert "guessed" in ok["text"]
    s = await b.state(lambda m: m["phase"] == "reveal")
    assert next(p for p in s["players"] if p["n"] == "b")["s"] > 100


async def foxfloe(port):
    status, body = get(port, "/foxfloe/vendor/three.module.js")
    assert status == 200 and body.startswith(b"/**")
    a, b = await Client.open(port, "a"), await Client.open(port, "b")
    await a.state(lambda m: len(m["racers"]) == 2 and m["phase"] == "lobby")
    await a.send({"t": "pos", "d": 999})
    await a.send({"t": "take", "id": 1})
    await b.send({"t": "start"})
    s = await a.state(lambda m: m["phase"] == "countdown")
    assert all(r["in"] and r["d"] == 0 for r in s["racers"]) and s["seed"], s
    await a.state(lambda m: m["phase"] == "racing", 6)
    await a.send({"t": "take", "id": 7})
    await b.send({"t": "take", "id": 7})
    took = await b.until(lambda m: m.get("t") == "took")
    assert took["id"] == 7 and took["by"] != (await b.state())["racers"][1]["id"], took
    await a.send({"t": "pos", "d": 400, "x": 1.5, "v": 18, "s": 900})
    await a.send({"t": "pos", "d": 100})
    await b.send({"t": "pos", "d": "x"})
    s = await b.state(lambda m: next(r for r in m["racers"] if r["n"] == "a")["d"] == 400)
    assert next(r for r in s["racers"] if r["n"] == "b")["d"] == 0, s
    await b.send({"t": "pos", "d": 1500, "s": 1500})
    await a.send({"t": "pos", "d": 5000, "s": 2000})
    s = await a.state(lambda m: m["phase"] == "done")
    pts = {r["n"]: r["pts"] for r in s["racers"]}
    assert pts == {"a": 2000 + 400, "b": 1500 + 600}, pts
    c = await Client.open(port, "c")
    s = await c.state(lambda m: len(m["racers"]) == 3)
    assert not next(r for r in s["racers"] if r["n"] == "c")["in"]

async def main():
    games = {"tictactoe": tictactoe, "connect4": connect4, "pong": pong, "snake": snake, "draw": draw, "foxfloe": foxfloe}
    for game, play in games.items():
        proc, port = await run_server(game)
        try:
            status, body = get(port, "/")
            assert status == 200 and b"common.js" in body, f"{game} page"
            assert json.loads(get(port, "/info")[1])["game"] == game
            for bad in ("/../server.py", "/games.py", "/.x.html", "/nope.js"):
                try:
                    get(port, bad)
                    raise AssertionError(bad)
                except urllib.error.HTTPError as e:
                    assert e.code == 404, bad
            await play(port)
            print("ok", game)
        finally:
            proc.terminate()
            err = proc.stderr.read().decode()
            proc.wait()
            if "Traceback" in err:
                raise AssertionError(err)


if __name__ == "__main__":
    asyncio.run(main())
