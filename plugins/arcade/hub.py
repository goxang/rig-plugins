#!/usr/bin/env python3
"""The Games tab: `hub.py offline` picks a solo game, `hub.py online` hosts and joins team rooms."""
import json
import os
import re
import select
import shutil
import signal
import socket
import subprocess
import sys
import termios
import time
import tty
import urllib.request
from collections import deque
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE / "online"))
from games import CATALOG  # noqa: E402

STATE = Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache") / "rig-arcade"
ROOMS = STATE / "rooms"
DEFAULT_PORT = 9100

OFFLINE = [
    ("Snake", "snake.sh", "eat, grow, do not bite yourself"),
    ("Tic Tac Toe", "tictactoe.sh", "against the computer, or a friend at the keyboard"),
    ("2048", "2048.sh", "slide and merge tiles"),
    ("Tetris", "tetris.sh", "the classic falling blocks"),
    ("Sudoku", "sudoku.sh", "fill the grid, no repeats"),
    ("Pac-Man", "pacman.sh", "eat the dots, dodge the ghosts"),
]

ACCENT, MUTED, OK, ERR, WARN = "\x1b[38;5;111m", "\x1b[38;5;244m", "\x1b[38;5;78m", "\x1b[38;5;203m", "\x1b[38;5;221m"
BOLD, RESET, REV, SEL = "\x1b[1m", "\x1b[0m", "\x1b[7m", "\x1b[48;5;237m"
ANSI = re.compile(r"(\x1b\[[0-9;]*m)")


def fit(s, width):
    out, used = [], 0
    for part in ANSI.split(s):
        if ANSI.fullmatch(part):
            out.append(part)
        elif used < width:
            out.append(part[: width - used])
            used += len(part[: width - used])
    return "".join(out) + RESET


def pad(s, n):
    return s + " " * max(0, n - len(ANSI.sub("", s)))


class Term:
    KEYS = {"A": "up", "B": "down", "C": "right", "D": "left", "H": "home", "F": "end"}

    def __init__(self):
        self.fd = sys.stdin.fileno()
        self.saved = termios.tcgetattr(self.fd)
        self.pending = deque()
        self.size = (0, 0)

    def __enter__(self):
        self.resume()
        return self

    def __exit__(self, *_):
        self.suspend()

    def resume(self):
        tty.setcbreak(self.fd)
        sys.stdout.write("\x1b[?25l\x1b[2J")
        sys.stdout.flush()

    def suspend(self):
        termios.tcsetattr(self.fd, termios.TCSADRAIN, self.saved)
        sys.stdout.write("\x1b[?25h\x1b[0m\x1b[2J\x1b[H")
        sys.stdout.flush()

    def dims(self):
        c, r = shutil.get_terminal_size((80, 24))
        return max(c, 20), max(r, 8)

    def key(self, timeout):
        if not self.pending:
            if not select.select([self.fd], [], [], timeout)[0]:
                return None
            self.tokenize(os.read(self.fd, 256).decode("utf-8", "ignore"))
        return self.pending.popleft() if self.pending else None

    def tokenize(self, s):
        i = 0
        while i < len(s):
            ch = s[i]
            if ch == "\x1b":
                if i + 1 < len(s) and s[i + 1] in "[O":
                    j = i + 2
                    while j < len(s) and not "@" <= s[j] <= "~":
                        j += 1
                    self.pending.append(self.KEYS.get(s[j] if j < len(s) else "", "other"))
                    i = j + 1
                    continue
                self.pending.append("esc")
            elif ch in "\r\n":
                self.pending.append("enter")
            elif ch in "\x7f\x08":
                self.pending.append("bksp")
            elif ch == "\x03":
                self.pending.append("ctrl-c")
            elif ch >= " ":
                self.pending.append(ch)
            i += 1

    def draw(self, lines, footer=""):
        cols, rows = self.dims()
        body = lines[: rows - 2]
        body += [""] * (rows - 2 - len(body))
        frame = body + ["", footer]
        sys.stdout.write("\x1b[H" + "\r\n".join(fit(l, cols) + "\x1b[K" for l in frame))
        sys.stdout.flush()


