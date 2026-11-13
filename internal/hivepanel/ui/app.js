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
  // put replaces el's contents, skipping null/false like h() does
  // (replaceChildren itself would print them as text).
  function put(el, ...kids) {
    el.replaceChildren();
    return add(el, kids);
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
  const ms = v => (v < 1 ? 'under 1 ms' : Math.round(v) + ' ms');
  // day: '13 Nov', with the year when it isn't this year ('13 Nov 2036')
  const day = t => { const d = new Date(t); return d.toLocaleDateString('en-GB', d.getFullYear() === new Date().getFullYear() ? { day: 'numeric', month: 'short' } : { day: 'numeric', month: 'short', year: 'numeric' }); };
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
    { id: 'people', label: 'People', icon: 'people', eyebrow: 'Live list', make: peoplePage },
    { id: 'channels', label: 'Channels', icon: 'hash', eyebrow: 'Live list', make: channelsPage },
    { id: 'bans', label: 'Bans', icon: 'bans', need: 'k', eyebrow: 'Network bans', make: bansPage },
    { id: 'opers', label: 'Opers', icon: 'opers', need: 'a', eyebrow: 'Who may /oper', make: opersPage },
    { id: 'servers', label: 'Linked servers', icon: 'servers', need: 'c', eyebrow: 'Links', make: serversPage },
    { id: 'services', label: 'Services', icon: 'services', need: 'a', eyebrow: 'Built in', make: servicesPage },
    { id: 'settings', label: 'Settings', icon: 'settings', need: 'a', eyebrow: 'beeircd.conf', make: settingsPage },
  ];
  const allowed = p => !p.need || has(p.need);

  // ---------- themes (kept in this browser only) ----------
  const THEMES = [['bee', 'Bee'], ['catppuccin-latte', 'Catppuccin Latte'], ['catppuccin-mocha', 'Catppuccin Mocha'], ['one-dark-pro', 'One Dark Pro'], ['dracula', 'Dracula']];
  function getTheme() {
    let t = null;
    try { t = localStorage.getItem('hive-theme'); } catch { /* private window */ }
    return THEMES.some(x => x[0] === t) ? t : 'bee';
  }
  function setTheme(t, keep) {
    if (t === 'bee') delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = t;
    if (keep) { try { localStorage.setItem('hive-theme', t); } catch { /* private window */ } }
    document.querySelectorAll('.theme select').forEach(x => { x.value = t; });
    window.dispatchEvent(new Event('hive-theme'));
  }
  setTheme(getTheme());
  const brand = () => h('div', { class: 'brand' }, h('span', { class: 'mark' }, icon('hive')),
    h('span', { class: 'bname' }, 'Hive Panel', h('small', { text: 'NETWORK CONTROL' })));

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
      put(eye, icon(show ? 'eye-off' : 'eye'));
      pw.focus();
    });
    const err = h('span', { class: 'err', id: 'err', role: 'alert', text: msg && msg !== 'Please sign in.' ? msg : '' });
    const go = h('button', { type: 'submit', class: 'btn main', text: 'Sign in' });
    const form = h('form', { novalidate: true },
      brand(),
      h('h2', { text: 'Sign in to your server' }),
      h('p', { class: 'intro', text: 'The same oper nick and oper password as /oper.' }),
      h('label', { class: 'f', for: 'nick' }, 'Oper nick', nick),
      h('label', { class: 'f pw', for: 'pw' }, 'Oper password', pw, eye),
      err, go);
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
    put(app, h('div', { class: 'signin' }, form));
    document.title = 'Sign in · Hive Panel';
    nick.focus();
  }

  // ---------- shell ----------
  let mainEl, navEl, crumbEl, pillEl, deco;
  async function signOut() {
    await api('POST', '/api/logout').catch(() => {});
    signedOut('');
  }
  function start() {
    navEl = h('div', { class: 'nav' });
    const out = h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Sign out', title: 'Sign out' }, icon('signout'));
    out.addEventListener('click', signOut);
    const side = h('nav', { class: 'side', 'aria-label': 'Hive Panel' },
      brand(), h('div', { class: 'nlabel', text: 'Server' }), navEl,
      h('div', { class: 'me' }, h('div', { class: 'av', text: me.account.slice(0, 1) }),
        h('div', { class: 'me-text' }, h('b', { text: me.account }), h('span', { class: 'lbl', text: me.owner ? 'Owner' : (me.class || 'Oper') + ' · ' + (me.flags || 'no flags') })),
        out));
    crumbEl = h('div', { class: 'crumb' });
    pillEl = h('span', { class: 'pill', role: 'status' }, h('span', { class: 'dot' }), h('span', { text: '…' }));
    const mac = /Mac|iPhone|iPad/.test(navigator.platform || '');
    const jumpBtn = h('button', { type: 'button', class: 'jump', 'aria-label': 'Jump to a page or action', title: 'Jump to (' + (mac ? '⌘K' : 'Ctrl K') + ')' },
      icon('search'), h('span', { text: 'Jump to…' }), h('span', { class: 'kbd', text: mac ? '⌘ K' : 'Ctrl K' }));
    jumpBtn.addEventListener('click', openJump);
    const themeSel = h('select', { 'aria-label': 'Theme' }, THEMES.map(([v, t]) => h('option', { value: v, text: t })));
    themeSel.value = getTheme();
    themeSel.addEventListener('change', () => setTheme(themeSel.value, true));
    const refresh = h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Refresh this page', title: 'Refresh' }, icon('refresh'));
    refresh.addEventListener('click', () => {
      route();
      api('GET', '/api/status').then(d => { setOffline(false); gotStatus(d.status); }).catch(() => setOffline(true));
    });
    const topbar = h('header', { class: 'topbar' }, crumbEl,
      h('div', { class: 'tacts' }, jumpBtn, h('label', { class: 'theme' }, icon('palette'), h('span', { text: 'Theme' }), themeSel), pillEl, refresh));
    mainEl = h('main', { class: 'content', id: 'main', tabindex: '-1' });
    put(app, h('div', { class: 'hp' }, side, h('div', { class: 'mainwrap' }, topbar, mainEl)));
    if (deco) deco.disconnect();
    deco = new MutationObserver(() => decorate());
    deco.observe(mainEl, { childList: true, subtree: true });
    drawNav();
    route();
    startLive();
  }
  function drawNav() {
    put(navEl, ...pages.filter(allowed).map(p =>
      h('a', { href: '#/' + p.id, 'data-page': p.id, class: page && page.id === p.id ? 'on' : null,
        'aria-current': page && page.id === p.id ? 'page' : null }, icon(p.icon), p.label)));
  }
  function route() {
    if (!me) return;
    const id = (location.hash.match(/^#\/([a-z]+)/) || [])[1] || 'overview';
    let p = pages.find(x => x.id === id && allowed(x));
    if (!p) { p = pages[0]; history.replaceState(null, '', '#/overview'); }
    if (page && page.leave) page.leave();
    page = p.make();
    page.id = p.id;
    put(mainEl, page.el);
    put(crumbEl, 'Hive Panel / ', h('b', { text: p.label }));
    document.title = p.label + ' · Hive Panel';
    drawNav();
    decorate();
    if (status && page.status) page.status(status);
    if (events && page.events) page.events(events);
    mainEl.focus({ preventScroll: true });
    window.scrollTo(0, 0);
  }

  // decorate gives every page the same look without each page drawing it:
  // a small label over the title, an icon box in number tiles and in card
  // heads. It runs again when a page redraws a part (a MutationObserver).
  const HEAD_ICON = {
    'Health': 'pulse', 'What happened': 'list', 'Services': 'services', 'Bots': 'services', 'Services run on the hub': 'services',
    'Limits': 'gauge', 'Server actions': 'terminal', 'Backups': 'archive', 'Add a ban': 'plus', 'Network map': 'map',
    'Settings': 'settings', 'What the flags allow': 'opers', 'Change an oper': 'opers', 'Add an oper': 'opers',
    'This server, for the other side': 'lock', 'Server tree': 'servers', 'Link blocks': 'servers', 'Link block': 'servers', 'Not linked': 'servers',
  };
  const TILE_ICON = [[/oper/i, 'opers'], [/bot/i, 'services'], [/memo/i, 'message'], [/logged|nick/i, 'lock'], [/online|guest|people/i, 'people'],
    [/suspend|akill|zline|shun|snline/i, 'bans'], [/exception|registered/i, 'check'], [/channel|biggest/i, 'hash']];
  function decorate() {
    if (!mainEl || !page) return;
    const pg = pages.find(x => x.id === page.id) || {};
    for (const hd of mainEl.querySelectorAll('header.head:not([data-deco])')) {
      hd.dataset.deco = '1';
      const t = hd.querySelector('.head-text');
      if (t && pg.eyebrow && !t.querySelector('.eyebrow')) t.prepend(h('div', { class: 'eyebrow', text: [status && status.network, pg.eyebrow].filter(Boolean).join(' · ') }));
    }
    for (const tl of mainEl.querySelectorAll('.card.tight:not([data-deco]), .card.tile:not([data-deco])')) {
      tl.dataset.deco = '1';
      const lbl = tl.querySelector(':scope > .lbl');
      if (!lbl) continue;
      const hit = TILE_ICON.find(([re]) => re.test(lbl.textContent));
      const slab = h('div', { class: 'slab' });
      lbl.replaceWith(slab);
      slab.append(lbl, h('span', { class: 'sico' }, icon(hit ? hit[1] : pg.icon || 'hive')));
    }
    for (const h2 of mainEl.querySelectorAll('.card h2')) {
      if (h2.querySelector('.sico') || h2.closest('.nm-card, td')) continue;
      const text = h2.textContent.trim();
      const name = HEAD_ICON[text] || (/AKILL|ZLINE|SHUN|SNLINE|Exception/.test(text) ? 'bans' : pg.icon || 'hive');
      h2.prepend(h('span', { class: 'sico' }, icon(name)));
    }
  }

  // ---------- Jump to (Ctrl K): pages and quick actions in one box ----------
  let jumpEl = null;
  function quick(path, lines, what) {
    return api('POST', path).then(d => toast([what + ': ', lines(d).map(x => x.textContent).join(' · ')]))
      .catch(e => toastErr(what + ': ' + e.message));
  }
  function openJump() {
    if (!me || jumpEl) return;
    const back = document.activeElement;
    const items = [
      ...pages.filter(allowed).map(p => ({ grp: 'Pages', label: p.label, icon: p.icon, run: () => { location.hash = '#/' + p.id; } })),
      has('a') ? { grp: 'Actions', label: 'Check the config', icon: 'check', run: () => quick('/api/check', checkLines, 'Check') } : null,
      has('a') ? { grp: 'Actions', label: 'Rehash', icon: 'refresh', run: () => quick('/api/rehash', rehashLines, 'Rehash') } : null,
      me.owner ? { grp: 'Actions', label: 'Back up now', icon: 'archive', run: () => quick('/api/backup', backupLines, 'Backup') } : null,
      { grp: 'Actions', label: 'Arrange the Overview', icon: 'grip', run: () => { editNext = true; if (page && page.id === 'overview' && page.edit) { editNext = false; page.edit(true); } else location.hash = '#/overview'; } },
      ...THEMES.map(([v, t]) => ({ grp: 'Theme', label: 'Theme: ' + t, icon: 'palette', hint: getTheme() === v ? 'now' : '', run: () => setTheme(v, true) })),
      { grp: 'You', label: 'Sign out', icon: 'signout', run: signOut },
    ].filter(Boolean);
    let sel = 0, shown = items;
    const input = h('input', { type: 'text', 'aria-label': 'Jump to', placeholder: 'Type a page or an action', autocomplete: 'off', spellcheck: 'false', role: 'combobox', 'aria-expanded': 'true', 'aria-controls': 'jump-list' });
    const list = h('div', { class: 'jump-list', id: 'jump-list', role: 'listbox' });
    function draw() {
      const q = input.value.trim().toLowerCase();
      shown = items.filter(x => !q || x.label.toLowerCase().includes(q) || x.grp.toLowerCase().includes(q));
      sel = Math.min(sel, Math.max(0, shown.length - 1));
      const out = [];
      let grp = '';
      shown.forEach((x, n) => {
        if (x.grp !== grp) { grp = x.grp; out.push(h('div', { class: 'jump-grp', text: grp })); }
        const b = h('button', { type: 'button', role: 'option', id: 'jump-' + n, 'aria-selected': n === sel ? 'true' : 'false' }, icon(x.icon), x.label, x.hint ? h('small', { text: x.hint }) : null);
        b.addEventListener('click', () => go(x));
        b.addEventListener('mousemove', () => { if (sel !== n) { sel = n; mark(); } });
        out.push(b);
      });
      if (!shown.length) out.push(h('div', { class: 'jump-none', text: 'Nothing called that.' }));
      put(list, out);
      mark();
    }
    function mark() {
      list.querySelectorAll('button').forEach((b, n) => b.setAttribute('aria-selected', n === sel ? 'true' : 'false'));
      const b = list.querySelector('[aria-selected="true"]');
      if (b) { b.scrollIntoView({ block: 'nearest' }); input.setAttribute('aria-activedescendant', b.id); }
    }
    function close() {
      if (!jumpEl) return;
      jumpEl.remove(); jumpEl = null;
      if (back && back.isConnected) back.focus({ preventScroll: true });
    }
    function go(x) { close(); x.run(); }
    input.addEventListener('input', () => { sel = 0; draw(); });
    input.addEventListener('keydown', e => {
      if (e.key === 'ArrowDown') { e.preventDefault(); sel = Math.min(shown.length - 1, sel + 1); mark(); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); sel = Math.max(0, sel - 1); mark(); }
      else if (e.key === 'Enter') { e.preventDefault(); if (shown[sel]) go(shown[sel]); }
      else if (e.key === 'Escape') { e.preventDefault(); close(); }
      else if (e.key === 'Tab') e.preventDefault();
    });
    jumpEl = h('div', { class: 'jump-bg', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Jump to' },
      h('div', { class: 'jump-box' }, h('div', { class: 'jump-in' }, icon('search'), input), list));
    jumpEl.addEventListener('mousedown', e => { if (e.target === jumpEl) close(); });
    document.body.append(jumpEl);
    draw();
    input.focus();
  }
  document.addEventListener('keydown', e => {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'k' && me) { e.preventDefault(); if (jumpEl) jumpEl.querySelector('input').focus(); else openJump(); }
  });
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
    if (pillEl && !offline) { pillEl.className = 'pill'; pillEl.lastChild.textContent = 'Running · ' + since(s.started); }
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
    if (pillEl) {
      pillEl.className = on ? 'pill bad' : 'pill';
      pillEl.lastChild.textContent = on ? 'Not answering' : status ? 'Running · ' + since(status.started) : '…';
    }
    if (page && page.offline) page.offline(on);
  }

  // ---------- what server actions answer, as lines ----------
  const line = (cls, text) => h('div', {}, h('span', { class: 'dot ' + cls }), h('span', { text }));
  function checkLines(d) {
    const c = d.check || {};
    return [c.db_error ? line('bad', 'Database problem: ' + c.db_error) : line('', 'Database fine · ' + plural(c.nicks, 'nick', 'nicks') + ' · ' + plural(c.channels, 'channel', 'channels') + ' · ' + plural(c.memos, 'memo', 'memos')),
      line('', 'History kept for ' + plural(c.history_channels, 'channel', 'channels'))];
  }
  function rehashLines(d) {
    const r = d.rehash || {};
    const out = [];
    if (!(r.applied || []).length && !(r.needs_restart || []).length) out.push(line('', 'Nothing had changed'));
    if ((r.applied || []).length) out.push(line('', 'Applied: ' + r.applied.join(', ')));
    if ((r.needs_restart || []).length) out.push(line('warn', 'Needs a restart: ' + r.needs_restart.join(', ')));
    return out;
  }
  function backupLines(d) {
    const k = d.backup || {};
    return [line('', 'Saved ' + (k.file || '').split(/[/\\]/).pop() + ' · ' + bytes(k.bytes || 0))];
  }

  // ---------- Overview: blocks the oper can arrange ----------
  // Each block is a card. Wide ones fill the row; the others sit in a left
  // or right column. Order, column and which are shown are kept per oper
  // account in this browser. Blocks the oper has no flag for never show.
  const OV = [
    { id: 'stats', label: 'Numbers', wide: true },
    { id: 'room', label: 'This server', wide: true },
    { id: 'map', label: 'Network map', wide: true, need: 'c' },
    { id: 'events', label: 'What happened', col: 'l' },
    { id: 'actions', label: 'Server actions', col: 'l', can: () => has('a') || me.owner },
    { id: 'health', label: 'Health', col: 'r' },
    { id: 'services', label: 'Services', col: 'r' },
    { id: 'links', label: 'Linked servers', col: 'r' },
    { id: 'bans', label: 'Recent bans', col: 'l', need: 'k', off: true },
    { id: 'chans', label: 'Top channels', col: 'r', off: true },
    { id: 'opers', label: 'Opers online', col: 'r', off: true },
  ];
  let editNext = false;
  const ovKey = () => 'hive-overview:' + me.account.toLowerCase();
  function ovLoad(fresh) {
    let saved = null;
    if (!fresh) { try { saved = JSON.parse(localStorage.getItem(ovKey()) || 'null'); } catch { /* private window or bad data */ } }
    if (!saved || typeof saved !== 'object') saved = {};
    const ids = OV.map(b => b.id), known = Array.isArray(saved.known) ? saved.known : [];
    const order = (Array.isArray(saved.order) ? saved.order : []).filter((id, n, a) => ids.includes(id) && a.indexOf(id) === n);
    for (const id of ids) if (!order.includes(id)) order.push(id);
    const col = {};
    for (const b of OV) if (!b.wide) col[b.id] = saved.col && (saved.col[b.id] === 'l' || saved.col[b.id] === 'r') ? saved.col[b.id] : b.col;
    const hidden = Array.isArray(saved.hidden) ? saved.hidden.filter(id => ids.includes(id)) : [];
    for (const b of OV) if (b.off && !known.includes(b.id) && !hidden.includes(b.id)) hidden.push(b.id); // new extra blocks start hidden
    return { order, col, hidden };
  }
  function ovSave(L) {
    try { localStorage.setItem(ovKey(), JSON.stringify({ order: L.order, col: L.col, hidden: L.hidden, known: OV.map(b => b.id) })); } catch { /* private window */ }
  }
  // ovCard: a card with the header strip (icon box, title, one line about it)
  function ovCard(ic, title, desc, ...right) {
    const tail = h('div', { class: 'ch-r' }, right);
    const card = h('section', { class: 'card' }, h('div', { class: 'ch' },
      h('h2', {}, h('span', { class: 'sico' }, icon(ic)), h('span', { class: 'ctitle' }, title, desc ? h('span', { class: 'cdesc', text: desc }) : null)), tail));
    return { card, tail };
  }

  function overviewPage() {
    let L = ovLoad(), editing = false;
    const eyebrow = h('div', { class: 'eyebrow', text: ' ' });
    const title = h('h1', { text: 'Overview' });
    const sub = h('span', { class: 'sub', text: ' ' });
    const editBtn = h('button', { type: 'button', class: 'btn' }, icon('grip'), 'Arrange');
    const notice = h('div', { class: 'notice', role: 'alert', hidden: true, text: "The server isn't answering. The numbers below are from before." });
    const toggles = h('div', { class: 'chips' });
    const said = h('span', { class: 'sr', role: 'status' }); // what a key move did, for screen readers
    const resetBtn = h('button', { type: 'button', class: 'btn' }, 'Reset');
    const doneBtn = h('button', { type: 'button', class: 'btn main' }, 'Done');
    const arrange = h('section', { class: 'arrange', hidden: true, 'aria-label': 'Arrange the Overview' },
      h('div', { class: 'arrange-top' }, h('div', {}, h('b', { text: 'Arrange your Overview' }),
        h('p', { text: 'Drag a block by its Drag handle, or use the arrow keys on it. Switch blocks on and off here. Kept for ' + me.account + ' in this browser.' })),
        h('div', { class: 'row-btns' }, resetBtn, doneBtn)),
      toggles, said);
    const area = h('div', { class: 'main-stack' });
    const foot = h('div', { class: 'ov-foot' }, h('span'), h('span'));
    const el = h('div', { class: 'main-stack ov-page' },
      h('header', { class: 'head', 'data-deco': '1' }, h('div', { class: 'head-text' }, eyebrow, title, sub), h('div', { class: 'head-btns' }, editBtn)),
      arrange, notice, area, foot);

    const tile = (ic, label) => {
      const n = h('span', { class: 'num', text: '…' }), l = h('span', { class: 'foot-n', text: ' ' });
      return { el: h('section', { class: 'card tight', 'data-deco': '1' }, h('div', { class: 'slab' }, h('span', { text: label }), h('span', { class: 'sico' }, icon(ic))), n, l), n, l };
    };
    const rowOf = (dotCls, label, end, endCls, mono) => h('div', { class: 'row' },
      h('span', { class: mono ? 'mono' : null }, dotCls !== null ? h('span', { class: 'dot ' + dotCls }) : null, label),
      h('span', { class: 'end ' + (endCls || ''), text: end }));

    // ---- the blocks ----
    const make = {
      stats() {
        const tP = tile('people', 'People online'), tC = tile('hash', 'Channels'), tO = tile('opers', 'Opers online'), tS = tile('servers', 'Servers');
        return { el: h('div', { class: 'tiles4 ov-bold' }, tP.el, tC.el, tO.el, tS.el), float: true,
          status(s) {
            tP.n.textContent = fmt(s.online);
            tP.l.textContent = (s.max_users ? 'of ' + fmt(s.max_users) + ' · ' : '') + 'most ' + fmt(s.peak);
            tC.n.textContent = fmt(s.channels);
            tC.l.textContent = s.services_on ? plural(s.reg_channels, 'registered', 'registered') : 'registered on the hub';
            tO.n.textContent = fmt(s.opers);
            tO.l.textContent = (s.oper_nicks || []).join(', ') || 'none on this server';
            const ls = s.links || [], down = ls.filter(l => l.state !== 'up').length;
            put(tS.n, String(ls.length + 1), ' ', h('small', { text: ls.length === 0 ? 'this one' : down ? down + ' not linked' : 'all linked' }));
            const lags = ls.filter(l => l.state === 'up' && l.lag_ms).map(l => l.lag_ms);
            tS.l.textContent = lags.length ? 'slowest ping ' + ms(Math.max(...lags)) : ls.length ? 'ping not measured yet' : 'no links set up';
          } };
      },
      room() {
        const c = ovCard('gauge', 'This server', 'What services keep, and how fast this server answers');
        const fact = label => { const b = h('b', { text: '…' }); return { el: h('div', {}, h('span', { text: label }), b), b }; };
        const fN = fact('Registered nicks'), fC = fact('Registered channels'), fM = fact('Memos kept'), fR = fact('Reply time'), fMem = fact('Memory');
        c.card.append(h('div', { class: 'facts' }, fN.el, fC.el, fM.el, fR.el, fMem.el));
        return { el: c.card, tail: c.tail,
          status(s) {
            const svc = s.services_on;
            fN.b.textContent = svc ? fmt(s.nicks) : 'on the hub';
            fC.b.textContent = svc ? fmt(s.reg_channels) : 'on the hub';
            fM.b.textContent = svc ? fmt(s.memos) : 'on the hub';
            fR.b.textContent = (s.lag_avg_ms < 0.1 ? '0.1' : (Math.round(s.lag_avg_ms * 10) / 10)) + ' ms';
            fMem.b.textContent = Math.round(s.memory_mb) + ' MB';
          } };
      },
      map() {
        const open = h('button', { type: 'button', class: 'btn' }, icon('servers'), 'Linked servers');
        open.addEventListener('click', () => { location.hash = '#/servers'; });
        const nm = window.HiveNetMap ? window.HiveNetMap({ h, put, icon, api, busy, toast, toastErr, plural, fmt, when, ends, since })
          .create({ panel: true, canPlace: has('c'), canSettings: has('a'), reload: () => load(), self: () => status, details: () => { location.hash = '#/servers'; }, barExtra: open }) : null;
        const alone = h('section', { class: 'card', hidden: true }, h('h2', { text: 'Network map' }),
          h('p', { class: 'note', text: 'This server runs alone ([link] role = standalone), so there is nothing to map yet.' }));
        const box = h('div', { class: 'stack' }, nm && nm.el, alone);
        let timer = 0, seq = 0;
        async function load() {
          if (!box.isConnected) return;
          const n = ++seq;
          try {
            const d = (await api('GET', '/api/links')).links;
            if (n !== seq) return;
            const solo = d.role === 'standalone';
            alone.hidden = !solo;
            if (nm) { nm.el.hidden = solo; if (!solo) nm.update(d); }
          } catch (e) { /* the offline notice says it */ }
        }
        return { el: box, host: () => nm && !nm.el.hidden ? nm.el.querySelector('.nm-bar') : alone.querySelector('h2'),
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) load(); }, 10000); },
          status() { if (nm) nm.refresh(); },
          leave() { clearInterval(timer); if (nm) nm.destroy(); } };
      },
      events() {
        const c = ovCard('list', 'What happened', 'Sign-ins and changes made in the Hive Panel', h('span', { class: 'warnword' }, 'Live'));
        const list = h('div');
        c.card.append(list);
        return { el: c.card, tail: c.tail,
          events(lst, before) {
            if (!lst.length) { put(list, h('p', { class: 'empty', text: 'Nothing yet. Sign-ins and changes made here show up in this list.' })); return; }
            const seen = new Set((before || []).map(e => e.at + e.who + e.what));
            put(list, ...lst.slice(0, 10).map(e => h('div', { class: 'ev' + (before && !seen.has(e.at + e.who + e.what) ? ' new' : '') },
              h('span', { class: 't', text: when(e.at) }),
              h('span', {}, h('b', { text: e.who }), ' ' + e.what, e.ip ? h('span', { class: 'ip', text: ' · ' + e.ip }) : null))));
          } };
      },
      actions() {
        const c = ovCard('terminal', 'Server actions', 'Sent straight to this server');
        const res = h('div', { class: 'res', hidden: true, role: 'status' });
        const btn = (ic, label, path, lines, ok) => {
          const b = h('button', { type: 'button', class: 'btn' }, icon(ic), label);
          b.addEventListener('click', () => busy(b, async () => {
            try { const d = await api('POST', path); put(res, lines(d)); if (ok) ok(d); }
            catch (e) { put(res, line('bad', e.message)); }
            res.hidden = false;
          }));
          return b;
        };
        c.card.append(h('div', { class: 'acts-v' },
          has('a') ? btn('check', 'Check the config', '/api/check', checkLines) : null,
          me.owner ? btn('archive', 'Back up now', '/api/backup', backupLines) : null,
          has('a') ? btn('refresh', 'Rehash', '/api/rehash', rehashLines) : null, res));
        return { el: c.card, tail: c.tail };
      },
      health() {
        const word = h('span', { class: 'okword' });
        const c = ovCard('pulse', 'Health', 'Things to look at now and then', word);
        const rows = h('div');
        c.card.append(rows);
        return { el: c.card, tail: c.tail,
          status(s) {
            const out = [];
            let worst = 0;
            const mark = lvl => { worst = Math.max(worst, lvl); return ['', 'warn', 'bad'][lvl]; };
            if (s.cert) {
              const ce = s.cert, left = (new Date(ce.not_after) - Date.now()) / 86400000;
              if (ce.renew_failures > 0) out.push(rowOf(mark(2), 'Certificate', ce.renew_failures + ' renewals failed', 'bad-t'));
              else if (left < 7) out.push(rowOf(mark(left < 2 ? 2 : 1), 'Certificate', 'ends ' + day(ce.not_after)));
              else out.push(rowOf(mark(0), 'Certificate', ce.renew_at ? 'renews ' + day(ce.renew_at) : 'until ' + day(ce.not_after)));
            }
            out.push(rowOf(mark(0), 'Database', bytes(s.db_bytes || 0)));
            if (s.last_backup) out.push(rowOf(mark((Date.now() - new Date(s.last_backup)) / 86400000 > 2 ? 1 : 0), 'Last backup', when(s.last_backup)));
            else out.push(rowOf(mark(1), 'Last backup', 'none yet'));
            if (s.update) out.push(rowOf(mark(1), 'Update', s.update.version + ' is out'));
            else out.push(rowOf(mark(0), 'Update', 'up to date'));
            const sf = s.safety || {}, guards = (sf.throttled || 0) + (sf.zlined || 0) + (sf.dnsbl_hits || 0);
            out.push(rowOf(mark(0), 'Guards today', guards ? fmt(guards) + ' stopped' : 'quiet'));
            put(rows, out);
            word.className = ['okword', 'warnword', 'badword'][worst];
            word.textContent = ['All fine', 'Look at this', 'Needs you'][worst];
          } };
      },
      services() {
        const word = h('span', { class: 'okword' });
        const c = ovCard('services', 'Services', 'The built-in service bots', word);
        const n = h('b', { text: '…' }), bots = h('div', { class: 'bots' });
        c.card.append(h('div', { class: 'svc-n' }, n), bots);
        return { el: c.card, tail: c.tail,
          status(s) {
            if (!s.services_on) {
              put(n, '0', h('small', { text: 'here' })); put(bots, h('p', { class: 'empty', text: 'Services run on the hub server.' }));
              word.textContent = ''; return;
            }
            const list = s.services || [];
            put(n, String(list.length), h('small', { text: list.length === 1 ? 'service bot' : 'service bots' }));
            put(bots, list.map(b => h('span', { class: 'bot', title: BOT_TEXT[b] || '' }, h('span', { class: 'dot' }), b)));
            word.className = 'okword'; word.textContent = list.length ? 'All up' : '';
          } };
      },
      links() {
        const c = ovCard('servers', 'Linked servers', "This network's servers and their ping");
        const rows = h('div');
        c.card.append(rows);
        return { el: c.card, tail: c.tail,
          status(s) {
            const out = [rowOf('here', s.server, 'this one', null, true)];
            for (const l of s.links || []) {
              const cls = l.state === 'up' ? '' : l.state === 'down' ? 'bad' : 'warn';
              out.push(rowOf(cls, l.name, l.state === 'up' ? (l.lag_ms ? ms(l.lag_ms) : 'linked') : l.state, null, true));
            }
            put(rows, out);
          } };
      },
      bans() {
        const all = h('button', { type: 'button', class: 'btn ghost' }, 'All bans');
        all.addEventListener('click', () => { location.hash = '#/bans'; });
        const c = ovCard('bans', 'Recent bans', 'The newest AKILLs on the network', all);
        const counts = h('div', { class: 'chips' }), rows = h('div');
        c.card.append(counts, rows);
        let timer = 0;
        async function load() {
          if (!c.card.isConnected) return;
          try {
            const d = (await api('GET', '/api/bans?kind=akill')).bans;
            put(counts, KINDS.map(k => h('span', { class: 'chip2' }, k.name + ' ' + fmt(d.counts[k.kind] || 0))));
            put(rows, d.rows.length ? d.rows.slice(0, 5).map(b => rowOf(null, b.mask, ends(b.expires), null, true)) : h('p', { class: 'empty', text: 'No AKILLs.' }));
          } catch (e) { put(rows, h('p', { class: 'empty', text: e.message })); }
        }
        return { el: c.card, tail: c.tail,
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) load(); }, 30000); },
          leave() { clearInterval(timer); } };
      },
      chans() {
        const all = h('button', { type: 'button', class: 'btn ghost' }, 'All channels');
        all.addEventListener('click', () => { location.hash = '#/channels'; });
        const c = ovCard('hash', 'Top channels', 'The biggest channels right now', all);
        const rows = h('div');
        c.card.append(rows);
        let timer = 0;
        async function load() {
          if (!c.card.isConnected) return;
          try {
            const d = (await api('GET', '/api/channels?' + new URLSearchParams({ filter: 'all', offset: 0 }))).channels;
            const list = d.rows.filter(r => r.open).slice(0, 6);
            put(rows, list.length ? list.map(r => rowOf(null, r.name, plural(r.people, 'person', 'people'), null, true)) : h('p', { class: 'empty', text: 'No channels open.' }));
          } catch (e) { put(rows, h('p', { class: 'empty', text: e.message })); }
        }
        return { el: c.card, tail: c.tail,
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) load(); }, 30000); },
          leave() { clearInterval(timer); } };
      },
      opers() {
        const c = ovCard('opers', 'Opers online', 'Opers on every server of the network');
        const rows = h('div');
        c.card.append(rows);
        let timer = 0;
        async function load() {
          if (!c.card.isConnected) return;
          try {
            const d = (await api('GET', '/api/people?' + new URLSearchParams({ filter: 'opers', offset: 0 }))).people;
            put(rows, d.rows.length ? d.rows.slice(0, 8).map(r => rowOf('', r.nick, r.server + ' · ' + since(r.signon), null, true)) : h('p', { class: 'empty', text: 'No opers online.' }));
          } catch (e) { put(rows, h('p', { class: 'empty', text: e.message })); }
        }
        return { el: c.card, tail: c.tail,
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) load(); }, 30000); },
          leave() { clearInterval(timer); } };
      },
    };

    // ---- build the blocks the oper may see ----
    const B = {};
    for (const b of OV) {
      if (b.need && !has(b.need)) continue;
      if (b.can && !b.can()) continue;
      const x = make[b.id]();
      x.def = b;
      x.handle = h('button', { type: 'button', class: 'handle' + (x.float ? ' float' : ''), 'aria-label': 'Move ' + b.label + ' (arrow keys)', title: 'Drag to move' }, icon('grip'), 'Drag');
      x.wrap = h('div', { class: 'blk', 'data-blk': b.id }, x.float ? x.handle : null, x.el);
      if (!x.float) {
        const host = x.host ? null : x.tail;
        if (host) host.append(x.handle);
      }
      B[b.id] = x;
    }
    const visible = () => L.order.filter(id => B[id] && !L.hidden.includes(id));
    function placeHandles() {
      for (const x of Object.values(B)) if (x.host && !x.float) { const hst = x.host(); if (hst && x.handle.parentNode !== hst) hst.append(x.handle); }
    }
    function layout() {
      const shownBefore = new Set([...area.querySelectorAll('[data-blk]')].map(e => e.dataset.blk));
      area.replaceChildren();
      let cols = null;
      for (const id of visible()) {
        const x = B[id];
        if (x.def.wide) { cols = null; area.append(x.wrap); continue; }
        if (!cols) {
          cols = { l: h('div', { class: 'ov-col', 'data-col': 'l' }), r: h('div', { class: 'ov-col', 'data-col': 'r' }) };
          area.append(h('div', { class: 'ov-cols' }, cols.l, cols.r));
        }
        cols[L.col[id]].append(x.wrap);
      }
      placeHandles();
      for (const id of visible()) {
        const x = B[id];
        if (!shownBefore.has(id) && x.show) x.show();
        if (status && x.status) x.status(status);
        if (events && x.events) x.events(events, null);
      }
      for (const x of Object.values(B)) if (x.handle) x.handle.tabIndex = editing ? 0 : -1;
    }
    function drawToggles() {
      put(toggles, Object.values(B).map(x => {
        const on = !L.hidden.includes(x.def.id);
        const t = h('button', { type: 'button', class: 'togg', 'aria-pressed': on ? 'true' : 'false' }, icon(on ? 'minus' : 'plus'), x.def.label);
        t.addEventListener('click', () => {
          L.hidden = on ? [...L.hidden, x.def.id] : L.hidden.filter(id => id !== x.def.id);
          ovSave(L); layout(); drawToggles();
        });
        return t;
      }));
    }
    function setEditing(on) {
      editing = on;
      el.classList.toggle('editing', on);
      arrange.hidden = !on;
      put(editBtn, icon(on ? 'check' : 'grip'), on ? 'Done' : 'Arrange');
      if (on) drawToggles();
      for (const x of Object.values(B)) if (x.handle) x.handle.tabIndex = on ? 0 : -1;
      if (on) arrange.scrollIntoView({ block: 'nearest' });
    }
    editBtn.addEventListener('click', () => setEditing(!editing));
    doneBtn.addEventListener('click', () => { setEditing(false); editBtn.focus(); });
    resetBtn.addEventListener('click', () => {
      try { localStorage.removeItem(ovKey()); } catch { /* private window */ }
      L = ovLoad(true); ovSave(L); layout(); drawToggles();
      toast(['The Overview is back to how it came']);
    });

    // ---- moving blocks: pointer (mouse, touch, pen) and keys ----
    let drag = null;
    const drop = h('div', { class: 'drop', text: 'DROP HERE', 'aria-hidden': 'true' });
    function where(x, y) {
      const under = document.elementFromPoint(x, y);
      if (!under || !area.contains(under) || under === drop) return null; // over the drop spot: keep it
      const blk = under.closest('.blk');
      if (blk && blk !== drag.x.wrap) {
        const r = blk.getBoundingClientRect(), after = y > r.top + r.height / 2;
        const inCol = blk.parentNode.classList.contains('ov-col');
        if (drag.x.def.wide && inCol) { const cols = blk.parentNode.parentNode; return { ref: cols, after: y > cols.getBoundingClientRect().top + cols.getBoundingClientRect().height / 2 }; }
        return { ref: blk, after };
      }
      const col = under.closest('.ov-col');
      if (col && !drag.x.def.wide) {
        // empty space under a column's last block: the end of that column
        const last = [...col.children].filter(n => n !== drop && n !== drag.x.wrap).pop();
        if (!last || y > last.getBoundingClientRect().bottom) return { col };
      }
      return null;
    }
    function dragMove(e) {
      if (!drag) return;
      if (!drag.on && Math.hypot(e.clientX - drag.x0, e.clientY - drag.y0) < 5) return;
      if (!drag.on) { drag.on = true; drag.x.wrap.classList.add('dragging'); document.body.classList.add('drag-on'); }
      const w = where(e.clientX, e.clientY);
      if (w && w.col) { if (drop.parentNode !== w.col || drop.nextSibling) w.col.append(drop); }
      else if (w) { if (w.after) w.ref.after(drop); else w.ref.before(drop); }
      // scroll near the edges while dragging
      if (e.clientY < 70) window.scrollBy(0, -12); else if (e.clientY > window.innerHeight - 50) window.scrollBy(0, 12);
    }
    function dragEnd(e, cancel) {
      if (!drag) return;
      const d = drag;
      drag = null;
      document.body.classList.remove('drag-on');
      d.x.wrap.classList.remove('dragging');
      try { d.x.handle.releasePointerCapture(e.pointerId); } catch { /* gone */ }
      if (!d.on || cancel || !drop.parentNode) { drop.remove(); return; }
      const id = d.x.def.id;
      const order = [];
      for (const n of area.querySelectorAll('[data-blk], .drop')) {
        if (n === drop) order.push(id);
        else if (n.dataset.blk !== id) order.push(n.dataset.blk);
      }
      const col = drop.parentNode.classList.contains('ov-col') ? drop.parentNode.dataset.col : null;
      drop.remove();
      const rest = L.order.filter(x => !order.includes(x)); // hidden blocks keep their place at the end
      L.order = [...order, ...rest];
      if (col && !d.x.def.wide) L.col[id] = col;
      ovSave(L); layout();
      d.x.handle.focus({ preventScroll: true });
    }
    for (const x of Object.values(B)) {
      x.handle.addEventListener('pointerdown', e => {
        if (!editing || e.button > 0) return;
        e.preventDefault();
        try { x.handle.setPointerCapture(e.pointerId); } catch { /* not a live pointer */ }
        drag = { x, x0: e.clientX, y0: e.clientY, on: false };
      });
      x.handle.addEventListener('pointermove', dragMove);
      x.handle.addEventListener('pointerup', e => dragEnd(e, false));
      x.handle.addEventListener('pointercancel', e => dragEnd(e, true));
      x.handle.addEventListener('keydown', e => {
        if (!editing) return;
        const id = x.def.id, vis = visible(), at = vis.indexOf(id);
        if (e.key === 'Escape' && drag) { dragEnd(e, true); return; }
        if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
          // move past the next shown block (in the same column for column blocks)
          const same = vis.filter(v => B[v].def.wide === x.def.wide && (x.def.wide || L.col[v] === L.col[id]));
          const k = same.indexOf(id), other = same[k + (e.key === 'ArrowUp' ? -1 : 1)];
          e.preventDefault();
          if (!other) return;
          const o = L.order.slice(), a = o.indexOf(id), b = o.indexOf(other);
          o[a] = other; o[b] = id; L.order = o;
        } else if ((e.key === 'ArrowLeft' || e.key === 'ArrowRight') && !x.def.wide) {
          e.preventDefault();
          const to = e.key === 'ArrowLeft' ? 'l' : 'r';
          if (L.col[id] === to) return;
          L.col[id] = to;
        } else return;
        void at;
        ovSave(L); layout(); x.handle.focus({ preventScroll: true });
        said.textContent = x.def.label + ' moved ' + (e.key === 'ArrowUp' ? 'up' : e.key === 'ArrowDown' ? 'down' : e.key === 'ArrowLeft' ? 'to the left column' : 'to the right column');
      });
    }

    function onStatus(s) {
      eyebrow.textContent = [s.network, (s.links || []).length ? plural((s.links || []).length + 1, 'server', 'servers') + ' linked' : 'one server'].filter(Boolean).join(' · ');
      title.textContent = s.server;
      sub.textContent = 'BeeIRCd ' + s.version + ' · running since ' + when(s.started);
      put(foot, h('span', { text: 'BeeIRCd ' + s.version }), h('span', { text: 'Updated ' + new Date().toLocaleTimeString('en-GB') }));
      for (const id of visible()) if (B[id].status) B[id].status(s);
    }
    layout();
    if (editNext) { editNext = false; setTimeout(() => setEditing(true), 0); }
    return {
      el,
      status: onStatus,
      events(list, before) { for (const id of visible()) if (B[id].events) B[id].events(list, before); },
      edit: setEditing,
      offline(on) { notice.hidden = !on; },
      leave() { for (const x of Object.values(B)) if (x.leave) x.leave(); if (drag) dragEnd({}, true); },
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
        put(grid, ...LIMITS.filter(l => byName[l.name]).map(l => {
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
        put(grid, h('p', { class: 'empty', text: e.message }));
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
          put(res, ...show(d));
        } catch (e) {
          put(res, h('div', {}, h('span', { class: 'dot bad' }), h('span', { text: e.message })));
        }
        res.hidden = false;
      }));
      return h('div', { class: 'act' }, h('div', { class: 'act-text' }, h('b', { text: title }), h('span', { class: 'lbl', text: text })), b, res);
    }
    const updateRow = h('div', { class: 'act', hidden: true });
    actions.append(
      action('Check', 'Reads the database, changes nothing', 'Check', '/api/check', checkLines),
      action('Rehash', 'Re-reads beeircd.conf without a restart', 'Rehash', '/api/rehash', rehashLines));
    actions.append(updateRow);
    const backups = me.owner ? backupsCard() : null;

    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Settings' }),
        h('span', { class: 'sub', text: 'Saved to beeircd.conf and applied at once' }))),
      h('div', { class: 'two' },
        h('section', { class: 'card' }, h('h2', { text: 'Limits' }), grid, bar),
        h('section', { class: 'card' }, h('h2', { text: 'Server actions' }), actions)),
      backups && backups.el);
    load();
    return {
      el,
      status(s) {
        if (backups) backups.status(s);
        if (s.update) {
          updateRow.hidden = false;
          put(updateRow, h('div', { class: 'act-text' }, h('b', { text: 'Update' }),
            h('span', { class: 'lbl', text: s.update.version + ' is out. Run beeircd update on the server.' })));
        } else updateRow.hidden = true;
      },
    };
  }

  // ---------- People ----------
  function peoplePage() {
    const PAGE = 50;
    let q = '', filter = 'all', offset = 0, openUID = null, data = null, typing = 0;
    const tile = label => { const n = h('span', { class: 'num', text: '…' }); return { n, el: h('section', { class: 'card tight' }, h('span', { class: 'lbl', text: label }), n) }; };
    const tOn = tile('Online'), tIn = tile('Logged in'), tGuests = tile('Guests'), tOpers = tile('Opers');
    const search = h('input', { class: 'search', type: 'search', placeholder: has('h') ? 'Search nick, host, address or account' : 'Search nick, host or account', 'aria-label': 'Search people', autocomplete: 'off', spellcheck: 'false' });
    const segs = [['all', 'All'], ['in', 'Logged in'], ['guests', 'Guests'], ['opers', 'Opers']].map(([v, label]) =>
      h('button', { type: 'button', 'aria-pressed': v === filter ? 'true' : 'false', 'data-f': v, text: label }));
    const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Show' }, segs);
    const tbody = h('tbody');
    const count = h('span', { class: 'lbl' });
    const back = h('button', { type: 'button', class: 'btn', text: 'Back' });
    const next = h('button', { type: 'button', class: 'btn', text: 'Next' });
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'People' }),
        h('span', { class: 'sub', text: 'Everyone online on the network, on every server' }))),
      h('div', { class: 'tiles4' }, tOn.el, tIn.el, tGuests.el, tOpers.el),
      h('section', { class: 'card list-card' },
        h('div', { class: 'tools' }, search, seg),
        h('div', { class: 'wrap' }, h('table', {},
          h('thead', {}, h('tr', {}, ['Nick', 'Host', 'Server', 'Channels', 'Online for'].map(t => h('th', { scope: 'col', text: t })))),
          tbody)),
        h('div', { class: 'pager' }, count, h('div', { class: 'row-btns' }, back, next))));

    async function load() {
      try {
        const d = await api('GET', '/api/people?' + new URLSearchParams({ q, filter, offset }));
        data = d.people;
        draw();
      } catch (e) {
        put(tbody, h('tr', {}, h('td', { colspan: 5, class: 'empty', text: e.message })));
      }
    }
    function draw() {
      const p = data;
      tOn.n.textContent = fmt(p.online); tIn.n.textContent = fmt(p.logged_in); tGuests.n.textContent = fmt(p.guests); tOpers.n.textContent = fmt(p.opers);
      const rows = [];
      if (!p.rows.length) rows.push(h('tr', {}, h('td', { colspan: 5, class: 'empty', text: q || filter !== 'all' ? 'Nobody matches.' : 'Nobody is online.' })));
      for (const r of p.rows) {
        const open = r.uid === openUID;
        const nickBtn = h('button', { type: 'button', class: 'rowbtn', 'aria-expanded': open ? 'true' : 'false' }, r.nick);
        const tr = h('tr', { class: 'person' + (open ? ' open' : '') },
          h('td', {}, nickBtn, r.oper ? [' ', h('span', { class: 'tag', text: 'oper' })] : null),
          h('td', { class: 'mono', text: r.host }),
          h('td', { class: 'lbl', text: r.server }),
          h('td', { text: String(r.channels) }),
          h('td', { class: 'lbl', text: since(r.signon) }));
        tr.addEventListener('click', () => toggle(r.uid));
        rows.push(tr);
        if (open) rows.push(detailRow(r));
      }
      put(tbody, ...rows);
      const from = p.matched ? p.offset + 1 : 0, to = p.offset + p.rows.length;
      count.textContent = fmt(from) + ' to ' + fmt(to) + ' of ' + fmt(p.matched);
      back.disabled = p.offset <= 0;
      next.disabled = to >= p.matched;
    }
    function toggle(uid) {
      openUID = openUID === uid ? null : uid;
      draw();
    }

    // The open person: their details and what can be done.
    function detailRow(r) {
      const box = h('div', {}, h('p', { class: 'empty', text: 'Loading…' }));
      const tr = h('tr', { class: 'detail' }, h('td', { colspan: 5 }, box));
      api('GET', '/api/person?uid=' + encodeURIComponent(r.uid)).then(d => fill(box, d.person))
        .catch(e => put(box, h('p', { class: 'empty', text: e.message })));
      return tr;
    }
    function item(label, value, cls, wide) {
      return h('div', { class: wide ? 'wide' : null }, h('span', { class: 'lbl', text: label }), h('span', { class: cls || null, text: value || '—' }));
    }
    function fill(box, p) {
      const items = [];
      if (p.ip) items.push(item('Real host', p.real_host === p.ip ? p.ip : p.real_host + ' (' + p.ip + ')', 'mono', true));
      items.push(item('Account', p.account || 'guest'));
      items.push(item('Secure', p.tls ? 'TLS' : 'not encrypted'));
      items.push(item('Ident', p.ident, 'mono'));
      items.push(item('Real name', p.realname));
      items.push(item('Channels', (p.chans || []).join(' ') || 'none', 'mono', true));
      if (p.local) items.push(item('Idle', p.idle ? since(p.idle) : '—'));
      items.push(item('Modes', p.modes, 'mono'));
      if (p.away) items.push(item('Away', p.away, null, true));
      if (p.oper_as) items.push(item('Oper', p.oper_as));

      // notice
      const note = h('input', { class: 'in', id: 'note-' + p.uid, type: 'text', maxlength: '400', placeholder: 'Type a message', autocomplete: 'off' });
      const send = h('button', { type: 'button', class: 'btn main', text: 'Send notice' });
      const doSend = () => busy(send, async () => {
        if (!note.value.trim()) { note.classList.add('wrong'); note.focus(); return; }
        try {
          const d = await api('POST', '/api/notice', { uid: p.uid, text: note.value });
          note.value = ''; note.classList.remove('wrong');
          toast([d.done.text]);
        } catch (e) { toastErr(e.message); }
      });
      send.addEventListener('click', doSend);
      note.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); doSend(); } });

      // disconnect / ban, each asks first
      const ends = [];
      const confirmBox = h('div', { class: 'confirm', hidden: true });
      function ask(kind) {
        const reason = h('input', { class: 'in', type: 'text', maxlength: '200', autocomplete: 'off',
          placeholder: 'Reason (they see it)', 'aria-label': 'Reason' });
        const life = h('select', { class: 'in', 'aria-label': 'Ban length' },
          [['1h', '1 hour'], ['1d', '1 day'], ['7d', '7 days'], ['30d', '30 days'], ['0', 'Until removed']].map(([v, t]) => h('option', { value: v, text: t })));
        life.value = '7d';
        const go = h('button', { type: 'button', class: 'btn bad', text: kind === 'kill' ? 'Disconnect ' + p.nick : 'Ban ' + p.nick });
        const cancel = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
        cancel.addEventListener('click', () => { confirmBox.hidden = true; });
        go.addEventListener('click', () => busy(go, async () => {
          try {
            const d = await api('POST', kind === 'kill' ? '/api/kill' : '/api/ban', { uid: p.uid, text: reason.value, life: kind === 'ban' ? life.value : '' });
            toast([d.done.text]);
            openUID = null;
            load();
          } catch (e) { toastErr(e.message); }
        }));
        put(confirmBox, ...[
          h('label', { class: 'f' }, kind === 'kill' ? 'Disconnect ' + p.nick + '?' : 'Ban ' + p.nick + ' from the whole network' + (p.ip ? ' (*@' + p.ip + ')' : '') + '?', reason),
          kind === 'ban' ? life : null, go, cancel].filter(Boolean));
        confirmBox.hidden = false;
        reason.focus();
      }
      if (has('k')) {
        const kb = h('button', { type: 'button', class: 'btn bad', text: 'Disconnect' });
        kb.addEventListener('click', () => ask('kill'));
        const bb = h('button', { type: 'button', class: 'btn bad', text: 'Ban ' + p.nick });
        bb.addEventListener('click', () => ask('ban'));
        ends.push(kb, bb);
      }
      put(box, 
        h('div', { class: 'dl' }, items),
        h('div', { class: 'acts' },
          h('div', { class: 'grow' }, h('label', { class: 'f', for: 'note-' + p.uid }, 'Notice to ' + p.nick, note), send),
          ends.length ? h('div', { class: 'end' }, ends) : null), // h() skips null
        confirmBox);
    }

    // controls
    search.addEventListener('input', () => {
      clearTimeout(typing);
      typing = setTimeout(() => { q = search.value.trim(); offset = 0; openUID = null; load(); }, 250);
    });
    for (const b of segs) b.addEventListener('click', () => {
      filter = b.dataset.f; offset = 0; openUID = null;
      segs.forEach(x => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
      load();
    });
    back.addEventListener('click', () => { offset = Math.max(0, offset - PAGE); openUID = null; load(); });
    next.addEventListener('click', () => { offset += PAGE; openUID = null; load(); });
    const onKey = e => { if (e.key === 'Escape' && openUID) { openUID = null; draw(); } };
    document.addEventListener('keydown', onKey);
    // Fresh list every 15 s, unless a person is open (don't move things under the oper's hands).
    const timer = setInterval(() => { if (!openUID && !document.hidden) load(); }, 15000);
    load();
    return { el, leave() { clearInterval(timer); document.removeEventListener('keydown', onKey); } };
  }

  // ---------- Bans ----------
  const KINDS = [
    { kind: 'akill', name: 'AKILL', what: 'user@host', life: '30d', ph: '*@203.0.113.9' },
    { kind: 'zline', name: 'ZLINE', what: 'IP, before connect', life: '30d', ph: '203.0.113.0/24' },
    { kind: 'shun', name: 'SHUN', what: "stays, can't talk", life: '30d', ph: '*@203.0.113.9' },
    { kind: 'snline', name: 'SNLINE', what: 'by realname', life: '30d', ph: '*free bitcoin*', field: 'Realname',
      hint: "* and ? as wildcards, or a /regex/. A nick takes that person's realname.", note: 'BeeIRCd servers only' },
    { kind: 'eline', name: 'Exception', what: 'never banned', life: '0', ph: '*@staff.example.org' },
  ];
  const kindOf = k => KINDS.find(x => x.kind === k) || KINDS[0];
  function ends(t) {
    if (!t) return 'never';
    const s = (new Date(t) - Date.now()) / 1000;
    if (s < 60) return 'in under a minute';
    if (s < 3570) return 'in ' + Math.round(s / 60) + ' min';
    if (s < 86400 * 2 - 1800) return 'in ' + Math.round(s / 3600) + ' h';
    return 'in ' + Math.round(s / 86400) + ' days';
  }
  function bansPage() {
    let kind = 'akill', data = null, freshMask = null, matchTimer = 0, matchSeq = 0;
    const tiles = KINDS.map(k => {
      const n = h('span', { class: 'num', text: '…' });
      const b = h('button', { type: 'button', class: 'card tile', 'aria-pressed': k.kind === kind ? 'true' : 'false' },
        h('span', { class: 'lbl', text: k.name }), n, h('span', { class: 'lbl', text: k.what }));
      b.addEventListener('click', () => { kind = k.kind; tiles.forEach(t => t.b.setAttribute('aria-pressed', t.k === k ? 'true' : 'false')); type.value = kind; typeChanged(); load(); });
      return { k, b, n };
    });
    const title = h('h2');
    const listNote = h('span', { class: 'lbl', text: 'newest first' });
    const maskHead = h('th', { scope: 'col', text: 'Mask' });
    const tbody = h('tbody');

    // add form
    const type = h('select', { class: 'in', id: 'ban-type' }, KINDS.map(k => h('option', { value: k.kind, text: k.name + ' · ' + k.what })));
    const mask = h('input', { class: 'in mono', id: 'ban-mask', type: 'text', autocomplete: 'off', spellcheck: 'false' });
    const hit = h('span', { class: 'hit', role: 'status' });
    const maskLabel = h('span', { text: 'Mask' });
    const maskHint = h('span', { class: 'hint', id: 'ban-mask-hint', hidden: true });
    const reason = h('input', { class: 'in', id: 'ban-reason', type: 'text', maxlength: '200', autocomplete: 'off', placeholder: 'They see it' });
    const life = h('select', { class: 'in', id: 'ban-life' },
      [['1h', 'in 1 hour'], ['1d', 'in 1 day'], ['7d', 'in 7 days'], ['30d', 'in 30 days'], ['0', 'never (until removed)']].map(([v, t]) => h('option', { value: v, text: t })));
    const add = h('button', { type: 'button', class: 'btn main full', text: 'Add ban' });
    function typeChanged() {
      const k = kindOf(type.value);
      mask.placeholder = k.ph;
      maskLabel.textContent = k.field || 'Mask';
      maskHint.textContent = k.hint || '';
      maskHint.hidden = !k.hint;
      if (k.hint) mask.setAttribute('aria-describedby', 'ban-mask-hint'); else mask.removeAttribute('aria-describedby');
      life.value = k.life;
      add.textContent = k.kind === 'eline' ? 'Add exception' : 'Add ' + k.name;
      checkMask();
    }
    function checkMask() {
      clearTimeout(matchTimer);
      const v = mask.value.trim();
      mask.classList.remove('wrong');
      if (!v) { hit.className = 'hit'; hit.textContent = ''; return; }
      const seq = ++matchSeq;
      matchTimer = setTimeout(async () => {
        try {
          const d = await api('GET', '/api/ban-match?' + new URLSearchParams({ kind: type.value, mask: v }));
          if (seq !== matchSeq) return;
          const n = d.match.people, ex = type.value === 'eline';
          hit.className = 'hit' + (n ? ' some' : '');
          put(hit, h('span', { class: 'dot ' + (n ? 'warn' : 'off') }),
            (ex ? 'Covers ' : 'Matches ') + plural(n, 'person', 'people') + ' online' + (d.match.mask !== v ? ' · saved as ' + d.match.mask : ''));
        } catch (e) {
          if (seq !== matchSeq) return;
          hit.className = 'hit bad';
          hit.textContent = e.message;
          mask.classList.add('wrong');
        }
      }, 300);
    }
    mask.addEventListener('input', checkMask);
    type.addEventListener('change', typeChanged);
    add.addEventListener('click', () => busy(add, async () => {
      if (!mask.value.trim()) { mask.classList.add('wrong'); mask.focus(); return; }
      try {
        const d = await api('POST', '/api/ban-add', { kind: type.value, mask: mask.value.trim(), reason: reason.value, life: life.value });
        toast([d.done.text]);
        freshMask = (d.done.text.match(/ on (\S+) /) || [])[1] || null;
        if (type.value !== kind) { kind = type.value; tiles.forEach(t => t.b.setAttribute('aria-pressed', t.k.kind === kind ? 'true' : 'false')); }
        mask.value = ''; reason.value = ''; checkMask();
        load();
      } catch (e) {
        hit.className = 'hit bad'; hit.textContent = e.message; mask.classList.add('wrong');
      }
    }));

    async function load() {
      try {
        const d = await api('GET', '/api/bans?kind=' + kind);
        data = d.bans;
        draw();
      } catch (e) {
        put(tbody, h('tr', {}, h('td', { colspan: 6, class: 'empty', text: e.message })));
      }
    }
    function draw() {
      for (const t of tiles) t.n.textContent = fmt(data.counts[t.k.kind] || 0);
      const k = kindOf(kind);
      title.textContent = k.kind === 'eline' ? 'Exceptions' : k.name + 's';
      listNote.textContent = 'newest first' + (k.note ? ' · ' + k.note : '');
      maskHead.textContent = k.field || 'Mask';
      if (!data.rows.length) {
        put(tbody, h('tr', {}, h('td', { colspan: 6, class: 'empty', text: k.kind === 'eline' ? 'No exceptions.' : 'No ' + k.name + 's.' })));
        return;
      }
      put(tbody, ...data.rows.map(b => {
        const cell = h('td', { class: 'end-cell' });
        const trash = h('button', { type: 'button', class: 'x', 'aria-label': 'Remove ' + kindOf(b.kind).name + ' on ' + b.mask, title: 'Remove' }, icon('trash'));
        cell.append(trash);
        trash.addEventListener('click', () => {
          const yes = h('button', { type: 'button', class: 'btn bad', text: 'Remove' });
          const no = h('button', { type: 'button', class: 'btn ghost', text: 'Keep' });
          no.addEventListener('click', () => put(cell, trash));
          yes.addEventListener('click', () => busy(yes, async () => {
            try {
              const d = await api('POST', '/api/ban-del', { kind: b.kind, mask: b.mask });
              toast([d.done.text]);
              load();
            } catch (e) { toastErr(e.message); put(cell, trash); }
          }));
          put(cell, h('span', { class: 'rm' }, yes, no));
          yes.focus();
        });
        return h('tr', { class: b.mask === freshMask ? 'fresh' : null },
          h('td', {}, h('span', { class: 'ty ' + b.kind, text: kindOf(b.kind).name })),
          h('td', { class: 'mono', text: b.mask }),
          h('td', { text: b.reason }),
          h('td', { text: b.set_by }),
          h('td', { class: 'lbl', text: ends(b.expires), title: b.expires ? new Date(b.expires).toLocaleString('en-GB') : 'until removed' }),
          cell);
      }));
      freshMask = null;
    }

    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Bans' }),
        h('span', { class: 'sub', text: 'Network bans reach every linked server' }))),
      h('div', { class: 'tiles5' }, tiles.map(t => t.b)),
      h('div', { class: 'band21' },
        h('section', { class: 'card list-card' },
          h('div', { class: 'ch tools' }, title, listNote),
          h('div', { class: 'wrap' }, h('table', { class: 'bans' },
            h('thead', {}, h('tr', {}, h('th', { scope: 'col', text: 'Type' }), maskHead, ['Reason', 'Set by', 'Ends'].map(t => h('th', { scope: 'col', text: t })), h('th', { scope: 'col' }, h('span', { class: 'sr', text: 'Remove' })))),
            tbody))),
        h('section', { class: 'card' }, h('h2', { text: 'Add a ban' }),
          h('label', { class: 'f', for: 'ban-type' }, 'Type', type),
          h('label', { class: 'f', for: 'ban-mask' }, maskLabel, mask, maskHint),
          hit,
          h('label', { class: 'f', for: 'ban-reason' }, 'Reason', reason),
          h('label', { class: 'f', for: 'ban-life' }, 'Ends', life),
          add)));
    typeChanged();
    load();
    const timer = setInterval(() => { if (!document.hidden && !el.querySelector('.rm')) load(); }, 30000);
    return { el, leave() { clearInterval(timer); } };
  }

  // ---------- Backups card (Settings, owner only) ----------
  function backupsCard() {
    const tbody = h('tbody');
    const sub = h('span', { class: 'lbl', text: 'Config, services, history and certificates · owner only' });
    const make = h('button', { type: 'button', class: 'btn', text: 'Back up now' });
    let list = null, open = null, lastSeen = null;
    make.addEventListener('click', () => busy(make, async () => {
      try {
        const d = await api('POST', '/api/backup');
        const k = d.backup || {};
        toast(['Saved ', h('b', { text: (k.file || '').split(/[/\\]/).pop() }), ' · ' + bytes(k.bytes || 0)]);
        open = null;
        await load();
      } catch (e) { toastErr('No backup made: ' + e.message); }
    }));
    async function load() {
      try {
        const d = await api('GET', '/api/backups');
        list = d.backups;
        draw();
      } catch (e) {
        put(tbody, h('tr', {}, h('td', { colspan: 4, class: 'empty', text: e.message })));
      }
    }
    function save(blob, name) {
      const a = h('a', { href: URL.createObjectURL(blob), download: name, hidden: true });
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(a.href), 60000);
    }
    function askRow(f) {
      const pw = h('input', { class: 'in', id: 'bk-pw', type: 'password', autocomplete: 'current-password', maxlength: 256 });
      const msg = h('span', { class: 'hint' });
      const no = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
      const go = h('button', { type: 'submit', class: 'btn main' }, icon('download'), 'Download');
      no.addEventListener('click', () => { open = null; draw(); });
      const form = h('form', { class: 'bk-ask' },
        h('span', { text: "This file holds password hashes and the server's private keys. Keep it somewhere safe." }),
        h('div', { class: 'bk-row' },
          h('label', { class: 'f', for: 'bk-pw' }, 'Your password again', pw, msg),
          h('div', { class: 'bk-btns' }, no, go)));
      const wrong = text => {
        pw.value = '';
        pw.classList.add('wrong'); msg.classList.add('wrong');
        msg.textContent = text;
        pw.focus();
      };
      form.addEventListener('submit', e => {
        e.preventDefault();
        if (!pw.value) { wrong('Type your password.'); return; }
        busy(go, async () => {
          let res;
          try {
            res = await fetch('/api/backup-get', { method: 'POST', credentials: 'same-origin',
              headers: { 'X-Hive': '1', 'Content-Type': 'application/json' }, body: JSON.stringify({ name: f.name, password: pw.value }) });
          } catch (err) {
            wrong("Can't reach the Hive Panel. Is beeircd-web running?");
            return;
          }
          if (!res.ok || !(res.headers.get('Content-Type') || '').startsWith('application/gzip')) {
            const data = await res.json().catch(() => ({}));
            if (res.status === 401 && data.signin) { signedOut(data.error || 'Please sign in.'); return; }
            wrong(data.error || 'Something went wrong (' + res.status + ').');
            return;
          }
          let blob;
          try { blob = await res.blob(); } catch (err) { wrong('The download broke off. Please try again.'); return; }
          pw.value = '';
          save(blob, f.name);
          toast(['Downloaded ', h('b', { text: f.name }), ' · ' + bytes(blob.size)]);
          open = null;
          draw();
        });
      });
      pw.addEventListener('input', () => { pw.classList.remove('wrong'); msg.classList.remove('wrong'); msg.textContent = ''; });
      setTimeout(() => pw.focus(), 0);
      return h('tr', { class: 'detail bk-detail' }, h('td', { colspan: 4 }, form));
    }
    function draw() {
      sub.textContent = 'Config, services, history and certificates · newest ' + fmt(list.keep) + ' kept on the server · owner only';
      if (!list.files.length) {
        put(tbody, h('tr', {}, h('td', { colspan: 4, class: 'empty', text: 'No backups yet. Back up now makes the first one.' })));
        return;
      }
      if (open && !list.files.some(f => f.name === open)) open = null;
      put(tbody, ...list.files.map(f => {
        const dl = h('button', { type: 'button', class: 'x dl-btn', 'aria-label': 'Download ' + f.name, title: 'Download', 'aria-expanded': open === f.name ? 'true' : 'false' }, icon('download'));
        dl.addEventListener('click', () => { open = open === f.name ? null : f.name; draw(); });
        const w = when(f.made), made = /^\d/.test(w) ? 'today ' + w : w;
        const row = h('tr', { class: open === f.name ? 'open' : null },
          h('td', {}, h('div', { class: 'bk-made' }, h('b', { text: made.charAt(0).toUpperCase() + made.slice(1) }), h('span', { class: 'mono lbl', text: f.name }))),
          h('td', { class: 'lbl bk-by', text: f.by ? f.by + ', in the panel' : 'beeircd backup' }),
          h('td', { class: 'bk-size', text: bytes(f.bytes) }),
          h('td', { class: 'end-cell' }, dl));
        return open === f.name ? [row, askRow(f)] : row;
      }));
    }
    const el = h('section', { class: 'card list-card bk-card' },
      h('div', { class: 'tools bk-head' },
        h('div', { class: 'head-text' }, h('h2', { text: 'Backups' }), sub), make),
      h('div', { class: 'wrap' }, h('table', { class: 'backups' },
        h('thead', {}, h('tr', {}, h('th', { scope: 'col', text: 'Made' }), h('th', { scope: 'col', class: 'bk-by', text: 'By' }), h('th', { scope: 'col', text: 'Size' }), h('th', { scope: 'col' }, h('span', { class: 'sr', text: 'Download' })))),
        tbody)));
    load();
    return {
      el,
      status(s) {
        // A backup made elsewhere (the timer, beeircd backup) shows up.
        if (s.last_backup && lastSeen && s.last_backup !== lastSeen && !open) load();
        lastSeen = s.last_backup || lastSeen;
      },
    };
  }

  // ---------- Opers ----------
  const FLAGS = [
    ['a', 'admin', 'Settings, rehash, opers'],
    ['b', 'bots', 'BotServ bots: add, change, delete'],
    ['c', 'links', 'Linked servers'],
    ['g', 'global', 'Notices to everyone'],
    ['h', 'hosts', 'See real hosts, HostServ'],
    ['k', 'kill', 'Disconnect people, network bans'],
    ['s', 'services', 'Other OperServ tools'],
  ];
  function chips(all) {
    return h('span', { class: 'fl', 'aria-label': 'flags ' + (all || 'none') },
      FLAGS.map(([f]) => h('span', { class: 'c' + (all.includes(f) ? '' : ' no'), text: f, 'aria-hidden': 'true' })));
  }
  function opersPage() {
    let data = null, open = null; // open: account name, or '+' when adding
    const tbody = h('tbody');
    const form = h('section', { class: 'card' });
    const addBtn = h('button', { type: 'button', class: 'btn', text: 'Add oper' });
    addBtn.addEventListener('click', () => { open = '+'; draw(); });

    async function load(keep) {
      try {
        data = (await api('GET', '/api/opers')).opers;
        if (!keep && open && open !== '+' && !data.rows.some(r => r.account === open)) open = null;
        draw();
      } catch (e) { put(tbody, h('tr', {}, h('td', { colspan: 5, class: 'empty', text: e.message }))); }
    }
    function draw() {
      put(tbody, ...data.rows.map(r => {
        const btn = h('button', { type: 'button', class: 'rowbtn', 'aria-expanded': r.account === open ? 'true' : 'false' },
          r.account, r.owner ? icon('lock', 'lock') : null);
        const tr = h('tr', { class: 'person' + (r.account === open ? ' open' : '') },
          h('td', {}, btn, r.disabled ? [' ', h('span', { class: 'tag', text: 'disabled' })] : null),
          h('td', { text: r.class || '—' }),
          h('td', {}, chips(r.all || '')),
          h('td', { class: 'mono', text: (r.hosts || []).join(' ') }),
          h('td', {}, h('span', { class: 'dot ' + (r.online ? '' : 'off') }), ' ', h('span', { class: 'lbl', text: r.online ? 'online' : 'offline' })));
        tr.addEventListener('click', () => { open = open === r.account ? null : r.account; draw(); });
        return tr;
      }));
      drawForm();
    }

    function drawForm() {
      if (!open) {
        put(form, h('h2', { text: 'Change an oper' }), h('p', { class: 'form-empty', text: 'Click an oper to change them, or Add oper.' }));
        return;
      }
      const adding = open === '+';
      const found = adding ? { account: '' } : data.rows.find(x => x.account === open);
      if (!found) { open = null; drawForm(); return; }
      // Empty fields are left out of the server's answer.
      const r = Object.assign({ class: '', flags: '', all: '', hosts: [], password: false }, found);
      if (r.owner) {
        put(form, h('div', { class: 'ch' }, h('h2', { text: r.account }), h('span', { class: 'lbl', text: 'owner' })),
          h('p', { class: 'note', text: 'The owner is the server admin in beeircd.conf, with every flag. Change it there; the owner\'s oper password is changed with /as oper ' + r.account + ' set password.' }),
          chips(r.all));
        return;
      }
      const nick = adding ? h('input', { class: 'in', id: 'op-nick', type: 'text', autocomplete: 'off', placeholder: 'Their registered nick' }) : null;
      const cls = h('select', { class: 'in', id: 'op-class' }, h('option', { value: '', text: 'No class (own flags only)' }),
        data.classes.map(c => h('option', { value: c.name, text: c.name + ' · ' + (c.flags || 'no flags'), disabled: [...c.flags].some(f => !has(f)) && r.class !== c.name })));
      cls.value = r.class || '';
      const boxes = {};
      const picks = h('div', { class: 'picks' });
      function drawPicks() {
        const cf = (data.classes.find(c => c.name === cls.value) || { flags: '' }).flags;
        put(picks, ...FLAGS.map(([f, name]) => {
          const fromClass = cf.includes(f);
          const mine = !has(f); // can't give or take it
          const box = boxes[f] || h('input', { type: 'checkbox' });
          boxes[f] = box;
          box.checked = fromClass || r.flags.includes(f) && !box.dataset.touched || box.dataset.touched === 'on';
          box.disabled = fromClass || mine;
          box.addEventListener('change', () => { box.dataset.touched = box.checked ? 'on' : 'off'; });
          return h('label', { class: 'pk' + (box.disabled ? ' locked' : ''), title: fromClass ? 'From the class' : mine ? 'You don\'t have this flag' : '' },
            box, h('span', { class: 'mono', text: f }), ' ' + name + (fromClass ? ' (class)' : ''));
        }));
      }
      cls.addEventListener('change', drawPicks);
      drawPicks();
      const hosts = h('input', { class: 'in mono', id: 'op-hosts', type: 'text', autocomplete: 'off', value: r.hosts.join(' '), placeholder: '*@192.168.50.* or *@*' });
      const pw = h('input', { class: 'in', id: 'op-pw', type: 'password', autocomplete: 'new-password', placeholder: adding ? 'At least 8 characters' : (r.password ? 'Leave empty to keep' : 'None set yet: set one') });
      const err = h('span', { class: 'err', role: 'alert' });
      const save = h('button', { type: 'button', class: 'btn main', text: adding ? 'Add oper' : 'Save' });
      save.addEventListener('click', () => busy(save, async () => {
        err.textContent = '';
        const cf = (data.classes.find(c => c.name === cls.value) || { flags: '' }).flags;
        const own = FLAGS.map(([f]) => f).filter(f => boxes[f].checked && !cf.includes(f)).join('');
        const name = adding ? nick.value.trim() : r.account;
        if (adding && !name) { err.textContent = 'Type their nick.'; nick.focus(); return; }
        if (!hosts.value.trim()) { err.textContent = 'Say where they may /oper from, like *@*'; hosts.focus(); return; }
        if (pw.value && pw.value.length < 8) { err.textContent = 'The oper password needs at least 8 characters.'; pw.focus(); return; }
        try {
          const d = await api('POST', '/api/oper-save', { name, class: cls.value, flags: own, hosts: hosts.value, password: pw.value, new: adding });
          pw.value = '';
          const c = d.changed;
          open = c.name;
          toast([adding ? c.name + ' is an oper now: ' : c.name + ' now has ', h('b', { text: c.new || 'no flags' })],
            c.id ? async () => { await api('POST', '/api/undo', { id: c.id }); await load(); toast(['Put back as it was']); } : null);
          await load(true);
        } catch (e) { err.textContent = e.message; }
      }));
      const rm = adding ? null : h('button', { type: 'button', class: 'btn bad', text: 'Remove oper' });
      if (rm) rm.addEventListener('click', () => {
        if (rm.dataset.sure !== '1') { rm.dataset.sure = '1'; rm.textContent = 'Sure? Remove ' + r.account; return; }
        busy(rm, async () => {
          try {
            const d = await api('POST', '/api/oper-del', { name: r.account });
            open = null;
            toast([d.changed.name + ' is no longer an oper'], d.changed.id ? async () => { await api('POST', '/api/undo', { id: d.changed.id }); open = r.account; await load(); toast(['Put back as it was']); } : null);
            await load();
          } catch (e) { err.textContent = e.message; rm.dataset.sure = ''; rm.textContent = 'Remove oper'; }
        });
      });
      put(form, ...[
        h('div', { class: 'ch' }, h('h2', { text: adding ? 'Add an oper' : r.account }),
          !adding && r.added_by ? h('span', { class: 'lbl', text: 'added by ' + r.added_by }) : null),
        adding ? h('label', { class: 'f', for: 'op-nick' }, 'Nick', nick) : null,
        h('label', { class: 'f', for: 'op-class' }, 'Class', cls),
        h('fieldset', { class: 'f plain' }, h('legend', { class: 'lbl', text: 'Flags' }), picks),
        h('span', { class: 'note', text: 'You can give and take only the flags you have.' }),
        h('label', { class: 'f', for: 'op-hosts' }, 'Allowed from', hosts),
        h('label', { class: 'f', for: 'op-pw' }, adding ? 'Oper password' : 'New oper password', pw),
        err,
        h('div', { class: 'btns' }, save, rm)].filter(Boolean)); // replaceChildren would print null
      (adding ? nick : cls).focus({ preventScroll: true });
    }

    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Opers' }),
        h('span', { class: 'sub', text: 'The same opers as /oper, on every server' })), addBtn),
      h('div', { class: 'band21' },
        h('section', { class: 'card list-card' },
          h('div', { class: 'wrap' }, h('table', { class: 'opers' },
            h('thead', {}, h('tr', {}, ['Oper', 'Class', 'Flags', 'Allowed from', 'Now'].map(t => h('th', { scope: 'col', text: t })))),
            tbody))),
        form),
      h('section', { class: 'card' }, h('h2', { text: 'What the flags allow' }),
        h('div', { class: 'leg' }, FLAGS.map(([f, , text]) => h('div', {}, h('span', { class: 'c', text: f }), h('span', { text }))))));
    const onKey = e => { if (e.key === 'Escape' && open && !e.target.closest('input, select')) { open = null; draw(); } };
    document.addEventListener('keydown', onKey);
    load();
    return { el, leave() { document.removeEventListener('keydown', onKey); } };
  }

  // ---------- Linked servers ----------
  function serversPage() {
    let data = null, open = null, timer = 0; // open: block name, or '+' when adding
    const sub = h('span', { class: 'sub' });
    const addBtn = h('button', { type: 'button', class: 'btn', text: 'Add a server', hidden: true });
    addBtn.addEventListener('click', () => { open = '+'; drawForm(); });
    const nm = window.HiveNetMap ? window.HiveNetMap({ h, put, icon, api, busy, toast, toastErr, plural, fmt, when, ends, since }).create({ panel: true, canPlace: has('c'), canSettings: has('a'), reload: () => load(), self: () => status }) : null;
    const tbody = h('tbody');
    const form = h('section', { class: 'card' });
    const mine = h('section', { class: 'card' });
    const left = h('div', { class: 'stack' },
      h('section', { class: 'card list-card' }, h('div', { class: 'ch tools' }, h('h2', { text: 'Link blocks' })),
        h('div', { class: 'wrap' }, h('table', { class: 'links' },
          h('thead', {}, h('tr', {}, ['Server', 'State', 'Up since', 'Software'].map(t => h('th', { scope: 'col', text: t })), h('th', { scope: 'col' }, h('span', { class: 'sr', text: 'Actions' })))),
          tbody))));
    const body = h('div', { class: 'band21' }, left, h('div', { class: 'stack' }, form, mine));
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Linked servers' }), sub), addBtn),
      nm && nm.el, body);

    async function load() {
      try {
        data = (await api('GET', '/api/links')).links;
        draw();
      } catch (e) { put(tbody, h('tr', {}, h('td', { colspan: 5, class: 'empty', text: e.message }))); }
    }
    function draw() {
      const d = data;
      if (d.role === 'standalone') {
        sub.textContent = 'This server runs alone';
        if (nm) nm.el.hidden = true;
        put(body, h('section', { class: 'card' }, h('h2', { text: 'Not linked' }),
          h('p', { class: 'note', text: 'This server runs alone ([link] role = standalone). To link servers, run beeircd install on the new server and answer the hub questions, or set [link] role = hub in beeircd.conf and restart. Then link blocks are added here.' })));
        return;
      }
      addBtn.hidden = false;
      if (nm) { nm.el.hidden = false; nm.update(d); }
      const st = Object.fromEntries((d.status || []).map(s => [s.name.toLowerCase(), s]));
      const up = (d.status || []).filter(s => s.state === 'up');
      const lags = up.filter(s => s.lag_ms).map(s => s.lag_ms);
      sub.textContent = d.map.length + (d.map.length === 1 ? ' server' : ' servers') + (lags.length ? ' · slowest ping ' + ms(Math.max(...lags)) : '') + ' · this one is the ' + d.role;

      // link blocks with their state
      if (!d.blocks.length) {
        put(tbody, h('tr', {}, h('td', { colspan: 5, class: 'empty', text: 'No link blocks yet. Add a server.' })));
      } else put(tbody, ...d.blocks.map(b => {
        const s = st[b.name.toLowerCase()] || { state: 'down' };
        const state = h('span', { class: 'row-state' }, h('span', { class: 'dot ' + (s.state === 'up' ? '' : s.state === 'down' ? 'bad' : 'warn') }), ' ', s.state === 'up' ? 'linked' : s.state);
        const acts = h('td', { class: 'cell-acts' });
        const btnFor = (label, cls, f) => { const x = h('button', { type: 'button', class: 'btn ' + cls, text: label }); x.addEventListener('click', e => { e.stopPropagation(); f(x); }); return x; };
        const conn = btnFor('Connect', '', x => busy(x, async () => {
          try { toast([(await api('POST', '/api/link-connect', { name: b.name })).done.text]); setTimeout(load, 2500); } catch (e) { toastErr(e.message); }
        }));
        const split = btnFor('Split', 'bad', () => {
          const reason = h('input', { class: 'in', type: 'text', placeholder: 'Reason', 'aria-label': 'Reason' });
          reason.addEventListener('click', e => e.stopPropagation());
          const yes = btnFor('Split ' + b.name, 'bad', x => busy(x, async () => {
            try { toast([(await api('POST', '/api/link-split', { name: b.name, reason: reason.value })).done.text]); await load(); } catch (e) { toastErr(e.message); }
          }));
          const no = btnFor('Keep', 'ghost', () => draw());
          put(acts, h('span', { class: 'split-ask' }, reason, yes, no));
          reason.focus();
        });
        if (s.state === 'up') acts.append(split);
        else if (b.host && s.state === 'down') acts.append(conn);
        const tr = h('tr', { class: 'person' + (open === b.name ? ' open' : '') },
          h('td', {}, h('button', { type: 'button', class: 'rowbtn mono', 'aria-expanded': open === b.name ? 'true' : 'false', text: b.name })),
          h('td', {}, state),
          h('td', { class: 'lbl', text: s.state === 'up' ? since(s.since) : s.error || (b.host ? '—' : 'connects to us') }),
          h('td', { class: 'lbl', text: s.software || '—' }),
          acts);
        tr.addEventListener('click', () => { open = open === b.name ? null : b.name; draw(); });
        return tr;
      }));

      put(mine, h('h2', { text: 'This server, for the other side' }),
        h('div', { class: 'dl1' },
          h('span', { class: 'lbl', text: 'Name' }), h('span', { class: 'mono', text: d.self }),
          h('span', { class: 'lbl', text: 'Link port' }), h('span', { class: 'mono', text: String(d.link_port || '') }),
          h('span', { class: 'lbl', text: 'Fingerprint' }), h('span', { class: 'fp', text: d.fingerprint || '' })),
        h('p', { class: 'note', text: 'The other server\'s link block needs these. On a BeeIRCd, beeircd link-info prints them too.' }));
      if (!form.dataset.dirty) drawForm();
    }

    function drawForm() {
      form.dataset.dirty = '';
      if (!open) {
        put(form, h('h2', { text: 'Link block' }), h('p', { class: 'form-empty', text: 'Click a server to change its link block, or Add a server.' }));
        return;
      }
      const adding = open === '+';
      const found = adding ? {} : data.blocks.find(b => b.name === open);
      if (!found) { open = null; drawForm(); return; }
      const b = Object.assign({ name: '', host: '', port: 0, fingerprint: '', autoconnect: false, has_password: false }, found);
      const name = h('input', { class: 'in mono', id: 'lk-name', type: 'text', value: b.name, autocomplete: 'off', placeholder: 'leaf2.example.net', readonly: adding ? null : true });
      const host = h('input', { class: 'in mono', id: 'lk-host', type: 'text', value: b.host, autocomplete: 'off', placeholder: 'Empty: it connects to us' });
      const port = h('input', { class: 'in mono', id: 'lk-port', type: 'text', inputmode: 'numeric', value: b.port ? String(b.port) : '', placeholder: String(data.link_port || 7000) });
      const fp = h('input', { class: 'in mono', id: 'lk-fp', type: 'text', value: b.fingerprint, autocomplete: 'off', spellcheck: 'false', placeholder: '44 characters, ends with =' });
      const pw = h('input', { class: 'in', id: 'lk-pw', type: 'password', autocomplete: 'new-password', placeholder: b.has_password ? 'Leave empty to keep' : 'Optional' });
      const nopw = b.has_password ? h('input', { type: 'checkbox', id: 'lk-nopw' }) : null;
      const auto = h('input', { type: 'checkbox', id: 'lk-auto' });
      auto.checked = b.autoconnect;
      for (const x of [name, host, port, fp, pw]) x.addEventListener('input', () => { form.dataset.dirty = '1'; });
      const err = h('span', { class: 'err', role: 'alert' });
      const save = h('button', { type: 'button', class: 'btn main', text: adding ? 'Add server' : 'Save' });
      save.addEventListener('click', () => busy(save, async () => {
        err.textContent = '';
        const portN = port.value.trim() ? Number(port.value.trim()) : 0;
        if (adding && !name.value.trim()) { err.textContent = 'Type the other server\'s name.'; name.focus(); return; }
        if (port.value.trim() && !(Number.isInteger(portN) && portN > 0 && portN < 65536)) { err.textContent = 'The port is a number from 1 to 65535.'; port.focus(); return; }
        if (!fp.value.trim()) { err.textContent = 'Paste the other server\'s fingerprint.'; fp.focus(); return; }
        try {
          const d = await api('POST', '/api/link-save', { name: name.value.trim(), host: host.value.trim(), port: portN, fingerprint: fp.value.trim(),
            password: pw.value, no_password: nopw ? nopw.checked : false, autoconnect: auto.checked, new: adding });
          open = adding ? name.value.trim() : open;
          form.dataset.dirty = '';
          toast([d.done.text]);
          await load();
        } catch (e) { err.textContent = e.message; }
      }));
      const rm = adding ? null : h('button', { type: 'button', class: 'btn bad', text: 'Remove' });
      if (rm) rm.addEventListener('click', () => {
        if (rm.dataset.sure !== '1') { rm.dataset.sure = '1'; rm.textContent = 'Sure? It unlinks'; return; }
        busy(rm, async () => {
          try { toast([(await api('POST', '/api/link-del', { name: b.name })).done.text]); open = null; await load(); }
          catch (e) { err.textContent = e.message; rm.dataset.sure = ''; rm.textContent = 'Remove'; }
        });
      });
      put(form,
        h('h2', { text: adding ? 'Add a server' : b.name }),
        h('label', { class: 'f', for: 'lk-name' }, 'Server name', name),
        h('div', { class: 'grid2' }, h('label', { class: 'f', for: 'lk-host' }, 'Address', host), h('label', { class: 'f', for: 'lk-port' }, 'Port', port)),
        h('label', { class: 'f', for: 'lk-fp' }, 'Fingerprint', fp),
        h('span', { class: 'note', text: 'The fingerprint locks the link: get it with beeircd link-info on the other server (or ./unrealircd spkifp).' }),
        h('label', { class: 'f', for: 'lk-pw' }, 'Link password', pw),
        nopw ? h('label', { class: 'ck', for: 'lk-nopw' }, nopw, 'Remove the password') : null,
        h('label', { class: 'ck', for: 'lk-auto' }, auto, 'Connect by itself (needs an address)'),
        err,
        h('div', { class: 'btns' }, save, rm));
      (adding ? name : host).focus({ preventScroll: true });
    }

    load();
    timer = setInterval(() => { if (!document.hidden && !el.querySelector('.split-ask')) load(); }, 10000);
    return { el, status() { if (nm) nm.refresh(); }, leave() { clearInterval(timer); if (nm) nm.destroy(); } };
  }

  // ---------- Services ----------
  const SVC_GROUPS = [
    ['Accounts', [
      ['nick-expire', 'Unused accounts are dropped after'],
      ['identify-within', 'Time to identify before the Guest rename'],
      ['guest-prefix', 'Guest nicks start with'],
      ['kill-quick', 'NickServ SET KILL QUICK gives'],
      ['allow-kill-immed', 'People may choose SET KILL IMMED'],
    ]],
    ['Channels', [['chan-expire', 'Unused channels are dropped after']]],
    ['Network bans', [['ban-expiry', 'Bans without a length last']]],
    ['Services log', [
      ['log-channel', 'Log channel (off for none)'],
      ['log-connects', 'Also log connects and quits'],
    ]],
  ];
  const BOT_TEXT = {
    NickServ: 'Registers nicks, logs people in', ChanServ: 'Registers and guards channels',
    MemoServ: 'Messages for people who are away', OperServ: 'Oper tools and network bans',
    HostServ: 'Virtual hosts', AdminServ: 'Server settings and opers', BotServ: 'Channel bots', Global: 'Notices to everyone',
  };
  // toSecs reads 90s / 30m / 12h / 30d / 2w (a bare number is seconds); -1 when it isn't a time.
  function toSecs(v) {
    const m = /^(\d+)\s*([smhdw]?)$/i.exec(v.trim());
    return m ? Number(m[1]) * { '': 1, s: 1, m: 60, h: 3600, d: 86400, w: 604800 }[m[2].toLowerCase()] : -1;
  }
  function secs(d) { return d >= 86400 && d % 86400 === 0 ? d / 86400 + 'd' : d >= 3600 && d % 3600 === 0 ? d / 3600 + 'h' : d >= 60 && d % 60 === 0 ? d / 60 + 'm' : d + 's'; }
  function servicesPage() {
    const tile = label => { const n = h('span', { class: 'num', text: '…' }); return { n, el: h('section', { class: 'card tight' }, h('span', { class: 'lbl', text: label }), n) }; };
    const tNicks = tile('Registered nicks'), tChans = tile('Registered channels'), tMemos = tile('Memos waiting'), tBots = tile('Bots');
    const bots = h('div');
    const form = h('div', { class: 'svc-form' }, h('p', { class: 'empty', text: 'Loading…' }));
    const count = h('span');
    const undoBtn = h('button', { type: 'button', class: 'btn ghost', text: 'Undo' });
    const saveBtn = h('button', { type: 'button', class: 'btn main', text: 'Save' });
    const bar = h('div', { class: 'unsaved', hidden: true }, count, h('div', { class: 'row-btns' }, undoBtn, saveBtn));
    const off = h('section', { class: 'card', hidden: true }, h('h2', { text: 'Services run on the hub' }),
      h('p', { class: 'note', text: 'This server has its services switched off ([services] enabled = false): the hub of the network runs them. Change these settings in the hub\'s Hive Panel.' }));
    const main = h('div', { class: 'band21' },
      h('section', { class: 'card' }, h('h2', { text: 'Settings' }), form, bar),
      h('section', { class: 'card' }, h('h2', { text: 'Bots' }), bots));
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Services' }),
        h('span', { class: 'sub', text: 'NickServ, ChanServ and the other bots, built in' }))),
      off,
      h('div', { class: 'tiles4' }, tNicks.el, tChans.el, tMemos.el, tBots.el),
      main);
    const fields = [];

    function check(f) {
      const s = f.s;
      if (s.kind === 'onoff') {
        f.dirty = (f.input.checked ? 'on' : 'off') !== s.value;
        f.ok = true;
      } else {
        const v = f.input.value.trim();
        f.dirty = v !== s.value;
        const sec = s.kind === 'time' ? toSecs(v) : 0;
        f.ok = v !== '' && (s.kind !== 'time' || sec >= s.min && sec <= s.max) && (s.kind !== 'text' || v.length <= s.max);
        f.input.classList.toggle('wrong', !f.ok);
        f.input.classList.toggle('changed', f.ok && f.dirty);
        f.hint.classList.toggle('wrong', !f.ok);
        f.hint.textContent = !f.ok ? (s.kind === 'time' ? 'A time like 90s, 30m, 12h or 30d (' + secs(s.min) + ' to ' + secs(s.max) + ').' : 'Up to ' + s.max + ' characters.')
          : f.dirty ? 'was ' + s.value : (s.kind === 'time' ? secs(s.min) + ' to ' + secs(s.max) : '');
      }
      const dirty = fields.filter(x => x.dirty).length;
      bar.hidden = dirty === 0;
      count.textContent = dirty === 1 ? '1 unsaved change' : dirty + ' unsaved changes';
      saveBtn.disabled = fields.some(x => x.dirty && !x.ok);
    }
    async function load() {
      try {
        const d = await api('GET', '/api/settings');
        const by = Object.fromEntries((d.settings || []).map(s => [s.name, s]));
        fields.length = 0;
        const groups = SVC_GROUPS.map(([title, list]) => h('fieldset', { class: 'plain svc-group' },
          h('legend', { class: 'grp', text: title }),
          list.filter(([n]) => by[n]).map(([n, label]) => {
            const s = by[n], id = 'svc-' + n;
            let input, hint = h('span', { class: 'hint', id: id + '-hint' });
            if (s.kind === 'onoff') {
              input = h('input', { type: 'checkbox', id });
              input.checked = s.value === 'on';
              const f = { s, input, hint, ok: true, dirty: false, label };
              input.addEventListener('change', () => check(f));
              fields.push(f);
              return h('label', { class: 'ck', for: id }, input, label);
            }
            input = h('input', { class: 'in' + (s.kind === 'text' ? '' : ' mono'), id, type: 'text', value: s.value, autocomplete: 'off', spellcheck: 'false', 'aria-describedby': id + '-hint' });
            const f = { s, input, hint, ok: true, dirty: false, label };
            input.addEventListener('input', () => check(f));
            input.addEventListener('keydown', e => { if (e.key === 'Enter' && !saveBtn.disabled) saveBtn.click(); });
            fields.push(f);
            return h('label', { class: 'f', for: id }, label, input, hint);
          })));
        put(form, groups);
        fields.forEach(check);
      } catch (e) { put(form, h('p', { class: 'empty', text: e.message })); }
    }
    undoBtn.addEventListener('click', () => { fields.forEach(f => { if (f.s.kind === 'onoff') f.input.checked = f.s.value === 'on'; else f.input.value = f.s.value; check(f); }); });
    saveBtn.addEventListener('click', () => busy(saveBtn, async () => {
      const done = [];
      for (const f of fields.filter(x => x.dirty && x.ok)) {
        const value = f.s.kind === 'onoff' ? (f.input.checked ? 'on' : 'off') : f.input.value.trim();
        try {
          const d = await api('POST', '/api/set', { name: f.s.name, value });
          done.push(d.changed);
          f.s.value = d.changed.new;
          if (f.s.kind !== 'onoff') f.input.value = d.changed.new;
          check(f);
          f.input.classList.remove('saved'); void f.input.offsetWidth; f.input.classList.add('saved');
        } catch (e) {
          if (f.s.kind !== 'onoff') { f.input.classList.add('wrong'); f.hint.classList.add('wrong'); f.hint.textContent = e.message; }
          toastErr(f.label + ': ' + e.message);
        }
      }
      if (!done.length) return;
      toast([done.length === 1 ? [fields.find(x => x.s.name === done[0].name).label + ': ', h('b', { text: done[0].new })] : plural(done.length, 'setting', 'settings') + ' saved', ' · applied'], async () => {
        for (const c of done.slice().reverse()) await api('POST', '/api/undo', { id: c.id });
        await load();
        toast(['Put back as it was']);
      });
    }));
    load();
    return {
      el,
      status(s) {
        off.hidden = s.services_on;
        main.hidden = !s.services_on;
        tNicks.n.textContent = fmt(s.nicks); tChans.n.textContent = fmt(s.reg_channels); tMemos.n.textContent = fmt(s.memos);
        const list = s.services || [];
        tBots.n.textContent = fmt(list.length);
        put(bots, list.map(b => h('div', { class: 'row' }, h('span', {}, h('span', { class: 'dot' }), b), h('span', { class: 'end', text: BOT_TEXT[b] || 'up' }))));
      },
    };
  }

  // ---------- Channels ----------
  function channelsPage() {
    const PAGE = 50;
    let q = '', filter = 'all', offset = 0, open = null, data = null, typing = 0;
    const tile = label => { const n = h('span', { class: 'num', text: '…' }), l = h('span', { class: 'lbl' }); return { n, l, el: h('section', { class: 'card tight' }, h('span', { class: 'lbl', text: label }), n, l) }; };
    const tOpen = tile('Channels open'), tReg = tile('Registered'), tBig = tile('Biggest'), tSus = tile('Suspended');
    const search = h('input', { class: 'search', type: 'search', placeholder: 'Search name or topic', 'aria-label': 'Search channels', autocomplete: 'off', spellcheck: 'false' });
    const segs = [['all', 'All'], ['registered', 'Registered'], ['unregistered', 'Not registered'], ['suspended', 'Suspended']].map(([v, label]) =>
      h('button', { type: 'button', 'aria-pressed': v === filter ? 'true' : 'false', 'data-f': v, text: label }));
    const tbody = h('tbody');
    const count = h('span', { class: 'lbl' });
    const back = h('button', { type: 'button', class: 'btn', text: 'Back' });
    const next = h('button', { type: 'button', class: 'btn', text: 'Next' });
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Channels' }),
        h('span', { class: 'sub', text: 'Every channel on the network, on every server' }))),
      h('div', { class: 'tiles4' }, tOpen.el, tReg.el, tBig.el, tSus.el),
      h('section', { class: 'card list-card' },
        h('div', { class: 'tools' }, search, h('div', { class: 'seg', role: 'group', 'aria-label': 'Show' }, segs)),
        h('div', { class: 'wrap' }, h('table', { class: 'chans' },
          h('thead', {}, h('tr', {}, ['Channel', 'People', 'Topic', 'Modes', 'ChanServ'].map(t => h('th', { scope: 'col', text: t })))),
          tbody)),
        h('div', { class: 'pager' }, count, h('div', { class: 'row-btns' }, back, next))));

    async function load() {
      try {
        data = (await api('GET', '/api/channels?' + new URLSearchParams({ q, filter, offset }))).channels;
        draw();
      } catch (e) { put(tbody, h('tr', {}, h('td', { colspan: 5, class: 'empty', text: e.message }))); }
    }
    function draw() {
      const d = data;
      tOpen.n.textContent = fmt(d.open); tReg.n.textContent = fmt(d.registered); tSus.n.textContent = fmt(d.suspended);
      tBig.n.textContent = fmt(d.biggest_n || 0); tBig.l.textContent = d.biggest || '—';
      const rows = [];
      if (!d.rows.length) rows.push(h('tr', {}, h('td', { colspan: 5, class: 'empty', text: q || filter !== 'all' ? 'No channel matches.' : 'No channels open.' })));
      for (const r of d.rows) {
        const isOpen = r.name === open;
        const tr = h('tr', { class: 'person' + (isOpen ? ' open' : '') },
          h('td', {}, h('button', { type: 'button', class: 'rowbtn mono', 'aria-expanded': isOpen ? 'true' : 'false', text: r.name })),
          h('td', { text: r.open ? String(r.people) : '—' }),
          h('td', {}, r.topic ? h('span', { class: 'topic', title: r.topic, text: r.topic }) : h('span', { class: 'lbl', text: '—' })),
          h('td', { class: 'mono lbl', text: r.modes || '' }),
          h('td', {}, r.suspended ? h('span', { class: 'tag sus', text: 'suspended' }) : r.registered ? h('span', { class: 'tag reg', text: 'registered' }) : h('span', { class: 'lbl', text: '—' })));
        tr.addEventListener('click', () => { open = open === r.name ? null : r.name; draw(); });
        rows.push(tr);
        if (isOpen) rows.push(detailRow(r.name));
      }
      put(tbody, rows);
      const from = d.matched ? d.offset + 1 : 0, to = d.offset + d.rows.length;
      count.textContent = fmt(from) + ' to ' + fmt(to) + ' of ' + fmt(d.matched) + ' · biggest first';
      back.disabled = d.offset <= 0; next.disabled = to >= d.matched;
    }
    function item(label, value, cls, wide) {
      return h('div', { class: wide ? 'span4' : null }, h('span', { class: 'lbl', text: label }), h('span', { class: cls || null, text: value || '—' }));
    }
    function detailRow(name) {
      const box = h('div', {}, h('p', { class: 'empty', text: 'Loading…' }));
      api('GET', '/api/channel?name=' + encodeURIComponent(name)).then(d => fill(box, d.channel)).catch(e => put(box, h('p', { class: 'empty', text: e.message })));
      return h('tr', { class: 'detail' }, h('td', { colspan: 5 }, box));
    }
    function fill(box, c) {
      const items = [];
      items.push(item('Topic', c.topic ? c.topic + (c.topic_by ? ' · set by ' + c.topic_by + (c.topic_at ? ', ' + when(c.topic_at) : '') : '') : (c.open ? 'no topic' : '—'), null, true));
      if (c.registered) {
        items.push(item('Founder', c.founder), item('Registered', c.registered_at ? day(c.registered_at) : ''), item('Last used', c.last_used ? when(c.last_used) : ''));
      }
      if (c.open) {
        const ex = [c.excepts ? plural(c.excepts, 'exception', 'exceptions') : '', c.invites ? plural(c.invites, 'invite', 'invites') : ''].filter(Boolean).join(' · ');
        items.push(item('Bans', fmt(c.bans) + (ex ? ' · ' + ex : '')));
        let modes = c.modes || 'none';
        if (c.has_key) modes += ' (key hidden)';
        if (c.limit) modes += ' · limit ' + c.limit;
        if (c.mlock) modes += ' · ChanServ keeps ' + c.mlock;
        items.push(item('Modes', modes, 'mono', !!c.mlock || modes.length > 28));
      }
      if (c.description) items.push(item('Description', c.description, null, true));
      if (c.suspended) items.push(item('Suspended', 'by ' + c.suspended_by + ' · ' + (c.suspend_why || 'no reason') + (c.suspend_till ? ' · until ' + day(c.suspend_till) : ' · until unsuspended'), 'badword', true));
      const parts = [h('div', { class: 'dl' }, items)];
      if (c.open) {
        const shown = c.members.slice(0, 40);
        parts.push(h('div', { class: 'f' }, h('span', { class: 'lbl', text: 'People here (' + fmt(c.people) + ') · ops first' }),
          h('div', { class: 'chips' }, shown.map(m => h('span', { class: 'chip2' + (/^[~&@%]/.test(m) ? ' op' : m.startsWith('+') ? ' v' : ''), text: m })),
            c.members.length > shown.length ? h('span', { class: 'chip2', text: '+ ' + fmt(c.members.length - shown.length) + ' more' }) : null)));
      }
      // actions, each only when the oper may do it
      const ask = h('div', { class: 'confirm', hidden: true });
      const acts = h('div', { class: 'acts' });
      if (c.open && has('g')) {
        const note = h('input', { class: 'in', id: 'cn-' + c.name, type: 'text', maxlength: '400', autocomplete: 'off', placeholder: 'Everyone in the channel sees it' });
        const send = h('button', { type: 'button', class: 'btn main', text: 'Send notice' });
        const go = () => busy(send, async () => {
          if (!note.value.trim()) { note.classList.add('wrong'); note.focus(); return; }
          try { toast([(await api('POST', '/api/chan-notice', { name: c.name, text: note.value })).done.text]); note.value = ''; note.classList.remove('wrong'); }
          catch (e) { toastErr(e.message); }
        });
        send.addEventListener('click', go);
        note.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); go(); } });
        acts.append(h('div', { class: 'grow' }, h('label', { class: 'f', for: 'cn-' + c.name }, 'Notice to ' + c.name, note), send));
      }
      const end = h('div', { class: 'end' });
      const cancel = () => { ask.hidden = true; };
      if (c.open && has('s')) {
        const tb = h('button', { type: 'button', class: 'btn', text: 'Set topic' });
        tb.addEventListener('click', () => {
          const t = h('input', { class: 'in', type: 'text', value: c.topic || '', maxlength: '390', 'aria-label': 'New topic', placeholder: 'Empty clears the topic' });
          const save = h('button', { type: 'button', class: 'btn main', text: 'Save topic' });
          const no = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
          no.addEventListener('click', cancel);
          save.addEventListener('click', () => busy(save, async () => {
            try { toast([(await api('POST', '/api/chan-topic', { name: c.name, text: t.value })).done.text]); ask.hidden = true; load(); }
            catch (e) { toastErr(e.message); }
          }));
          ask.classList.remove('bad-ask');
          put(ask, h('label', { class: 'f' }, 'New topic for ' + c.name, t), save, no);
          ask.hidden = false; t.focus(); t.select();
        });
        end.append(tb);
      }
      if (c.registered && has('s')) {
        if (c.suspended) {
          const ub = h('button', { type: 'button', class: 'btn', text: 'Unsuspend' });
          ub.addEventListener('click', () => busy(ub, async () => {
            try { toast([(await api('POST', '/api/chan-unsuspend', { name: c.name })).done.text]); load(); } catch (e) { toastErr(e.message); }
          }));
          end.append(ub);
        } else {
          const sb = h('button', { type: 'button', class: 'btn bad', text: 'Suspend' });
          sb.addEventListener('click', () => {
            const reason = h('input', { class: 'in', type: 'text', maxlength: '200', placeholder: 'Reason (people see it)', 'aria-label': 'Reason' });
            const life = h('select', { class: 'in', 'aria-label': 'How long' },
              [['1h', '1 hour'], ['1d', '1 day'], ['7d', '7 days'], ['30d', '30 days'], ['0', 'Until unsuspended']].map(([v, t]) => h('option', { value: v, text: t })));
            life.value = '0';
            const go = h('button', { type: 'button', class: 'btn bad', text: 'Suspend ' + c.name });
            const no = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
            no.addEventListener('click', cancel);
            go.addEventListener('click', () => busy(go, async () => {
              try { toast([(await api('POST', '/api/chan-suspend', { name: c.name, reason: reason.value, life: life.value })).done.text]); open = null; load(); }
              catch (e) { toastErr(e.message); }
            }));
            put(ask, h('label', { class: 'f' }, 'Suspend ' + c.name + '? Everyone but opers is removed and can\'t come back.', reason), life, go, no);
            ask.hidden = false; reason.focus();
          });
          end.append(sb);
        }
      }
      if (end.childNodes.length) acts.append(end);
      if (acts.childNodes.length) parts.push(acts);
      parts.push(ask);
      put(box, h('div', { class: 'main-stack' }, parts));
    }

    search.addEventListener('input', () => { clearTimeout(typing); typing = setTimeout(() => { q = search.value.trim(); offset = 0; open = null; load(); }, 250); });
    for (const b of segs) b.addEventListener('click', () => {
      filter = b.dataset.f; offset = 0; open = null;
      segs.forEach(x => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
      load();
    });
    back.addEventListener('click', () => { offset = Math.max(0, offset - PAGE); open = null; load(); });
    next.addEventListener('click', () => { offset += PAGE; open = null; load(); });
    const onKey = e => { if (e.key === 'Escape' && open && !e.target.closest('input, select')) { open = null; draw(); } };
    document.addEventListener('keydown', onKey);
    const timer = setInterval(() => { if (!open && !document.hidden) load(); }, 15000);
    load();
    return { el, leave() { clearInterval(timer); document.removeEventListener('keydown', onKey); } };
  }

  // ---------- boot ----------
  api('GET', '/api/me').then(d => { me = d.oper; start(); }).catch(e => { if (!me) showSignin(e.message); });
})();
