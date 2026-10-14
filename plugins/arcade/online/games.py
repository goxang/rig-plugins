"""Game rules for the online rooms. A Game owns the state of one room; server.py owns the sockets."""
import random
import time
from math import cos, sin
from collections import Counter, deque


def num(v, lo, hi, default=None):
    if isinstance(v, bool) or not isinstance(v, (int, float)) or v != v:
        return default
    return max(lo, min(hi, v))


def integer(v, lo, hi):
    n = num(v, lo, hi)
    return None if n is None or n != int(n) else int(n)


class Game:
    tick = 0.0

    def __init__(self, room):
        self.room = room

    def join(self, p): ...
    def leave(self, p): ...
    def msg(self, p, m): ...
    def step(self): ...
    def admin(self, cmd, args): return None


class Seated(Game):
    """Two seats for the first two players, everyone else watches; a freed seat goes to the next in line."""

    def __init__(self, room):
        super().__init__(room)
        self.seats = [None, None]
        self.wins = [0, 0]
        self.rounds = 0
        self.reset()

    def seat_of(self, p):
        return self.seats.index(p.id) if p.id in self.seats else -1

    def fill(self):
        changed = False
        for i in range(2):
            if self.seats[i] in self.room.players:
                continue
            waiting = (q.id for q in self.room.players.values() if q.id not in self.seats)
            nxt = next(waiting, None)
            changed |= nxt != self.seats[i]
            self.seats[i] = nxt
        if changed:
            self.wins = [0, 0]
            self.rounds = 0
            self.reset()
        return changed

    def names(self):
        return [self.room.players[i].name if i in self.room.players else None for i in self.seats]

    def join(self, p):
        self.fill()
        self.push()

    def leave(self, p):
        self.fill()
        self.push()

    def push(self):
        self.room.broadcast({"t": "state", "seats": self.names(), "ids": self.seats, "wins": self.wins,
                             "watching": max(0, len(self.room.players) - 2), **self.snapshot()})

    def reset(self): ...
    def snapshot(self): return {}


class TicTacToe(Seated):
    LINES = [(0, 1, 2), (3, 4, 5), (6, 7, 8), (0, 3, 6), (1, 4, 7), (2, 5, 8), (0, 4, 8), (2, 4, 6)]

    def reset(self):
        self.board = [None] * 9
        self.turn = self.rounds % 2
        self.winner = None
        self.line = None

    def snapshot(self):
        return {"board": self.board, "turn": self.turn, "winner": self.winner, "line": self.line}

    def msg(self, p, m):
        seat = self.seat_of(p)
        if m.get("t") == "rematch" and seat >= 0 and self.winner is not None:
            self.rounds += 1
            self.reset()
        elif m.get("t") == "move" and seat == self.turn and self.winner is None and None not in self.seats:
            i = integer(m.get("i"), 0, 8)
            if i is None or self.board[i] is not None:
                return
            self.board[i] = seat
            for line in self.LINES:
                if all(self.board[c] == seat for c in line):
                    self.winner, self.line = seat, list(line)
                    self.wins[seat] += 1
            if self.winner is None:
                self.winner = "draw" if None not in self.board else None
            self.turn = 1 - seat
        else:
            return
        self.push()