def header(title, sub, right=""):
    return [f" {BOLD}{ACCENT}ARCADE{RESET}  {BOLD}{title}{RESET}  {MUTED}{sub}{RESET}  {right}", f" {MUTED}{'─' * 70}{RESET}"]


def keys_footer(*pairs):
    return " " + "   ".join(f"{ACCENT}{k}{RESET} {MUTED}{v}{RESET}" for k, v in pairs)


def menu(term, title, sub, items, footer, idx=0, on_digit=True):
    """items: (label, blurb). Returns the chosen index or None on esc/q."""
    while True:
        lines = header(title, sub) + [""]
        for i, (label, blurb) in enumerate(items):
            row = f"  {ACCENT}{i + 1}{RESET}  {BOLD}{pad(label, 16)}{RESET} {MUTED}{blurb}{RESET}"
            lines.append((SEL + pad(row, 76) + RESET) if i == idx else row)
        term.draw(lines, footer)
        k = term.key(0.5)
        if k in ("up", "k"):
            idx = (idx - 1) % len(items)
        elif k in ("down", "j"):
            idx = (idx + 1) % len(items)
        elif k == "enter":
            return idx
        elif k in ("esc", "q", "ctrl-c"):
            return None
        elif on_digit and k and k.isdigit() and 1 <= int(k) <= len(items):
            return int(k) - 1


def offline(term):
    idx = 0
    while True:
        items = [(t, b) for t, _, b in OFFLINE]
        idx = menu(term, "Offline", "play alone, nothing to set up", items,
                   keys_footer(("↑↓", "move"), ("enter", "play"), ("1-6", "quick pick"), ("q", "quit")), idx)
        if idx is None:
            return
        term.suspend()
        try:
            subprocess.run(["bash", str(HERE / OFFLINE[idx][1])])
        finally:
            term.resume()


# ---------- online ----------

def web_opener():
    return urllib.request.build_opener(urllib.request.ProxyHandler({}))


def probe(host, port, timeout=1.5):
    host = f"[{host}]" if ":" in host else host
    try:
        with web_opener().open(f"http://{host}:{port}/info", timeout=timeout) as r:
            info = json.loads(r.read(65536))
        return info if isinstance(info, dict) and "game" in info else None
    except (OSError, ValueError):
        return None


def admin(port, command):
    """One operator command to a room on this machine; the room only obeys loopback with this header."""
    req = urllib.request.Request(f"http://127.0.0.1:{port}/admin/{command}", headers={"X-Rig-Admin": "1"})
    try:
        with web_opener().open(req, timeout=2) as r:
            return json.loads(r.read(65536))
    except (OSError, ValueError):
        return None


def local_ips():
    ips = []
    try:
        ips = [ip for ip in subprocess.run(["hostname", "-I"], capture_output=True, text=True, timeout=2).stdout.split()
               if ":" not in ip]
    except (OSError, subprocess.SubprocessError):
        pass
    if not ips:
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
                s.connect(("10.255.255.255", 1))
                ips = [s.getsockname()[0]]
        except OSError:
            ips = ["127.0.0.1"]
    return ips[:2]


def port_free(port):
    with socket.socket() as s:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            s.bind(("0.0.0.0", port))
            return True
        except OSError:
            return False


def is_ours(room):
    try:
        cmd = Path(f"/proc/{room['pid']}/cmdline").read_bytes().split(b"\0")
    except FileNotFoundError:
        return not Path("/proc").is_dir() and _alive(room["pid"])
    except OSError:
        return False
    return any(c.endswith(b"server.py") for c in cmd) and str(room["port"]).encode() in cmd


def _alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def load_rooms():
    rooms = []
    for f in sorted(ROOMS.glob("*.json"), key=lambda p: int(p.stem) if p.stem.isdigit() else 0):
        try:
            room = json.loads(f.read_text())
            alive = is_ours(room)
        except (OSError, ValueError, KeyError, TypeError):
            alive = False
        if not alive:
            f.unlink(missing_ok=True)
            continue
        info = probe("127.0.0.1", room["port"], 0.4) or {}
        room.update(players=info.get("players"), cap=info.get("cap"), locked=info.get("locked"))
        rooms.append(room)
    return rooms


