// Shared by every game page: name prompt, websocket with reconnect, seat cards, toast.
(() => {
  const $ = (s) => document.querySelector(s);
  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* private mode */ } },
  };
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined) e.textContent = text;
    return e;
  };

  let sock = null;

  const Arcade = {
    me: null,
    el,
    palette: ["#111111", "#ffffff", "#ff4d4d", "#ff9f43", "#ffd93d", "#4cd964", "#2ec4b6", "#3b82f6", "#9b5de5", "#ff6fb5", "#8d5a3b", "#9aa5b1"],
    snakeColors: ["#ff6b81", "#4dd4ac", "#7c9cff", "#ffc857", "#c792ea", "#ff9f43", "#5ad1ff", "#a3e635", "#f472b6", "#94a3b8"],
    seatColors: ["#ff6b81", "#4dd4ac"],

    toast(text, ms = 2600) {
      document.querySelectorAll(".toast").forEach((t) => t.remove());
      const t = el("div", "toast", text);
      document.body.appendChild(t);
      setTimeout(() => t.remove(), ms);
    },

    askName(force) {
      const asked = new URLSearchParams(location.search).get("name");
      if (asked && !force) { store.set("arcade.name", asked.slice(0, 16)); return Promise.resolve(asked.slice(0, 16)); }
      const saved = store.get("arcade.name");
      if (saved && !force) return Promise.resolve(saved);
      return new Promise((resolve) => {
        const box = el("div", "modal");
        const card = el("div", "card");
        const input = el("input");
        input.maxLength = 16;
        input.placeholder = "Your name";
        input.value = saved || "";
        const go = el("button", "btn", "Play");
        const done = () => {
          const name = input.value.trim().slice(0, 16) || "Player";
          store.set("arcade.name", name);
          box.remove();
          resolve(name);
        };
        go.onclick = done;
        input.onkeydown = (e) => { if (e.key === "Enter") done(); };
        card.append(el("h2", "", "Pick a name"), el("p", "", "Your teammates see this in the room"), input, go);
        box.appendChild(card);
        document.body.appendChild(box);
        input.focus();
      });
    },

    // start({welcome(m), msg(m), close()}): joins the room and keeps the socket alive.
    async start(h) {
      const conn = $("#conn");
      const setConn = (ok, text) => {
        if (!conn) return;
        conn.className = "pill " + (ok ? "ok" : "bad");
        conn.textContent = text;
      };
      const who = $("#who");
      if (who) who.onclick = async () => { store.set("arcade.name", await Arcade.askName(true)); location.reload(); };
      const name = await Arcade.askName();
      let tries = 0, ping, dead = false;
      const open = () => {
        const ws = sock = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws`);
        ws.onopen = () => {
          tries = 0;
          ws.send(JSON.stringify({ t: "join", name }));
          ping = setInterval(() => Arcade.send({ t: "ping" }), 20000);
        };
        ws.onmessage = (e) => {
          let m;
          try { m = JSON.parse(e.data); } catch { return; }
          if (m.t === "welcome") {
            Arcade.me = m;
            document.title = `${m.title} - ${m.room}`;
            const room = $("#room");
            if (room) { room.textContent = ""; room.append(el("b", "", m.title), " · " + m.room); }
            if (who) who.textContent = m.name;
            setConn(true, "connected");
            if (h.welcome) h.welcome(m);
          } else if (m.t === "full") {
            dead = true;
            setConn(false, "room full");
            Arcade.toast("This room is full", 8000);
          } else {
            h.msg(m);
          }
        };
        ws.onclose = () => {
          clearInterval(ping);
          if (h.close) h.close();
          if (dead) return;
          setConn(false, "reconnecting…");
          setTimeout(open, Math.min(5000, 400 * 2 ** tries++));
        };
      };
      open();
    },

    send(obj) {
      if (sock && sock.readyState === 1) sock.send(JSON.stringify(obj));
    },

    // renderSeats(box, state, marks): two player cards for the seated games.
    renderSeats(box, s, marks) {
      box.textContent = "";
      for (let i = 0; i < 2; i++) {
        const name = s.seats[i];
        const row = el("div", "seat" + (name ? "" : " empty") + (s.ids[i] === Arcade.me.id ? " me" : "") +
          (s.turn === i && s.winner == null && !s.noturn ? " turn" : ""));
        const mark = el("div", "mark", marks[i]);
        mark.style.background = Arcade.seatColors[i];
        const text = el("div", "who");
        text.append(el("b", "", name || "Waiting for a player…"), el("span", "", name ? "" : "open seat"));
        row.append(mark, text, el("div", "wins", String(s.wins[i])));
        box.appendChild(row);
      }
      if (s.watching > 0) box.appendChild(el("div", "muted", `${s.watching} watching`));
    },

    mySeat(s) { return s.ids.indexOf(Arcade.me.id); },
  };

  window.Arcade = Arcade;
})();