class ConnectFour(Seated):
    COLS, ROWS = 7, 6

    def reset(self):
        self.grid = [[None] * self.COLS for _ in range(self.ROWS)]
        self.turn = self.rounds % 2
        self.winner = None
        self.line = None

    def snapshot(self):
        return {"grid": self.grid, "turn": self.turn, "winner": self.winner, "line": self.line}

    def four(self, r, c, seat):
        for dr, dc in ((0, 1), (1, 0), (1, 1), (1, -1)):
            cells = [(r, c)]
            for sign in (1, -1):
                rr, cc = r + dr * sign, c + dc * sign
                while 0 <= rr < self.ROWS and 0 <= cc < self.COLS and self.grid[rr][cc] == seat:
                    cells.append((rr, cc))
                    rr, cc = rr + dr * sign, cc + dc * sign
            if len(cells) >= 4:
                return [list(x) for x in cells]
        return None

    def msg(self, p, m):
        seat = self.seat_of(p)
        if m.get("t") == "rematch" and seat >= 0 and self.winner is not None:
            self.rounds += 1
            self.reset()
        elif m.get("t") == "drop" and seat == self.turn and self.winner is None and None not in self.seats:
            c = integer(m.get("c"), 0, self.COLS - 1)
            if c is None:
                return
            r = next((r for r in range(self.ROWS - 1, -1, -1) if self.grid[r][c] is None), None)
            if r is None:
                return
            self.grid[r][c] = seat
            self.line = self.four(r, c, seat)
            if self.line:
                self.winner = seat
                self.wins[seat] += 1
            elif all(self.grid[0][x] is not None for x in range(self.COLS)):
                self.winner = "draw"
            self.turn = 1 - seat
        else:
            return
        self.push()


class Pong(Seated):
    tick = 0.02
    W, H = 800, 500
    PW, PH, PX = 12, 90, 24
    R = 8
    PADDLE_SPEED = 460
    WIN_AT = 7

    def reset(self):
        self.pad = [self.H / 2, self.H / 2]
        self.dir = [0, 0]
        self.score = [0, 0]
        self.phase = "wait" if None in self.seats else "serve"
        self.serve_at = time.monotonic() + 1.5
        self.last = time.monotonic()
        self.center_ball(self.rounds % 2)

    def center_ball(self, toward):
        self.bx, self.by = self.W / 2, self.H / 2
        self.vx = (1 if toward == 1 else -1) * 360
        self.vy = random.choice((-1, 1)) * random.uniform(60, 180)

    def snapshot(self):
        return {"phase": self.phase, "score": self.score, "pad": [round(y, 1) for y in self.pad],
                "ball": [round(self.bx, 1), round(self.by, 1)], "w": self.W, "h": self.H,
                "pw": self.PW, "ph": self.PH, "px": self.PX, "r": self.R, "win": self.WIN_AT,
                "serve_in": max(0, round(self.serve_at - time.monotonic(), 1)) if self.phase == "serve" else 0}

    def msg(self, p, m):
        seat = self.seat_of(p)
        if seat < 0:
            return
        kind = m.get("t")
        if kind == "dir":
            d = integer(m.get("d"), -1, 1)
            if d is not None:
                self.dir[seat] = d
        elif kind == "pos":
            y = num(m.get("y"), 0, 1)
            if y is not None:
                self.pad[seat] = self.clamp(y * self.H)
                self.dir[seat] = 0
        elif kind == "rematch" and self.phase == "over":
            self.rounds += 1
            self.reset()
            self.push()

    def clamp(self, y):
        return max(self.PH / 2, min(self.H - self.PH / 2, y))

    def step(self):
        now = time.monotonic()
        dt = min(now - self.last, 0.05)
        self.last = now
        if self.phase == "wait":
            if None not in self.seats:
                self.phase, self.serve_at = "serve", now + 1.5
                self.push()
            return
        if None in self.seats:
            self.phase = "wait"
            self.push()
            return
        for i in range(2):
            self.pad[i] = self.clamp(self.pad[i] + self.dir[i] * self.PADDLE_SPEED * dt)
        if self.phase == "serve":
            if now >= self.serve_at:
                self.phase = "play"
            self.push()
            return
        if self.phase != "play":
            return
        self.bx += self.vx * dt
        self.by += self.vy * dt
        if self.by < self.R:
            self.by, self.vy = self.R, abs(self.vy)
        elif self.by > self.H - self.R:
            self.by, self.vy = self.H - self.R, -abs(self.vy)
        self.paddle_hit(0)
        self.paddle_hit(1)
        for scorer, out in ((1, self.bx < 0), (0, self.bx > self.W)):
            if out:
                self.score[scorer] += 1
                if self.score[scorer] >= self.WIN_AT:
                    self.phase = "over"
                    self.wins[scorer] += 1
                else:
                    self.phase, self.serve_at = "serve", now + 1.2
                    self.center_ball(1 - scorer)
                break
        self.push()

    def paddle_hit(self, seat):
        left = seat == 0
        if (self.vx < 0) != left:
            return
        face = self.PX + self.PW if left else self.W - self.PX - self.PW
        near = self.bx - self.R <= face if left else self.bx + self.R >= face
        behind = self.bx < self.PX - self.PW if left else self.bx > self.W - self.PX + self.PW
        if not near or behind or abs(self.by - self.pad[seat]) > self.PH / 2 + self.R:
            return
        offset = (self.by - self.pad[seat]) / (self.PH / 2 + self.R)
        speed = min(1000, (self.vx ** 2 + self.vy ** 2) ** 0.5 * 1.06)
        angle = offset * 0.95
        self.vx = cos(angle) * speed * (1 if left else -1)
        self.vy = sin(angle) * speed
        self.bx = face + self.R if left else face - self.R