def start_room(game, name, port):
    ROOMS.mkdir(parents=True, exist_ok=True)
    log = open(STATE / f"{port}.log", "wb")
    proc = subprocess.Popen([sys.executable, str(HERE / "online" / "server.py"), "--game", game, "--room", name,
                             "--port", str(port)], stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True)
    for _ in range(30):
        time.sleep(0.1)
        if proc.poll() is not None:
            tail = (STATE / f"{port}.log").read_text(errors="ignore").strip().splitlines()
            return False, tail[-1] if tail else "the server stopped right away"
        if probe("127.0.0.1", port, 0.3):
            break
    else:
        proc.terminate()
        return False, "the server did not answer"
    (ROOMS / f"{port}.json").write_text(json.dumps({"pid": proc.pid, "game": game, "room": name, "port": port,
                                                    "started": int(time.time())}))
    return True, ""


def stop_room(room):
    if is_ours(room):
        os.kill(room["pid"], signal.SIGTERM)
        for _ in range(20):
            if not _alive(room["pid"]):
                break
            time.sleep(0.1)
        else:
            os.kill(room["pid"], signal.SIGKILL)
    (ROOMS / f"{room['port']}.json").unlink(missing_ok=True)


def spawn_detached(cmd, data=None):
    try:
        p = subprocess.Popen(cmd, stdin=subprocess.PIPE if data else subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                             stderr=subprocess.DEVNULL, start_new_session=True)
        if data:
            p.stdin.write(data.encode())
            p.stdin.close()
        return True
    except OSError:
        return False


def open_url(url):
    for tool in ("xdg-open", "open", "wslview"):
        if shutil.which(tool) and spawn_detached([tool, url]):
            return True
    return False


def copy_text(text):
    for cmd in (["wl-copy"], ["xclip", "-selection", "clipboard"], ["xsel", "-ib"], ["pbcopy"]):
        if shutil.which(cmd[0]) and spawn_detached(cmd, text):
            return True
    return False


def parse_address(raw):
    raw = re.sub(r"^\w+://", "", raw.strip()).split("/")[0]
    if not raw:
        return None
    host, port = raw, DEFAULT_PORT
    if raw.startswith("["):
        host, _, rest = raw[1:].partition("]")
        port = rest[1:] if rest.startswith(":") else port
    elif raw.count(":") == 1:
        host, port = raw.split(":")
    if not re.fullmatch(r"[A-Za-z0-9._-]+|[0-9a-fA-F:]+", host or ""):
        return None
    try:
        port = int(port)
    except ValueError:
        return None
    return (host, port) if 1 <= port <= 65535 else None


def age(ts):
    s = max(0, int(time.time() - ts))
    return f"{s}s" if s < 60 else f"{s // 60}m" if s < 3600 else f"{s // 3600}h{s % 3600 // 60:02d}m"


