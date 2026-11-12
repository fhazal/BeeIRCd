// Hive Panel. The page loads once; everything after is AJAX (fetch) and
// one live stream (Server-Sent Events). Text from the server is always set
// with textContent, never as HTML.
'use strict';
(() => {
  const app = document.getElementById('app');
  const SVG = 'http://www.w3.org/2000/svg';

  // ---------- small helpers ----------
  function h(tag, attrs, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (v === true) el.setAttribute(k, '');
      else el.setAttribute(k, v);
    }
    add(el, kids);
    return el;
  }
  function add(el, kids) {
    for (const k of kids.flat(Infinity)) {
      if (k == null || k === false) continue;
      el.append(k instanceof Node ? k : document.createTextNode(String(k)));
    }
    return el;
  }
  function icon(name, cls) {
    const s = document.createElementNS(SVG, 'svg');
    s.setAttribute('class', 'ic' + (cls ? ' ' + cls : ''));
    s.setAttribute('aria-hidden', 'true');
    const u = document.createElementNS(SVG, 'use');
    u.setAttribute('href', '#i-' + name);
    s.append(u);
    return s;
  }
  function svgEl(tag, attrs) {
    const el = document.createElementNS(SVG, tag);
    for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
    return el;
  }
  const fmt = n => Number(n || 0).toLocaleString('en-US');
  const plural = (n, one, many) => fmt(n) + ' ' + (n === 1 ? one : many);
  function bytes(n) {
    if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB';
    if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
    if (n >= 1 << 10) return Math.round(n / (1 << 10)) + ' KB';
    return n + ' bytes';
  }
  function since(t) {
    const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
    const d = Math.floor(s / 86400), hr = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
    if (d > 0) return d + (d === 1 ? ' day ' : ' days ') + hr + ' h';
    if (hr > 0) return hr + ' h ' + m + ' min';
    return m > 0 ? m + ' min' : 'under a minute';
  }
  const pad = n => String(n).padStart(2, '0');
  function when(t) {
    const d = new Date(t), now = new Date();
    const hm = pad(d.getHours()) + ':' + pad(d.getMinutes());
    if (d.toDateString() === now.toDateString()) return hm;
    const y = new Date(now); y.setDate(now.getDate() - 1);
    if (d.toDateString() === y.toDateString()) return 'yesterday ' + hm;
    return d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short' }) + ' ' + hm;
  }
  const day = t => new Date(t).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' });
  const has = f => !!me && me.flags.includes(f);

  // ---------- talking to the panel ----------
  class ApiError extends Error {}
  async function api(method, path, body) {
    const opt = { method, credentials: 'same-origin', headers: { 'X-Hive': '1' } };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    let res, data = {};
    try {
      res = await fetch(path, opt);
      data = await res.json().catch(() => ({}));
    } catch (e) {
      throw new ApiError("Can't reach the Hive Panel. Is beeircd-web running?");
    }
    if (res.status === 401 && data.signin) {
      signedOut(data.error || 'Please sign in.');
      throw new ApiError(data.error || 'Please sign in.');
    }
    if (!res.ok) throw new ApiError(data.error || 'Something went wrong (' + res.status + ').');
    return data;
  }

  // busy shows a spinner on button b while f runs.
  async function busy(b, f) {
    b.disabled = true; b.classList.add('busy'); b.setAttribute('aria-busy', 'true');
    try { return await f(); } finally { b.disabled = false; b.classList.remove('busy'); b.removeAttribute('aria-busy'); }
  }

  // ---------- toasts ----------
  let toastBox;
  function toast(parts, undo) {
    if (!toastBox || !toastBox.isConnected) { toastBox = h('div', { class: 'toasts', role: 'status' }); document.body.append(toastBox); }
    const t = h('div', { class: 'toast' }, h('span', { class: 'dot' }), h('span', {}, parts));
    if (undo) t.append(h('button', { type: 'button', class: 'btn ghost', text: 'Undo', onclick: async e => {
      await busy(e.currentTarget, undo).catch(err => toastErr(err.message));
      t.remove();
    } }));
    toastBox.append(t);
    setTimeout(() => t.remove(), 9000);
  }
  function toastErr(msg) {
    if (!toastBox || !toastBox.isConnected) { toastBox = h('div', { class: 'toasts', role: 'status' }); document.body.append(toastBox); }
    const t = h('div', { class: 'toast' }, h('span', { class: 'dot bad' }), h('span', { text: msg }));
    toastBox.append(t);
    setTimeout(() => t.remove(), 9000);
  }

  // ---------- state ----------
  let me = null;          // the signed-in oper
  let status = null;      // last status
  let events = null;      // last What happened
  let page = null;        // current page object {id, el, status(), events()}
  let live = null;        // EventSource
  let poll = 0, liveFails = 0;
  const pages = [
    { id: 'overview', label: 'Overview', icon: 'overview', make: overviewPage },
    { id: 'settings', label: 'Settings', icon: 'settings', need: 'a', make: settingsPage },
  ];

  // ---------- sign in ----------
  function signedOut(msg) {
    stopLive();
    me = null; status = null; events = null; page = null;
    showSignin(msg);
  }
  function showSignin(msg) {
    const nick = h('input', { class: 'in', id: 'nick', type: 'text', autocomplete: 'username', autocapitalize: 'off', spellcheck: 'false', required: true });
    const pw = h('input', { class: 'in', id: 'pw', type: 'password', autocomplete: 'current-password', required: true, 'aria-describedby': 'err' });
    const eye = h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Show password' }, icon('eye'));
    eye.addEventListener('click', () => {
      const show = pw.type === 'password';
      pw.type = show ? 'text' : 'password';
      eye.setAttribute('aria-label', show ? 'Hide password' : 'Show password');
      eye.replaceChildren(icon(show ? 'eye-off' : 'eye'));
      pw.focus();
    });
    const err = h('span', { class: 'err', id: 'err', role: 'alert', text: msg && msg !== 'Please sign in.' ? msg : '' });
    const go = h('button', { type: 'submit', class: 'btn main', text: 'Sign in' });
    const form = h('form', { novalidate: true },
      h('div', { class: 'top' }, icon('hive'), h('b', { text: 'Hive Panel' })),
      h('label', { class: 'f', for: 'nick' }, 'Oper nick', nick),
      h('label', { class: 'f pw', for: 'pw' }, 'Oper password', pw, eye),
      err, go,
      h('span', { class: 'foot', text: 'The same nick and password as /oper' }));
    form.addEventListener('submit', async e => {
      e.preventDefault();
      err.textContent = '';
      nick.classList.remove('wrong'); pw.classList.remove('wrong');
      if (!nick.value.trim()) { nick.classList.add('wrong'); err.textContent = 'Type your oper nick.'; nick.focus(); return; }
      if (!pw.value) { pw.classList.add('wrong'); err.textContent = 'Type your oper password.'; pw.focus(); return; }
      await busy(go, async () => {
        try {
          const d = await api('POST', '/api/login', { nick: nick.value.trim(), password: pw.value });
          me = d.oper;
          pw.value = '';
          start();
        } catch (e2) {
          err.textContent = e2.message;
          pw.classList.add('wrong');
          pw.select();
        }
      });
    });
    app.replaceChildren(h('div', { class: 'signin' }, form));
    document.title = 'Sign in · Hive Panel';
    nick.focus();
  }

  // ---------- shell ----------
  let mainEl, navEl;
  function start() {
    navEl = h('div', { class: 'nav' });
    const out = h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Sign out', title: 'Sign out' }, icon('signout'));
    out.addEventListener('click', async () => {
      await api('POST', '/api/logout').catch(() => {});
      signedOut('');
    });
    const side = h('nav', { class: 'side', 'aria-label': 'Hive Panel' },
      h('div', { class: 'brand' }, icon('hive'), 'Hive Panel'),
      navEl,
      h('div', { class: 'me' }, h('div', { class: 'av', text: me.account.slice(0, 1) }),
        h('div', { class: 'me-text' }, h('b', { text: me.account }), h('span', { class: 'lbl', text: me.owner ? 'Owner' : (me.class || 'Oper') })),
        out));
    mainEl = h('main', { class: 'content', id: 'main', tabindex: '-1' });
    app.replaceChildren(h('div', { class: 'hp' }, side, mainEl));
    drawNav();
    route();
    startLive();
  }
  function drawNav() {
    navEl.replaceChildren(...pages.filter(p => !p.need || has(p.need)).map(p =>
      h('a', { href: '#/' + p.id, 'data-page': p.id, class: page && page.id === p.id ? 'on' : null,
        'aria-current': page && page.id === p.id ? 'page' : null }, icon(p.icon), p.label)));
  }
  function route() {
    if (!me) return;
    const id = (location.hash.match(/^#\/([a-z]+)/) || [])[1] || 'overview';
    let p = pages.find(x => x.id === id && (!x.need || has(x.need)));
    if (!p) { p = pages[0]; history.replaceState(null, '', '#/overview'); }
    if (page && page.leave) page.leave();
    page = p.make();
    page.id = p.id;
    mainEl.replaceChildren(page.el);
    document.title = p.label + ' · Hive Panel';
    drawNav();
    if (status && page.status) page.status(status);
    if (events && page.events) page.events(events);
    mainEl.focus({ preventScroll: true });
    window.scrollTo(0, 0);
  }
  window.addEventListener('hashchange', () => {
    if (!me) return;
    route();
    // A page change counts as a click (keeps the session) and refreshes flags.
    api('GET', '/api/me').then(d => {
      if (d.oper && d.oper.flags !== me.flags) { me = d.oper; drawNav(); route(); }
    }).catch(() => {});
  });

  // ---------- live ----------
  function gotStatus(s) {
    status = s;
    if (page && page.status) page.status(s);
  }
  function gotEvents(list) {
    const before = events;
    events = list || [];
    if (page && page.events) page.events(events, before);
  }
  function startLive() {
    stopLive();
    if (document.hidden || !me) return;
    if (liveFails >= 3 || !window.EventSource) { startPoll(); return; }
    live = new EventSource('/api/live');
    let opened = false;
    live.addEventListener('open', () => { opened = true; liveFails = 0; });
    live.addEventListener('status', e => { setOffline(false); gotStatus(JSON.parse(e.data)); });
    live.addEventListener('events', e => gotEvents(JSON.parse(e.data)));
    live.addEventListener('offline', () => setOffline(true));
    live.addEventListener('signin', e => { const d = JSON.parse(e.data); signedOut(d.error); });
    live.addEventListener('error', () => {
      if (!opened) liveFails++;
      if (liveFails >= 3) { stopLive(); startPoll(); }
    });
  }
  function startPoll() {
    const tick = async () => {
      try {
        const [s, ev] = await Promise.all([api('GET', '/api/status'), api('GET', '/api/events')]);
        setOffline(false); gotStatus(s.status); gotEvents(ev.events);
      } catch (e) { if (me) setOffline(true); }
    };
    tick();
    poll = setInterval(tick, 15000);
  }
  function stopLive() {
    if (live) { live.close(); live = null; }
    if (poll) { clearInterval(poll); poll = 0; }
  }
  document.addEventListener('visibilitychange', () => {
    if (!me) return;
    if (document.hidden) stopLive(); else startLive();
  });
  let offline = false;
  function setOffline(on) {
    if (on === offline) return;
    offline = on;
    if (page && page.offline) page.offline(on);
  }

  // ---------- Overview ----------
  function overviewPage() {
    const sub = h('span', { class: 'sub', text: ' ' });
    const pill = h('span', { class: 'pill' }, h('span', { class: 'dot' }), h('span', { text: '…' }));
    const notice = h('div', { class: 'notice', role: 'alert', hidden: true, text: "The server isn't answering. The numbers below are from before." });

    const C = 2 * Math.PI * 52;
    const val = svgEl('circle', { class: 'val', cx: 60, cy: 60, r: 52, 'stroke-dasharray': '0 ' + C, transform: 'rotate(-90 60 60)' });
    const big = svgEl('text', { class: 'big', x: 60, y: 58, 'text-anchor': 'middle' });
    const small = svgEl('text', { class: 'small', x: 60, y: 76, 'text-anchor': 'middle' });
    const ring = svgEl('svg', { class: 'ring', viewBox: '0 0 120 120', role: 'img' });
    ring.append(svgEl('circle', { class: 'track', cx: 60, cy: 60, r: 52 }), val, big, small);
    const peak = h('span', { class: 'lbl' });

    const tile = label => {
      const n = h('span', { class: 'num' }), l = h('span', { class: 'lbl' });
      return { el: h('section', { class: 'card tight' }, h('span', { class: 'lbl', text: label }), n, l), n, l };
    };
    const tChan = tile('Channels'), tOpers = tile('Opers online'), tServers = tile('Servers'), tLag = tile('Reply time');
    const health = h('div'), healthWord = h('span', { class: 'okword' });
    const links = h('div');
    const svc = h('div'), svcWord = h('span', { class: 'okword' });
    const evList = h('div');

    const el = h('div', { class: 'stack' },
      h('header', { class: 'head' },
        h('div', { class: 'head-text' }, h('h1', { text: 'Overview' }), sub), pill),
      notice,
      h('div', { class: 'band' },
        h('section', { class: 'card ring-card' }, ring, h('span', { text: 'People online' }), peak),
        h('div', { class: 'tiles' }, tChan.el, tOpers.el, tServers.el, tLag.el)),
      h('div', { class: 'three' },
        h('section', { class: 'card' }, h('div', { class: 'ch' }, h('h2', { text: 'Health' }), healthWord), health),
        h('section', { class: 'card' }, h('div', { class: 'ch' }, h('h2', { text: 'Linked servers' })), links),
        h('section', { class: 'card' }, h('div', { class: 'ch' }, h('h2', { text: 'Services' }), svcWord), svc)),
      h('section', { class: 'card' }, h('div', { class: 'ch' }, h('h2', { text: 'What happened' }),
        h('span', { class: 'lbl' }, h('span', { class: 'dot warn' }), ' Live')), evList));
    el.classList.add('main-stack');

    const row = (dotCls, label, end, endCls, mono) => h('div', { class: 'row' },
      h('span', { class: mono ? 'mono' : null }, dotCls !== null ? h('span', { class: 'dot ' + dotCls }) : null, label),
      h('span', { class: 'end ' + (endCls || ''), text: end }));

    function onStatus(s) {
      sub.textContent = [s.network, s.server, 'BeeIRCd ' + s.version].filter(Boolean).join(' · ');
      pill.className = 'pill';
      pill.lastChild.textContent = 'Running ' + since(s.started);
      const max = s.max_users || 0, pct = max > 0 ? Math.min(1, s.online / max) : 0;
      val.setAttribute('stroke-dasharray', (pct * C).toFixed(1) + ' ' + C.toFixed(1));
      big.textContent = fmt(s.online);
      small.textContent = max ? 'of ' + fmt(max) : '';
      ring.setAttribute('aria-label', fmt(s.online) + (max ? ' of ' + fmt(max) : '') + ' people online');
      peak.textContent = 'Most since start ' + fmt(s.peak);

      tChan.n.textContent = fmt(s.channels);
      tChan.l.textContent = s.services_on ? plural(s.reg_channels, 'registered', 'registered') : 'Registered on the hub';
      tOpers.n.textContent = fmt(s.opers);
      tOpers.l.textContent = (s.oper_nicks || []).join(', ') || 'None on this server';
      const ls = s.links || [];
      const down = ls.filter(l => l.state !== 'up').length;
      tServers.n.replaceChildren(String(ls.length + 1), ' ', h('small', { text: ls.length === 0 ? 'this one' : down ? down + ' not linked' : 'all linked' }));
      const lags = ls.filter(l => l.state === 'up' && l.lag_ms).map(l => l.lag_ms);
      tServers.l.textContent = lags.length ? 'Slowest ping ' + Math.round(Math.max(...lags)) + ' ms' : ls.length ? '' : 'No links set up';
      tLag.n.replaceChildren(String(Math.max(0, Math.round(s.lag_avg_ms))), ' ', h('small', { text: 'ms' }));
      tLag.l.textContent = 'Memory ' + Math.round(s.memory_mb) + ' MB';

      // Health
      const rows = [];
      let worst = 0;
      const mark = (lvl) => { worst = Math.max(worst, lvl); return ['', 'warn', 'bad'][lvl]; };
      if (s.cert) {
        const c = s.cert;
        const left = (new Date(c.not_after) - Date.now()) / 86400000;
        if (c.renew_failures > 0) rows.push(row(mark(2), 'Certificate', c.renew_failures + ' renewals failed', 'badword'));
        else if (left < 7) rows.push(row(mark(left < 2 ? 2 : 1), 'Certificate', 'ends ' + day(c.not_after)));
        else rows.push(row(mark(0), 'Certificate', c.renew_at ? 'renews ' + day(c.renew_at) : 'until ' + day(c.not_after)));
      }
      rows.push(row(mark(0), 'Database', bytes(s.db_bytes || 0)));
      if (s.last_backup) {
        const old = (Date.now() - new Date(s.last_backup)) / 86400000 > 2;
        rows.push(row(mark(old ? 1 : 0), 'Last backup', when(s.last_backup)));
      } else rows.push(row(mark(1), 'Last backup', 'none yet'));
      if (s.update) rows.push(row(mark(1), 'Update', s.update.version + ' is out', 'warnword'));
      else rows.push(row(mark(0), 'Update', 'up to date'));
      const sf = s.safety || {};
      const guards = (sf.throttled || 0) + (sf.zlined || 0) + (sf.dnsbl_hits || 0);
      rows.push(row(mark(0), 'Guards today', guards ? fmt(guards) + ' stopped' : 'quiet'));
      health.replaceChildren(...rows);
      healthWord.className = ['okword', 'warnword', 'badword'][worst];
      healthWord.textContent = ['All fine', 'Look at this', 'Needs you'][worst];

      // Linked servers
      const lrows = [row('warn', s.server, 'this one', null, true)];
      for (const l of ls) {
        const cls = l.state === 'up' ? '' : l.state === 'down' ? 'bad' : 'warn';
        const end = l.state === 'up' ? (l.lag_ms ? Math.round(l.lag_ms) + ' ms' : 'linked') : l.state;
        lrows.push(row(cls, l.name, end, null, true));
      }
      links.replaceChildren(...lrows);

      // Services
      if (!s.services_on) {
        svc.replaceChildren(h('p', { class: 'empty', text: 'Services run on the hub server.' }));
        svcWord.textContent = '';
      } else {
        const counts = { NickServ: plural(s.nicks, 'nick', 'nicks'), ChanServ: plural(s.reg_channels, 'channel', 'channels'), MemoServ: plural(s.memos, 'memo', 'memos') };
        const bots = s.services || [];
        svc.replaceChildren(...bots.slice(0, 6).map(b => row(null, b, counts[b] || 'up')));
        svcWord.className = 'okword';
        svcWord.textContent = bots.length ? 'All up' : '';
      }
    }

    function onEvents(list, before) {
      if (!list.length) { evList.replaceChildren(h('p', { class: 'empty', text: 'Nothing yet. Sign-ins and changes made here show up in this list.' })); return; }
      const seen = new Set((before || []).map(e => e.at + e.who + e.what));
      evList.replaceChildren(...list.slice(0, 12).map(e => h('div', { class: 'ev' + (before && !seen.has(e.at + e.who + e.what) ? ' new' : '') },
        h('span', { class: 't', text: when(e.at) }),
        h('span', {}, h('b', { text: e.who }), ' ' + e.what, e.ip ? h('span', { class: 'ip', text: ' · ' + e.ip }) : null))));
    }

    return {
      el, status: onStatus, events: onEvents,
      offline(on) {
        notice.hidden = !on;
        pill.className = on ? 'pill bad' : 'pill';
        if (on) pill.lastChild.textContent = 'Not answering';
      },
    };
  }

  // ---------- Settings ----------
  const LIMITS = [
    { name: 'max-users', label: 'Max users', hint: 'people connected at once' },
    { name: 'max-channels', label: 'Max channels per user' },
    { name: 'flood-burst', label: 'Flood burst', hint: 'lines at once' },
    { name: 'flood-rate', label: 'Flood rate', hint: 'lines a second after that' },
  ];
  function settingsPage() {
    const grid = h('div', { class: 'grid2' }, h('p', { class: 'empty', text: 'Loading…' }));
    const count = h('span');
    const undoBtn = h('button', { type: 'button', class: 'btn ghost', text: 'Undo' });
    const saveBtn = h('button', { type: 'button', class: 'btn main', text: 'Save' });
    const bar = h('div', { class: 'unsaved', hidden: true }, count, h('div', { class: 'row-btns' }, undoBtn, ' ', saveBtn));
    const fields = [];

    function check(f) {
      const v = f.input.value.trim();
      const n = Number(v);
      f.ok = v !== '' && /^\d+$/.test(v) && n >= f.s.min && n <= f.s.max;
      f.dirty = v !== f.s.value;
      f.input.classList.toggle('wrong', !f.ok);
      f.input.classList.toggle('changed', f.ok && f.dirty);
      f.input.setAttribute('aria-invalid', f.ok ? 'false' : 'true');
      f.hint.classList.toggle('wrong', !f.ok);
      f.hint.textContent = !f.ok ? 'Use a number from ' + fmt(f.s.min) + ' to ' + fmt(f.s.max) + '.' : f.dirty ? 'was ' + fmt(f.s.value) : (f.def.hint || '');
      const dirty = fields.filter(x => x.dirty).length;
      bar.hidden = dirty === 0;
      count.textContent = dirty === 1 ? '1 unsaved change' : dirty + ' unsaved changes';
      saveBtn.disabled = fields.some(x => x.dirty && !x.ok);
    }
    async function load() {
      try {
        const d = await api('GET', '/api/settings');
        const byName = Object.fromEntries((d.settings || []).map(s => [s.name, s]));
        fields.length = 0;
        grid.replaceChildren(...LIMITS.filter(l => byName[l.name]).map(l => {
          const s = byName[l.name];
          const id = 'set-' + l.name;
          const input = h('input', { class: 'in', id, type: 'text', inputmode: 'numeric', autocomplete: 'off', value: s.value, 'aria-describedby': id + '-hint' });
          const hint = h('span', { class: 'hint', id: id + '-hint', text: l.hint || '' });
          const f = { s, def: l, input, hint, ok: true, dirty: false };
          input.addEventListener('input', () => check(f));
          input.addEventListener('keydown', e => { if (e.key === 'Enter' && !saveBtn.disabled) saveBtn.click(); });
          fields.push(f);
          return h('label', { class: 'f', for: id }, l.label, input, hint);
        }));
        fields.forEach(check);
      } catch (e) {
        grid.replaceChildren(h('p', { class: 'empty', text: e.message }));
      }
    }
    undoBtn.addEventListener('click', () => { fields.forEach(f => { f.input.value = f.s.value; check(f); }); });
    saveBtn.addEventListener('click', () => busy(saveBtn, async () => {
      const changed = [];
      for (const f of fields.filter(x => x.dirty && x.ok)) {
        try {
          const d = await api('POST', '/api/set', { name: f.s.name, value: f.input.value.trim() });
          changed.push({ f, c: d.changed });
          f.s.value = d.changed.new;
          f.input.value = d.changed.new;
          check(f);
          f.input.classList.remove('saved');
          void f.input.offsetWidth; // restart the highlight
          f.input.classList.add('saved');
        } catch (e) {
          f.input.classList.add('wrong');
          f.hint.classList.add('wrong');
          f.hint.textContent = e.message;
          toastErr(f.def.label + ' was not saved: ' + e.message);
        }
      }
      if (!changed.length) return;
      const words = changed.map((x, i) => [i ? ', ' : '', x.f.def.label + ' is now ', h('b', { text: fmt(x.c.new) })]);
      toast([words, ' · saved and applied'], async () => {
        for (const x of changed.slice().reverse()) await api('POST', '/api/undo', { id: x.c.id });
        await load();
        toast(['Put back as it was']);
      });
    }));

    // Server actions
    const actions = h('div');
    function action(title, text, label, path, show, cls) {
      const res = h('div', { class: 'res', hidden: true });
      const b = h('button', { type: 'button', class: 'btn' + (cls ? ' ' + cls : ''), text: label });
      b.addEventListener('click', () => busy(b, async () => {
        try {
          const d = await api('POST', path);
          res.replaceChildren(...show(d));
        } catch (e) {
          res.replaceChildren(h('div', {}, h('span', { class: 'dot bad' }), h('span', { text: e.message })));
        }
        res.hidden = false;
      }));
      return h('div', { class: 'act' }, h('div', { class: 'act-text' }, h('b', { text: title }), h('span', { class: 'lbl', text: text })), b, res);
    }
    const line = (cls, text) => h('div', {}, h('span', { class: 'dot ' + cls }), h('span', { text }));
    const backupText = h('span', { class: 'lbl' });
    const updateRow = h('div', { class: 'act', hidden: true });
    actions.append(
      action('Check', 'Reads the database, changes nothing', 'Check', '/api/check', d => {
        const c = d.check || {};
        return [c.db_error ? line('bad', 'Database problem: ' + c.db_error) : line('', 'Database fine · ' + plural(c.nicks, 'nick', 'nicks') + ' · ' + plural(c.channels, 'channel', 'channels') + ' · ' + plural(c.memos, 'memo', 'memos')),
          line('', 'History kept for ' + plural(c.history_channels, 'channel', 'channels'))];
      }),
      action('Rehash', 'Re-reads beeircd.conf without a restart', 'Rehash', '/api/rehash', d => {
        const r = d.rehash || {};
        const out = [];
        if (!(r.applied || []).length && !(r.needs_restart || []).length) out.push(line('', 'Nothing had changed'));
        if ((r.applied || []).length) out.push(line('', 'Applied: ' + r.applied.join(', ')));
        if ((r.needs_restart || []).length) out.push(line('warn', 'Needs a restart: ' + r.needs_restart.join(', ')));
        return out;
      }));
    if (me.owner) {
      const b = action('Backup', '', 'Back up now', '/api/backup', d => {
        const k = d.backup || {};
        return [line('', 'Saved ' + (k.file || '').split(/[/\\]/).pop() + ' · ' + bytes(k.bytes || 0)), line('', (k.parts || []).join(', '))];
      });
      b.querySelector('.act-text').replaceChildren(h('b', { text: 'Backup' }), backupText);
      actions.append(b);
    }
    actions.append(updateRow);

    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Settings' }),
        h('span', { class: 'sub', text: 'Saved to beeircd.conf and applied at once' }))),
      h('div', { class: 'two' },
        h('section', { class: 'card' }, h('h2', { text: 'Limits' }), grid, bar),
        h('section', { class: 'card' }, h('h2', { text: 'Server actions' }), actions)));
    load();
    return {
      el,
      status(s) {
        backupText.textContent = s.last_backup ? 'Last one ' + when(s.last_backup) + ' · owner only' : 'No backup yet · owner only';
        if (s.update) {
          updateRow.hidden = false;
          updateRow.replaceChildren(h('div', { class: 'act-text' }, h('b', { text: 'Update' }),
            h('span', { class: 'lbl', text: s.update.version + ' is out. Run beeircd update on the server.' })));
        } else updateRow.hidden = true;
      },
    };
  }

  // ---------- boot ----------
  api('GET', '/api/me').then(d => { me = d.oper; start(); }).catch(e => { if (!me) showSignin(e.message); });
})();