DIRS = [(0, -1), (1, 0), (0, 1), (-1, 0)]


class Snake(Game):
    tick = 0.11
    W, H = 48, 32
    COLORS = 10

    def __init__(self, room):
        super().__init__(room)
        self.snakes = {}
        self.food = set()
        self.best = {}
        self.next_color = 0
        self.fill_food()

    def join(self, p):
        self.snakes[p.id] = {"name": p.name, "color": self.next_color % self.COLORS, "cells": deque(),
                             "dir": 1, "q": [], "alive": False, "kills": 0, "grow": False, "next": None}
        self.next_color += 1
        self.spawn(self.snakes[p.id])
        self.push()

    def leave(self, p):
        self.snakes.pop(p.id, None)

    def msg(self, p, m):
        sn = self.snakes.get(p.id)
        if sn is None:
            return
        if m.get("t") == "dir" and sn["alive"]:
            d = integer(m.get("d"), 0, 3)
            last = sn["q"][-1] if sn["q"] else sn["dir"]
            if d is not None and d != last and d != (last + 2) % 4 and len(sn["q"]) < 3:
                sn["q"].append(d)
        elif m.get("t") == "respawn" and not sn["alive"]:
            self.spawn(sn)

    def occupied(self):
        cells = set(self.food)
        for sn in self.snakes.values():
            cells.update(sn["cells"])
        return cells

    def spawn(self, sn):
        taken = self.occupied()
        heads = [s["cells"][0] for s in self.snakes.values() if s["alive"]]
        for _ in range(80):
            x, y = random.randint(6, self.W - 7), random.randint(6, self.H - 7)
            d = 1 if x < self.W / 2 else 3
            dx = DIRS[d][0]
            cells = [(x, y), (x - dx, y), (x - 2 * dx, y)]
            if any(c in taken for c in cells) or any(abs(hx - x) + abs(hy - y) < 8 for hx, hy in heads):
                continue
            sn.update(cells=deque(cells), dir=d, q=[], alive=True, grow=False)
            self.food.difference_update(cells)
            return True
        return False

    def fill_food(self):
        alive = sum(1 for s in self.snakes.values() if s["alive"])
        want = 14 + 3 * alive
        taken = self.occupied()
        tries = 0
        while len(self.food) < want and tries < 200:
            tries += 1
            c = (random.randrange(self.W), random.randrange(self.H))
            if c not in taken:
                self.food.add(c)
                taken.add(c)

    def step(self):
        alive = [s for s in self.snakes.values() if s["alive"]]
        for sn in alive:
            if sn["q"]:
                sn["dir"] = sn["q"].pop(0)
            dx, dy = DIRS[sn["dir"]]
            hx, hy = sn["cells"][0]
            sn["next"] = (hx + dx, hy + dy)
            sn["grow"] = sn["next"] in self.food
        owner = {}
        for sn in alive:
            body = list(sn["cells"])
            for c in body if sn["grow"] else body[:-1]:
                owner[c] = sn
        heads = Counter(s["next"] for s in alive)
        dead = []
        for sn in alive:
            nx, ny = sn["next"]
            if not (0 <= nx < self.W and 0 <= ny < self.H):
                dead.append((sn, "the wall"))
            elif sn["next"] in owner:
                hit = owner[sn["next"]]
                dead.append((sn, "yourself" if hit is sn else hit["name"]))
                if hit is not sn:
                    hit["kills"] += 1
            elif heads[sn["next"]] > 1:
                dead.append((sn, "a head-on crash"))
        gone = {id(sn) for sn, _ in dead}
        for sn, cause in dead:
            self.best[sn["name"]] = max(self.best.get(sn["name"], 0), len(sn["cells"]))
            self.food.update(list(sn["cells"])[::2])
            sn["cells"] = deque()
            sn["alive"] = False
            pid = next((i for i, s in self.snakes.items() if s is sn), None)
            if pid in self.room.players:
                self.room.send(self.room.players[pid], {"t": "died", "by": cause})
        for sn in alive:
            if id(sn) in gone:
                continue
            sn["cells"].appendleft(sn["next"])
            if sn["grow"]:
                self.food.discard(sn["next"])
            else:
                sn["cells"].pop()
        self.fill_food()
        self.push()

    def push(self):
        W = self.W
        rows = [{"id": i, "n": s["name"], "c": s["color"], "a": s["alive"], "s": len(s["cells"]),
                 "k": s["kills"], "b": max(self.best.get(s["name"], 0), len(s["cells"])),
                 "p": [y * W + x for x, y in s["cells"]]} for i, s in self.snakes.items()]
        self.room.broadcast({"t": "state", "w": W, "h": self.H, "snakes": rows,
                             "food": [y * W + x for x, y in self.food]})