class Online:
    def __init__(self, term):
        self.term, self.rooms, self.sel = term, [], 0
        self.ips = local_ips()
        self.note, self.note_until, self.note_color = "", 0, MUTED
        self.recent_file = STATE / "recent.json"

    def say(self, text, color=OK, secs=6):
        self.note, self.note_color, self.note_until = text, color, time.time() + secs

    def url(self, room):
        return f"http://{self.ips[0]}:{room['port']}"

    def recent(self):
        try:
            data = json.loads(self.recent_file.read_text())
            return [x for x in data if isinstance(x, str)][:6]
        except (OSError, ValueError):
            return []

    def remember(self, addr):
        keep = [addr] + [r for r in self.recent() if r != addr]
        try:
            STATE.mkdir(parents=True, exist_ok=True)
            self.recent_file.write_text(json.dumps(keep[:6]))
        except OSError:
            pass

    def refresh(self):
        self.rooms = load_rooms()
        self.sel = min(self.sel, max(0, len(self.rooms) - 1))

    def screen(self, prompt=None):
        cols, _ = self.term.dims()
        lines = header("Online", "host a room, share the link, play with the team", f"{MUTED}you: {', '.join(self.ips)}{RESET}")
        lines.append("")
        if not self.rooms:
            lines += [f"  {MUTED}You are not hosting any room.{RESET}", "",
                      f"  {ACCENT}n{RESET}  host a new room - Snake.io, Draw & Guess, Tic-Tac-Toe, Connect Four, Pong or Fox & Floe",
                      f"  {ACCENT}j{RESET}  join a teammate: type their ip and port, the game opens in your browser", "",
                      f"  {MUTED}If a teammate cannot connect, allow the port in your firewall.{RESET}"]
        else:
            lines.append(f" {MUTED}{pad('  #', 5)}{pad('GAME', 14)}{pad('ROOM', 22)}{pad('PLAYERS', 9)}{pad('UP', 8)}JOIN LINK{RESET}")
            for i, r in enumerate(self.rooms):
                cap = f"{r['players']}/{r['cap']}" if r.get("players") is not None else "?"
                cap += " lock" if r.get("locked") else ""
                row = (f"  {ACCENT}{i + 1}{RESET}  {BOLD}{pad(CATALOG[r['game']]['title'], 14)}{RESET}"
                       f"{pad(r['room'][:20], 22)}{pad(cap, 10)}{pad(age(r['started']), 8)}{OK}{self.url(r)}{RESET}")
                lines.append((SEL + pad(row, min(cols, 100)) + RESET) if i == self.sel else row)
        if self.note and time.time() < self.note_until:
            lines += ["", f" {self.note_color}{self.note}{RESET}"]
        footer = prompt or keys_footer(("n", "new room"), ("j", "join"), ("enter", "open"), ("c", "copy link"),
                                       ("k", "kick"), ("l", "lock"), ("x", "stop race"), ("r", "restart"),
                                       ("s", "stop"), ("a", "stop all"), ("↑↓", "select"), ("q", "quit"))
        self.term.draw(lines, footer)

    def ask(self, label, default="", hint=""):
        text, first = default, True
        while True:
            self.screen(f" {ACCENT}{label}{RESET} {text}{REV} {RESET}  {MUTED}{hint}{RESET}")
            k = self.term.key(0.5)
            if k == "enter":
                return text.strip()
            if k in ("esc", "ctrl-c"):
                return None
            if k == "bksp":
                text = text[:-1]
            elif k and len(k) == 1:
                text = (k if first else text + k)[:60]
            if k:
                first = False

    def host(self):
        games = list(CATALOG.items())
        choice = menu(self.term, "New room", "which game?", [(m["title"], m["blurb"]) for _, m in games],
                      keys_footer(("↑↓", "move"), ("enter", "choose"), ("q", "back")))
        if choice is None:
            return
        game, meta = games[choice]
        who = os.environ.get("USER") or "team"
        name = self.ask("Room name:", f"{who}'s {meta['title']}"[:32], "enter = ok, empty = cancel")
        if not name:
            return
        used = {r["port"] for r in self.rooms}
        port = next(p for p in range(DEFAULT_PORT, 65536) if p not in used and port_free(p))
        raw = self.ask("Port:", str(port), "teammates need this number")
        if not raw:
            return
        if not raw.isdigit() or not 1024 <= int(raw) <= 65535:
            return self.say("Port must be a number from 1024 to 65535", ERR)
        port = int(raw)
        if port in used or not port_free(port):
            return self.say(f"Port {port} is already taken", ERR)
        ok, err = start_room(game, name, port)
        self.refresh()
        if ok:
            self.sel = next((i for i, r in enumerate(self.rooms) if r["port"] == port), 0)
            self.say(f"'{name}' is live - send teammates {self.ips[0]}:{port}  (c copies the link)")
        else:
            self.say(f"Could not start the room: {err}", ERR, 12)

    def kick(self, room):
        out = admin(room["port"], "players")
        people = out["players"] if out else []
        if not people:
            return self.say("Nobody to remove in that room", WARN)
        choice = menu(self.term, f"Remove from '{room['room']}'", "they are blocked from rejoining until the room restarts",
                      [(p["name"] + ("  (host)" if p["host"] else ""), f"id {p['id']}") for p in people],
                      keys_footer(("↑↓", "move"), ("enter", "remove"), ("q", "back")))
        if choice is None:
            return
        who = people[choice]
        out = admin(room["port"], f"kick?id={who['id']}")
        self.say(f"Removed {who['name']}" if out and out.get("ok") else f"Could not remove {who['name']}",
                 OK if out and out.get("ok") else ERR)

    def join(self):
        recent = self.recent()
        hint = "  recent: " + "  ".join(f"{i + 1}={a}" for i, a in enumerate(recent)) if recent else ""
        raw = self.ask("Join (ip or ip:port):", "", hint.strip() or "enter = join, empty = cancel")
        if not raw:
            return
        if raw.isdigit() and 1 <= int(raw) <= len(recent):
            raw = recent[int(raw) - 1]
        addr = parse_address(raw)
        if not addr:
            return self.say("That is not an address - try 10.0.0.5:9100", ERR)
        host, port = addr
        self.say(f"Contacting {host}:{port}…", MUTED, 3)
        self.screen()
        info = probe(host, port, 2.5)
        if not info:
            return self.say(f"No room answers on {host}:{port} - is the host running it, and is the port open?", ERR, 10)
        self.remember(f"{host}:{port}")
        shown = f"[{host}]" if ":" in host else host
        url = f"http://{shown}:{port}/"
        if open_url(url):
            self.say(f"Joining {info['title']} - '{info['room']}' ({info['players']}/{info['cap']})  {url}")
        else:
            self.say(f"Open this in a browser: {url}", WARN, 15)

    def run(self):
        self.refresh()
        last, size = time.time(), self.term.dims()
        while True:
            self.screen()
            k = self.term.key(0.5)
            if self.term.dims() != size:
                size = self.term.dims()
                sys.stdout.write("\x1b[2J")
            if time.time() - last > 2:
                self.refresh()
                last = time.time()
            if k in ("q", "ctrl-c"):
                return
            if k == "up" and self.rooms:
                self.sel = (self.sel - 1) % len(self.rooms)
            elif k == "down" and self.rooms:
                self.sel = (self.sel + 1) % len(self.rooms)
            elif k == "n":
                self.host()
            elif k == "j":
                self.join()
            elif k and k.isdigit() and 1 <= int(k) <= len(self.rooms):
                self.sel = int(k) - 1
            elif k in ("enter", "o") and self.rooms:
                url = self.url(self.rooms[self.sel])
                self.say(f"Opened {url}" if open_url(url) else f"Open this in a browser: {url}", OK)
            elif k == "c" and self.rooms:
                url = self.url(self.rooms[self.sel])
                self.say(f"Copied {url}" if copy_text(url) else f"No clipboard tool found - the link is {url}", OK)
            elif k == "k" and self.rooms:
                self.kick(self.rooms[self.sel])
            elif k == "l" and self.rooms:
                room = self.rooms[self.sel]
                out = admin(room["port"], f"lock?on={0 if room.get('locked') else 1}")
                self.say(("Locked" if out and out.get("locked") else "Unlocked") + f" '{room['room']}'" if out else "The room did not answer",
                         OK if out else ERR)
                self.refresh()
            elif k == "x" and self.rooms:
                out = admin(self.rooms[self.sel]["port"], "stop")
                self.say("Race stopped, everyone is back in the lobby" if out and out.get("ok") else "No race is running in that room",
                         OK if out and out.get("ok") else WARN)
            elif k == "r" and self.rooms:
                room = self.rooms[self.sel]
                stop_room(room)
                ok, err = start_room(room["game"], room["room"], room["port"])
                self.say(f"Restarted '{room['room']}'" if ok else f"Could not restart: {err}", OK if ok else ERR, 10)
                self.refresh()
            elif k == "a" and self.rooms:
                if self.ask(f"Stop all {len(self.rooms)} rooms? type y:", "", "everyone is disconnected") == "y":
                    for room in self.rooms:
                        stop_room(room)
                    self.say("Stopped every room", WARN)
                    self.refresh()
            elif k == "s" and self.rooms:
                room = self.rooms[self.sel]
                if self.ask(f"Stop '{room['room']}'? type y:", "", "everyone in it is disconnected") == "y":
                    stop_room(room)
                    self.say(f"Stopped '{room['room']}'", WARN)
                    self.refresh()


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "offline"
    if mode not in ("offline", "online") or not sys.stdin.isatty():
        sys.exit("usage: hub.py offline|online (run it in a terminal)")
    try:
        with Term() as term:
            (offline if mode == "offline" else lambda t: Online(t).run())(term)
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
