// The room's race: lobby, countdown, one shared river, the others as boats on it, points decide the podium.
(() => {
  const $ = (s) => document.querySelector(s), el = Arcade.el;
  const list = $('#raceList'), status = $('#raceStatus'), btn = $('#raceStart'), count = $('#raceCount'), tags = $('#raceTags');
  const place = $('#racePlace'), track = $('#raceTrack'), podium = $('#podium');
  const ff = () => window.__ff;
  const color = (id) => Arcade.snakeColors[id % Arcade.snakeColors.length];
  const fmt = (s) => `${Math.floor(s / 60)}:${(s % 60).toFixed(1).padStart(4, '0')}`;
  const chars = $('#raceChars'), hostBar = $('#raceHost'), weaponBtn = $('#weapon');
  const WEAPON = { snow: 'Snowball', mine: 'Ice mine', seeker: 'Seeker', frost: 'Frost wave' };
  const store = { get: (k) => { try { return localStorage.getItem(k); } catch { return null; } }, set: (k, v) => { try { localStorage.setItem(k, v); } catch { /* private mode */ } } };
  let S = null, countEnd = 0, goT = -1e9, lastF = null, rivals = [], hooked = false, appliedCh = null, wantCh = store.get('foxfloe_char') || 'rusty';

  const mine = () => S && Arcade.me && S.racers.find((r) => r.id === Arcade.me.id);
  const rowing = () => { const r = mine(); return S.phase === 'racing' && r && r.in && r.f == null; };
  const byRace = (a, b) => (b.in - a.in) || ((a.f ?? 1e9) - (b.f ?? 1e9)) || (b.d - a.d);
  const byPoints = (a, b) => (b.in - a.in) || (b.pts - a.pts) || byRace(a, b);
  const name = (id) => (S.racers.find((r) => r.id === id) || {}).n || 'someone';

  // the list is rebuilt ten times a second, so a click would land on a detached button: act on press
  list.addEventListener('pointerdown', (e) => {
    const k = e.target.closest('.kick');
    if (!k || !S) return;
    const id = +k.dataset.id, who = (S.racers.find((r) => r.id === id) || {}).n || 'this player';
    if (confirm(`Remove ${who} from the room?`)) Arcade.send({ t: 'host', cmd: 'kick', id });
  });
  btn.onclick = () => { Arcade.send({ t: 'start' }); btn.blur(); };
  $('#podiumAgain').onclick = (e) => { Arcade.send({ t: 'start' }); e.target.blur(); };
  $('#podiumClose').onclick = (e) => { podium.hidden = true; e.target.blur(); };

  function hook(f) {
    if (hooked || !f) return;
    hooked = true;
    f.race.hooks.fish = (id) => Arcade.send({ t: 'take', id });
    f.race.hooks.crate = (id) => Arcade.send({ t: 'crate', id });
    f.race.hooks.pass = (r) => Arcade.toast(`you passed ${r.n}  +50`, 1800);
  }

  function onState(m) {
    const prev = S ? S.phase : null, f = ff();
    hook(f);
    S = m;
    const me = mine();
    if (me && me.ch !== wantCh && appliedCh === null && f && f.race.chars[wantCh]) Arcade.send({ t: 'char', c: wantCh });
    if (me && f && me.ch !== appliedCh) { f.race.setChar(me.ch); appliedCh = me.ch; }
    if (m.phase === 'countdown') {
      countEnd = performance.now() + m.left * 1000;
      podium.hidden = true;
      if (prev !== 'countdown' && me && me.in && f) f.race.title();
    } else if (m.phase === 'racing' && prev === 'countdown' && me && me.in && f) {
      f.race.go(m.seed);
      goT = performance.now();
    }
    if (f) f.race.lock((m.phase === 'countdown' || rowing()) && !!me && me.in);
    if (me && me.f != null && lastF == null && m.phase !== 'lobby') {
      Arcade.toast(`finished #${[...m.racers].sort(byRace).findIndex((r) => r.id === me.id) + 1} in ${fmt(me.f)}  +${me.fb}`, 5000);
      if (f) f.race.title();
    }
    lastF = me ? me.f : null;
    if (m.phase === 'done' && prev === 'racing') showPodium(m, me);
    syncRivals(m, me);
    render(m, me);
  }

  function syncRivals(m, me) {
    const now = performance.now(), next = [];
    if (m.phase === 'racing' && me && me.in) {
      for (const r of m.racers) {
        if (r.id === me.id || !r.in) continue;
        const o = rivals.find((x) => x.id === r.id) || { id: r.id, c: color(r.id) };
        Object.assign(o, { n: r.n, ch: r.ch, d: r.d, x: r.x, v: r.f == null ? r.v : 0, t: now, done: r.f != null });
        next.push(o);
      }
    }
    rivals = next;
    if (ff()) ff().race.rivals = rivals;
  }

  function render(m, me) {
    const racing = m.phase === 'racing' || m.phase === 'countdown';
    const order = [...m.racers].sort(m.phase === 'racing' ? byRace : byPoints);
    list.textContent = '';
    order.forEach((r, i) => {
      const row = el('div', 'r-row' + (me && r.id === me.id ? ' me' : '') + (r.in ? '' : ' out'));
      const dot = el('i', 'r-dot'); dot.style.background = color(r.id);
      const text = !r.in ? 'watching' : m.phase === 'lobby' ? '' : r.f != null ? `${r.pts} pts · ${fmt(r.f)}` : `${r.pts} pts`;
      const right = el('span', '', text);
      if (me && m.host === me.id && r.id !== me.id) {
        const kick = el('button', 'kick', '✕');
        kick.title = `remove ${r.n}`;
        kick.dataset.id = r.id;
        right.append(kick);
      }
      row.append(el('span', 'r-pos', r.in && m.phase !== 'lobby' ? String(i + 1) : ''), dot, el('b', '', (r.id === m.host ? '★ ' : '') + r.n), right);
      list.appendChild(row);
    });
    const field = m.racers.filter((r) => r.in);
    const myPos = me && me.in ? [...field].sort(byRace).findIndex((r) => r.id === me.id) + 1 : 0;
    place.hidden = !(m.phase === 'racing' && myPos);
    place.innerHTML = `${myPos}<small>/${field.length}</small>`;
    track.hidden = !racing;
    track.querySelectorAll('.t-dot').forEach((d) => { if (!field.some((r) => `t${r.id}` === d.id)) d.remove(); });
    for (const r of field) {
      let d = document.getElementById(`t${r.id}`);
      if (!d) { d = el('div', 't-dot'); d.id = `t${r.id}`; d.append(el('span')); track.appendChild(d); }
      d.style.background = color(r.id);
      d.style.left = `${Math.min(100, r.d / m.goal * 100)}%`;
      d.classList.toggle('me', !!me && r.id === me.id);
      d.firstChild.textContent = r.n;
    }
    renderChars(m, me);
    renderHost(m, me);
    renderWeapon(m, me);
    btn.style.display = m.phase === 'lobby' || m.phase === 'done' ? '' : 'none';
    btn.textContent = m.phase === 'done' ? 'race again' : 'start race';
    status.textContent =
      m.phase === 'lobby' ? `${m.racers.length} here · ${m.goal} m · practice while you wait` :
      m.phase === 'countdown' ? 'get ready' :
      m.phase === 'done' ? 'race over · points decide the podium' :
      !me || !me.in ? 'race on, you join the next one' :
      me.f != null ? `finished in ${fmt(me.f)}, waiting for the rest` :
      `${Math.max(0, Math.ceil(m.goal - me.d))} m to go · ${fmt(m.clock)}`;
  }

  function renderChars(m, me) {
    const f = ff();
    const open = f && me && (m.phase === 'lobby' || m.phase === 'done');
    chars.hidden = !open;
    if (!open) return;
    const key = `${me.ch}`;
    if (chars.dataset.sel === key && chars.childElementCount) return;
    chars.dataset.sel = key;
    chars.textContent = '';
    for (const [id, c] of Object.entries(f.race.chars)) {
      const b = el('button', 'char' + (id === me.ch ? ' sel' : ''));
      b.style.setProperty('--fur', '#' + c.fur.toString(16).padStart(6, '0'));
      b.title = `${c.name}: ${c.perk}`;
      b.append(el('i'), el('span', '', c.name));
      b.onclick = () => { wantCh = id; store.set('foxfloe_char', id); Arcade.send({ t: 'char', c: id }); b.blur(); };
      chars.appendChild(b);
    }
  }

  function renderHost(m, me) {
    const host = !!me && m.host === me.id;
    hostBar.hidden = !host;
    if (!host) return;
    const racing = m.phase === 'racing' || m.phase === 'countdown';
    const sig = [racing, m.goal, m.locked, m.weapons].join();
    if (hostBar.dataset.sig === sig) return;
    hostBar.dataset.sig = sig;
    hostBar.textContent = '';
    const add = (label, msg, cls = '') => {
      const b = el('button', 'hb ' + cls, label);
      b.onclick = () => { Arcade.send({ t: 'host', ...msg }); b.blur(); };
      hostBar.appendChild(b);
    };
    if (racing) add('stop race', { cmd: 'stop' }, 'danger');
    else for (const g of [500, 1000, 1500, 2500, 4000]) add(`${g}`, { cmd: 'goal', m: g }, g === m.goal ? 'on' : '');
    add(m.locked ? 'unlock room' : 'lock room', { cmd: 'lock', on: m.locked ? 0 : 1 }, m.locked ? 'on' : '');
    add(m.weapons ? 'weapons on' : 'weapons off', { cmd: 'weapons', on: m.weapons ? 0 : 1 }, m.weapons ? 'on' : '');
  }

  function renderWeapon(m, me) {
    const w = m.phase === 'racing' && me && me.in && me.f == null ? me.w : null;
    weaponBtn.hidden = !w;
    if (w) weaponBtn.firstChild.textContent = WEAPON[w] || w;
    weaponBtn.dataset.w = w || '';
  }

  function fire() {
    const me = mine();
    if (S && me && rowing() && me.w) Arcade.send({ t: 'fire' });
  }
  weaponBtn.onclick = () => { fire(); weaponBtn.blur(); };
  addEventListener('keydown', (e) => { if (e.code === 'KeyF' && !e.repeat) fire(); });

  function onFx(m) {
    const f = ff(), me = mine();
    if (!f || !S || !me || !me.in || S.phase !== 'racing') return;
    const victims = m.tos || (m.to != null ? [m.to] : []);
    if (m.k === 'snow' || m.k === 'seeker') f.race.shot(m.k, m.d, m.x, m.d1, m.x1);
    else if (m.k === 'mine') f.race.mine(m.id, m.d, m.x);
    else if (m.k === 'boom') f.race.boom(m.id, m.to == null);
    else if (m.k === 'frost') f.race.ring(m.d, m.x);
    if (victims.includes(me.id)) {
      const blocked = f.race.hit(m.k === 'boom' ? 'mine' : m.k, m.ms);
      Arcade.toast(blocked ? `shield stopped ${name(m.by)}'s ${m.k === 'boom' ? 'mine' : m.k}` : `${name(m.by)} hit you`, 1800);
    } else if (m.by === me.id && victims.length) {
      Arcade.toast(`hit ${victims.map(name).join(', ')}`, 1400);
    } else if (m.k === 'boom' && m.by === me.id && m.to == null) {
      /* own mine expired */
    }
  }

  function showPodium(m, me) {
    const order = m.racers.filter((r) => r.in).sort(byPoints);
    const box = $('#podiumList');
    box.textContent = '';
    order.forEach((r, i) => {
      const row = el('div', 'p-row' + (i < 3 ? ` p${i + 1}` : '') + (me && r.id === me.id ? ' me' : ''));
      const dot = el('i', 'r-dot'); dot.style.background = color(r.id);
      row.append(el('span', 'p-rank', ['1st', '2nd', '3rd'][i] || `${i + 1}th`), dot, el('b', '', r.n),
        el('span', '', r.f != null ? fmt(r.f) : 'dnf'), el('strong', '', `${r.pts}`));
      box.appendChild(row);
    });
    $('#podiumTitle').textContent = order.length && me && order[0].id === me.id ? 'you win' : `${order.length ? order[0].n : 'nobody'} wins`;
    podium.hidden = false;
  }

  // positions out 10x a second, labels and the countdown every frame
  setInterval(() => {
    const f = ff();
    if (S && f && rowing()) Arcade.send({ t: 'pos', d: f.dist, x: f.x, v: f.speed, s: f.score });
  }, 100);

  const tagEls = new Map();
  (function frame() {
    requestAnimationFrame(frame);
    const now = performance.now();
    const n = S && S.phase === 'countdown' ? String(Math.max(1, Math.ceil((countEnd - now) / 1000))) : now - goT < 900 ? 'go' : '';
    if (count.textContent !== n) { count.textContent = n; count.classList.remove('beat'); void count.offsetWidth; if (n) count.classList.add('beat'); }
    for (const [id, t] of tagEls) if (!rivals.some((r) => r.id === id)) { t.remove(); tagEls.delete(id); }
    for (const r of rivals) {
      let t = tagEls.get(r.id);
      if (!t) { t = el('span'); t.style.borderColor = r.c; tags.appendChild(t); tagEls.set(r.id, t); }
      t.textContent = r.n;
      t.style.display = r.vis ? '' : 'none';
      if (r.vis) { t.style.left = `${r.sx}px`; t.style.top = `${r.sy}px`; }
    }
    const f = ff();
    if (f) document.body.classList.toggle('drafting', !!(S && rowing() && f.race.drafting));
  })();

  Arcade.start({
    msg(m) {
      if (m.t === 'state') onState(m);
      else if (m.t === 'fx') onFx(m);
      else if (m.t === 'took' && S && m.by !== Arcade.me.id && ff()) {
        if (ff().race.took(m.id) && rowing()) Arcade.toast(`${name(m.by)} snatched your fish`, 1400);
      }
    },
  });
})();