WORDS = """apple bridge castle dragon rocket pizza guitar window pencil turtle bicycle volcano pirate garden
lantern mirror anchor balloon camera diamond engine feather glasses hammer island jungle kettle ladder magnet
needle orange parrot rainbow saddle tractor umbrella violin wallet zebra bottle candle cloud dolphin elephant
flower giraffe helmet igloo jacket kangaroo lemon mountain notebook octopus penguin queen robot spider
tornado unicorn vampire whale yacht server database deadline docker bug keyboard laptop firewall router
password payment terminal merchant receipt invoice wallet coffee meeting calendar headphones monitor cable
battery satellite microphone sandwich pancake cactus snowman skeleton scissors sunglasses telescope trophy
backpack fireworks lighthouse skateboard hamburger butterfly dinosaur treasure compass""".split()


class Draw(Game):
    tick = 0.5
    PICK, DRAW, REVEAL = 12, 75, 6
    TURNS_EACH = 2

    def __init__(self, room):
        super().__init__(room)
        self.scores = {}
        self.order = []
        self.phase = "lobby"
        self.deadline = 0
        self.turn = 0
        self.drawer = None
        self.reset_round()

    def reset_round(self):
        self.strokes = []
        self.guessed = set()
        self.word = ""
        self.reveal = []
        self.options = []

    def name(self, pid):
        return self.room.players[pid].name if pid in self.room.players else "?"

    def left(self):
        return max(0, round(self.deadline - time.monotonic()))

    def hint(self):
        if self.phase == "reveal":
            return self.word
        if self.phase != "draw":
            return ""
        spent = 1 - (self.deadline - time.monotonic()) / self.DRAW
        count = min((spent >= 0.4) + (spent >= 0.7), max(0, len(self.reveal) - 2))
        shown = set(self.reveal[:count])
        return "".join(ch if ch == " " or i in shown else "_" for i, ch in enumerate(self.word))

    def push(self, to=None):
        msg = {"t": "state", "phase": self.phase, "left": self.left(), "drawer": self.drawer,
               "hint": self.hint(), "turn": self.turn, "turns": len(self.order) * self.TURNS_EACH,
               "players": [{"id": i, "n": p.name, "s": self.scores.get(i, 0), "g": i in self.guessed}
                           for i, p in self.room.players.items()]}
        if to:
            self.room.send(to, msg)
        else:
            self.room.broadcast(msg)

    def join(self, p):
        self.scores.setdefault(p.id, 0)
        self.chat(f"{p.name} joined")
        if self.phase == "lobby" and len(self.room.players) >= 2:
            self.start_game()
        self.push()
        self.room.send(p, {"t": "strokes", "list": self.strokes})
        self.tell_drawer()

    def leave(self, p):
        self.chat(f"{p.name} left")
        if self.phase in ("pick", "draw") and p.id == self.drawer:
            self.end_turn("the drawer left")
        if len(self.room.players) < 2 and self.phase != "lobby":
            self.phase = "lobby"
            self.reset_round()
        self.push()

    def chat(self, text, kind="sys"):
        self.room.broadcast({"t": "chat", "k": kind, "text": text})

    def start_game(self):
        self.scores = {i: 0 for i in self.room.players}
        self.order = list(self.room.players)
        self.turn = 0
        self.next_turn()

    def next_turn(self):
        total = len(self.order) * self.TURNS_EACH
        while self.turn < total and self.order[self.turn % len(self.order)] not in self.room.players:
            self.turn += 1
        if self.turn >= total:
            board = sorted(self.room.players, key=lambda i: -self.scores.get(i, 0))
            if board:
                self.chat(f"Game over - {self.name(board[0])} wins with {self.scores.get(board[0], 0)}", "win")
            self.phase = "over"
            self.deadline = time.monotonic() + 12
            self.drawer = None
            self.reset_round()
            return
        self.drawer = self.order[self.turn % len(self.order)]
        self.reset_round()
        self.options = random.sample(WORDS, 3)
        self.phase = "pick"
        self.deadline = time.monotonic() + self.PICK
        self.room.broadcast({"t": "clear"})
        self.tell_drawer()

    def tell_drawer(self):
        d = self.room.players.get(self.drawer)
        if not d:
            return
        if self.phase == "pick":
            self.room.send(d, {"t": "pick", "options": self.options})
        elif self.phase == "draw":
            self.room.send(d, {"t": "word", "word": self.word})

    def choose(self, word):
        self.word = word
        self.reveal = [i for i, ch in enumerate(word) if ch != " "]
        random.shuffle(self.reveal)
        self.phase = "draw"
        self.deadline = time.monotonic() + self.DRAW
        self.tell_drawer()
        self.push()

    def end_turn(self, why):
        if self.phase not in ("pick", "draw"):
            return
        if self.word:
            self.chat(f"The word was '{self.word}' ({why})", "sys")
        self.phase = "reveal"
        self.deadline = time.monotonic() + self.REVEAL
        if not self.word:
            self.word = "-"

    def msg(self, p, m):
        kind = m.get("t")
        if kind == "pick" and self.phase == "pick" and p.id == self.drawer:
            w = m.get("word")
            if w in self.options:
                self.choose(w)
        elif kind == "stroke" and self.phase == "draw" and p.id == self.drawer:
            pts = m.get("p")
            if not isinstance(pts, list) or not 1 <= len(pts) <= 200 or len(self.strokes) >= 4000:
                return
            clean = []
            for pt in pts:
                if not isinstance(pt, list) or len(pt) != 2:
                    return
                x, y = num(pt[0], 0, 1), num(pt[1], 0, 1)
                if x is None or y is None:
                    return
                clean.append([round(x, 4), round(y, 4)])
            color = integer(m.get("c"), 0, 11)
            width = integer(m.get("w"), 1, 40)
            if color is None or width is None:
                return
            s = {"c": color, "w": width, "p": clean}
            self.strokes.append(s)
            self.room.broadcast({"t": "stroke", **s})
        elif kind == "clear" and self.phase == "draw" and p.id == self.drawer:
            self.strokes = []
            self.room.broadcast({"t": "clear"})
        elif kind == "chat":
            self.guess(p, str(m.get("text", ""))[:80].strip())

    def guess(self, p, text):
        if not text:
            return
        if self.phase == "draw" and p.id != self.drawer and p.id not in self.guessed:
            if text.lower() == self.word.lower():
                points = 100 + 8 * self.left()
                self.scores[p.id] = self.scores.get(p.id, 0) + points
                self.scores[self.drawer] = self.scores.get(self.drawer, 0) + 40
                self.guessed.add(p.id)
                self.chat(f"{p.name} guessed it! +{points}", "ok")
                if len(self.guessed) >= len(self.room.players) - 1:
                    self.end_turn("everyone got it")
                self.push()
                return
            elif self.word and sum(a != b for a, b in zip(text.lower(), self.word.lower())) <= 1 \
                    and len(text) == len(self.word):
                self.room.send(p, {"t": "chat", "k": "sys", "text": f"'{text}' is close!"})
        if self.phase == "draw" and (p.id == self.drawer or p.id in self.guessed):
            return self.say_to_solvers(p, text)
        self.room.broadcast({"t": "chat", "k": "msg", "who": p.name, "text": text})

    def say_to_solvers(self, p, text):
        for q in self.room.players.values():
            if q.id == self.drawer or q.id in self.guessed:
                self.room.send(q, {"t": "chat", "k": "msg", "who": p.name, "text": text, "private": True})

    def step(self):
        if not self.room.players:
            return
        now = time.monotonic()
        if self.phase == "pick" and now >= self.deadline:
            self.choose(random.choice(self.options))
        elif self.phase == "draw" and now >= self.deadline:
            self.end_turn("time is up")
        elif self.phase == "reveal" and now >= self.deadline:
            self.turn += 1
            self.next_turn()
        elif self.phase == "over" and now >= self.deadline:
            self.phase = "lobby"
            if len(self.room.players) >= 2:
                self.start_game()
        self.push()


class FoxRace(Game):
    """Everyone rows one shared river (same seed) to the finish line; points decide the podium.
    The browser respawns a crashed boat and reports distance and points, the room keeps time, fish, places,
    weapons and mines. The boat that is hit slows itself (its shield may absorb the hit), the room only scores."""
    tick = 0.1
    GOALS = (500, 1000, 1500, 2500, 4000)
    COUNTDOWN = 3.0
    GRACE = 30
    PODIUM = (600, 400, 250)
    LATE = 100
    CHARS = ("rusty", "frost", "ember", "shadow", "lime")
    START_WEAPON = {"shadow": "mine"}
    # weapon: (slow ms, points for the shooter)
    WEAPONS = {"snow": (1800, 25), "mine": (2600, 40), "seeker": (2200, 35), "frost": (1500, 15)}
    SNOW_RANGE, SNOW_LANE, MINE_LANE, MINE_LIFE, FROST_RANGE = 45, 1.6, 1.5, 45, 28

    def __init__(self, room):
        super().__init__(room)
        self.goal, self.weapons_on = 1500, True
        self.phase, self.at, self.seed, self.racers, self.taken = "lobby", 0.0, 0, {}, set()
        self.mines, self.mine_seq = [], 0

    def join(self, p):
        self.racers[p.id] = {"n": p.name, "d": 0, "x": 0, "v": 0, "s": 0, "f": None, "fb": 0, "hb": 0, "in": False,
                             "ch": "rusty", "w": None, "pd": 0, "got": set()}

    def leave(self, p):
        self.racers.pop(p.id, None)
        self.mines = [m for m in self.mines if m["by"] != p.id]

    def begin(self):
        for q in self.racers.values():
            q.update({"d": 0, "x": 0, "v": 0, "s": 0, "f": None, "fb": 0, "hb": 0, "in": True, "pd": 0,
                      "w": self.START_WEAPON.get(q["ch"]) if self.weapons_on else None, "got": set()})
        self.taken.clear()
        self.mines.clear()
        self.seed = random.randrange(2 ** 31)
        self.phase, self.at = "countdown", time.time() + self.COUNTDOWN

    def admin(self, cmd, args):
        if cmd == "stop":
            if self.phase not in ("countdown", "racing"):
                return {"ok": False}
            self.phase, self.mines = "lobby", []
            for q in self.racers.values():
                q.update({"in": False, "w": None, "f": None})
            self.room.broadcast({"t": "note", "text": "the host stopped the race"})
            return {"ok": True}
        if cmd == "start":
            if self.phase in ("lobby", "done") and self.racers:
                self.begin()
                return {"ok": True}
            return {"ok": False}
        if cmd == "goal" and self.phase in ("lobby", "done"):
            try:
                goal = int(args.get("m"))
            except (TypeError, ValueError):
                return {"ok": False}
            if goal in self.GOALS:
                self.goal = goal
                return {"goal": goal}
            return {"ok": False}
        if cmd == "weapons":
            self.weapons_on = str(args.get("on", "1")) not in ("0", "false")
            return {"weapons": self.weapons_on}
        return None

    def hit(self, by, victim, kind):
        ms, pts = self.WEAPONS[kind]
        self.racers[by]["hb"] += pts
        return ms

    def fire(self, p, r):
        kind = r["w"]
        if kind not in self.WEAPONS:
            return
        r["w"] = None
        ms = self.WEAPONS[kind][0]
        ahead = sorted(((q["d"] - r["d"], i) for i, q in self.racers.items()
                        if i != p.id and q["in"] and q["f"] is None and q["d"] > r["d"]))
        fx = {"t": "fx", "k": kind, "by": p.id, "d": round(r["d"], 1), "x": round(r["x"], 1), "ms": ms}
        if kind == "snow":
            hit = next((i for gap, i in ahead if gap <= self.SNOW_RANGE and abs(self.racers[i]["x"] - r["x"]) < self.SNOW_LANE), None)
            fx.update(to=hit, d1=round(r["d"] + (self.racers[hit]["d"] - r["d"] if hit else self.SNOW_RANGE), 1),
                      x1=round(self.racers[hit]["x"] if hit else r["x"], 1))
        elif kind == "seeker":
            hit = ahead[0][1] if ahead else None
            fx.update(to=hit, d1=round(self.racers[hit]["d"] if hit else r["d"] + self.SNOW_RANGE, 1),
                      x1=round(self.racers[hit]["x"] if hit else r["x"], 1))
        elif kind == "mine":
            self.mine_seq += 1
            self.mines.append({"id": self.mine_seq, "d": r["d"], "x": r["x"], "by": p.id, "t": time.time()})
            fx["id"] = self.mine_seq
        else:
            hits = [i for i, q in self.racers.items() if i != p.id and q["in"] and q["f"] is None
                    and abs(q["d"] - r["d"]) <= self.FROST_RANGE]
            fx["tos"] = hits
            for i in hits:
                self.hit(p.id, i, kind)
        if fx.get("to") is not None:
            self.hit(p.id, fx["to"], kind)
        self.room.broadcast(fx)

    def msg(self, p, m):
        r = self.racers.get(p.id)
        if r is None:
            return
        t = m.get("t")
        if t == "char" and self.phase in ("lobby", "done") and m.get("c") in self.CHARS:
            r["ch"] = m["c"]
        elif t == "start" and self.phase in ("lobby", "done"):
            self.begin()
        elif self.phase != "racing" or not r["in"] or r["f"] is not None:
            return
        elif t == "pos":
            r["pd"], r["d"] = r["d"], num(m.get("d"), r["d"], self.goal, r["d"])
            r["x"] = num(m.get("x"), -8, 8, 0)
            r["v"] = num(m.get("v"), 0, 60, 0)
            r["s"] = int(num(m.get("s"), 0, 10 ** 6, r["s"]))
            if r["d"] >= self.goal:
                place = sum(q["f"] is not None for q in self.racers.values())
                r["f"], r["v"] = round(time.time() - self.at, 2), 0
                r["fb"] = self.PODIUM[place] if place < len(self.PODIUM) else self.LATE
        elif t == "take":
            fish = integer(m.get("id"), 0, 10 ** 6)
            if fish is not None and fish not in self.taken:
                self.taken.add(fish)
                self.room.broadcast({"t": "took", "id": fish, "by": p.id})
        elif t == "crate" and self.weapons_on:
            crate = integer(m.get("id"), 0, 10 ** 6)
            if crate is not None and crate not in r["got"] and r["w"] is None:
                r["got"].add(crate)
                r["w"] = random.choice(tuple(self.WEAPONS))
        elif t == "fire" and self.weapons_on:
            self.fire(p, r)

    def trip_mines(self, now):
        left = []
        for mine in self.mines:
            victim = next((i for i, q in self.racers.items() if i != mine["by"] and q["in"] and q["f"] is None
                           and q["pd"] - 0.5 <= mine["d"] <= q["d"] + 0.5 and abs(q["x"] - mine["x"]) < self.MINE_LANE), None)
            if victim is not None:
                ms = self.hit(mine["by"], victim, "mine") if mine["by"] in self.racers else self.WEAPONS["mine"][0]
                self.room.broadcast({"t": "fx", "k": "boom", "id": mine["id"], "to": victim, "by": mine["by"], "ms": ms})
            elif now - mine["t"] < self.MINE_LIFE:
                left.append(mine)
            else:
                self.room.broadcast({"t": "fx", "k": "boom", "id": mine["id"], "to": None, "by": mine["by"], "ms": 0})
        self.mines = left

    def step(self):
        now = time.time()
        if self.phase == "countdown" and now >= self.at:
            self.phase = "racing"
        elif self.phase == "racing":
            self.trip_mines(now)
            field = [r for r in self.racers.values() if r["in"]]
            times = [r["f"] for r in field if r["f"] is not None]
            if not field or len(times) == len(field) or (times and now - self.at - min(times) > self.GRACE):
                self.phase = "done"
                self.mines.clear()
        self.room.broadcast({"t": "state", "phase": self.phase, "goal": self.goal, "seed": self.seed,
                             "host": self.room.host_id(), "locked": self.room.locked, "weapons": self.weapons_on,
                             "left": round(max(0.0, self.at - now), 2) if self.phase == "countdown" else 0,
                             "clock": round(now - self.at, 1) if self.phase == "racing" else 0,
                             "racers": [{"id": i, **{k: v for k, v in r.items() if k not in ("pd", "got")},
                                         "pts": r["s"] + r["fb"] + r["hb"]} for i, r in self.racers.items()]})


CATALOG = {
    "snake": {"cls": Snake, "title": "Snake.io", "cap": 16, "icon": "S",
              "blurb": "eat, grow, make others crash into you - 2 to 16 players"},
    "draw": {"cls": Draw, "title": "Draw & Guess", "cap": 12, "icon": "D",
             "blurb": "one draws, the rest guess the word - 2 to 12 players"},
    "tictactoe": {"cls": TicTacToe, "title": "Tic-Tac-Toe", "cap": 20, "icon": "X",
                  "blurb": "two players, the rest watch and take the next seat"},
    "connect4": {"cls": ConnectFour, "title": "Connect Four", "cap": 20, "icon": "C",
                 "blurb": "drop discs, line up four - 2 players + audience"},
    "foxfloe": {"cls": FoxRace, "title": "Fox & Floe", "cap": 16, "icon": "F",
                "blurb": "3D river race: pick a fox, grab crates, snowballs, mines and seekers - 2 to 16"},
    "pong": {"cls": Pong, "title": "Pong", "cap": 20, "icon": "P",
             "blurb": "first to 7 wins - 2 players + audience"},
}
