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
  // tabIntoView scrolls a tab strip that is wider than the screen (phones)
  // so its active tab shows, once the strip is on the page.
  function tabIntoView(strip) {
    requestAnimationFrame(() => {
      const on = strip.querySelector('.on');
      if (!on || strip.scrollWidth <= strip.clientWidth) return;
      const s = strip.getBoundingClientRect(), b = on.getBoundingClientRect();
      if (b.left < s.left || b.right > s.right) strip.scrollLeft += b.left - s.left - (s.width - b.width) / 2;
    });
  }
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
  // What happened rows: ok (signed in), bad (refused), ch (a change).
  const evKind = w => /^could not/.test(w) ? 'bad' : /^signed in/.test(w) ? 'ok' : 'ch';
  function evIcon(w, k) {
    if (k !== 'ch') return k === 'ok' ? 'check' : /wrong password/.test(w) ? 'lock' : 'x';
    for (const [re, ic] of [[/check/, 'pulse'], [/backup/, 'archive'], [/^banned|ban/, 'bans'], [/oper entry/, 'opers'], [/link|connected|disconnected|split/, 'servers'],
      [/notice/, 'send'], [/topic/, 'message'], [/map/, 'map'], [/rehash/, 'refresh'], [/suspend/, 'lock']]) if (re.test(w)) return ic;
    return 'settings';
  }
  // evHex: the icon inside a flat-topped, round-cornered hexagon.
  function evHex(name) {
    const s = svgEl('svg', { class: 'ev-hex', viewBox: '0 0 30 26', 'aria-hidden': 'true' });
    s.append(svgEl('path', { d: 'M8.5 2.5h13l6 10.5-6 10.5h-13l-6-10.5z' }));
    return h('span', { class: 'ev-ic' }, s, icon(name));
  }
  const ms = v => (v < 1 ? 'under 1 ms' : Math.round(v) + ' ms');
  // day: '13 Nov', with the year when it isn't this year ('13 Nov 2036')
  const day = t => { const d = new Date(t); return d.toLocaleDateString('en-GB', d.getFullYear() === new Date().getFullYear() ? { day: 'numeric', month: 'short' } : { day: 'numeric', month: 'short', year: 'numeric' }); };
  const has = f => !!me && me.flags.includes(f);

  // ---------- talking to the panel ----------
  class ApiError extends Error {}
  // bg runs a timed refresh: the api() calls it starts say X-Hive-Auto, so
  // the panel doesn't count them as clicks (an unattended tab signs out).
  let bgDepth = 0;
  const bg = f => { bgDepth++; try { return f(); } finally { bgDepth--; } };
  async function api(method, path, body) {
    const opt = { method, credentials: 'same-origin', headers: { 'X-Hive': '1' } };
    if (bgDepth) opt.headers['X-Hive-Auto'] = '1';
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
    { id: 'home', label: 'Home', icon: 'home', you: true, make: homePage },
    { id: 'chat', label: 'Chat', icon: 'message', you: true, chat: true, make: chatPage },
    { id: 'mynick', label: 'My nick', icon: 'user', you: true, eyebrow: 'NickServ', make: myNickPage },
    { id: 'mychannels', label: 'My channels', icon: 'hash', you: true, eyebrow: 'ChanServ', make: myChannelsPage },
    { id: 'memos', label: 'Memos', icon: 'mail', you: true, eyebrow: 'MemoServ', make: memosPage },
    { id: 'chan', label: 'Channel', icon: 'hash', you: true, nav: false, parent: 'mychannels', make: chanPage },
    { id: 'overview', label: 'Overview', icon: 'overview', make: overviewPage },
    { id: 'people', label: 'People', icon: 'people', eyebrow: 'Live list', make: peoplePage },
    { id: 'channels', label: 'Channels', icon: 'hash', eyebrow: 'Live list', make: channelsPage },
    { id: 'bans', label: 'Bans', icon: 'bans', need: 'ks', eyebrow: 'Network bans', make: bansPage },
    { id: 'spamfilter', label: 'Spam filter', icon: 'filter', need: 'k', eyebrow: 'Spam filter', make: spamPage },
    { id: 'opers', label: 'Opers', icon: 'opers', need: 'a', eyebrow: 'Who may /oper', make: opersPage },
    { id: 'servers', label: 'Linked servers', icon: 'servers', need: 'c', eyebrow: 'Links', make: serversPage },
    { id: 'services', label: 'Services', icon: 'services', need: 'abghk', eyebrow: 'Built in', make: servicesPage },
    { id: 'settings', label: 'Settings', icon: 'settings', need: 'a', eyebrow: 'beeircd.conf', make: settingsPage },
  ];
  // A member (signed in with the NickServ password) sees only the YOU
  // pages; the server refuses the rest anyway.
  const allowed = p => (!p.chat || !!me && !!me.chat) && (p.you || !!me && !me.member && (!p.need || [...p.need].some(has)));

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
    document.querySelectorAll('.tmenu [data-theme-v]').forEach(x => x.setAttribute('aria-checked', String(x.dataset.themeV === t)));
    window.dispatchEvent(new Event('hive-theme'));
  }
  // Swatch colours: [ground, accent] of each theme, for the theme menu.
  const THEME_SWATCH = { 'bee': ['#111214', '#f2c94c'], 'catppuccin-latte': ['#eff1f5', '#df8e1d'], 'catppuccin-mocha': ['#1e1e2e', '#f9e2af'], 'one-dark-pro': ['#282c34', '#e5c07b'], 'dracula': ['#282a36', '#ff79c6'] };
  // themeMenu is the top bar's palette button with its menu of themes.
  function themeMenu() {
    const btn = h('button', { type: 'button', class: 'icon-btn tb-btn', 'aria-label': 'Theme', title: 'Theme', 'aria-haspopup': 'menu', 'aria-expanded': 'false' }, icon('palette'));
    const items = THEMES.map(([v, t]) => {
      const sw = h('span', { class: 'tsw' });
      sw.style.setProperty('--sw-a', THEME_SWATCH[v][0]);
      sw.style.setProperty('--sw-b', THEME_SWATCH[v][1]);
      const it = h('button', { type: 'button', role: 'menuitemradio', 'aria-checked': String(getTheme() === v), 'data-theme-v': v, class: 'titem' }, sw, h('span', { class: 'tname', text: t }), icon('check', 'tcheck'));
      it.addEventListener('click', () => { setTheme(v, true); close(true); });
      return it;
    });
    const menu = h('div', { class: 'tmenu', role: 'menu', 'aria-label': 'Theme', hidden: true }, h('div', { class: 'tmhead', text: 'Theme' }), items);
    const wrap = h('div', { class: 'tmwrap' }, btn, menu);
    function onDoc(e) { if (!wrap.contains(e.target)) close(false); }
    function open() {
      menu.hidden = false; btn.setAttribute('aria-expanded', 'true');
      (items.find(x => x.getAttribute('aria-checked') === 'true') || items[0]).focus();
      document.addEventListener('pointerdown', onDoc, true);
    }
    function close(focusBtn) {
      if (menu.hidden) return;
      menu.hidden = true; btn.setAttribute('aria-expanded', 'false');
      document.removeEventListener('pointerdown', onDoc, true);
      if (focusBtn) btn.focus();
    }
    btn.addEventListener('click', () => { if (menu.hidden) open(); else close(true); });
    menu.addEventListener('keydown', e => {
      const i = items.indexOf(document.activeElement);
      if (e.key === 'Escape') { e.preventDefault(); close(true); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); items[(i + 1) % items.length].focus(); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); items[(i - 1 + items.length) % items.length].focus(); }
      else if (e.key === 'Home') { e.preventDefault(); items[0].focus(); }
      else if (e.key === 'End') { e.preventDefault(); items[items.length - 1].focus(); }
      else if (e.key === 'Tab') close(false);
    });
    return wrap;
  }
  setTheme(getTheme());
  const brand = () => h('div', { class: 'brand' }, h('span', { class: 'mark' }, icon('hive')),
    h('span', { class: 'bname' }, 'Hive Panel', h('small', { text: 'NETWORK CONTROL' })));

  // ---------- sign in ----------
  function signedOut(msg) {
    stopLive();
    if (page && page.leave) page.leave(); // its timers must not ask again
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
    const intro = h('p', { class: 'intro', text: 'The same oper nick and oper password as /oper.' });
    const nickLbl = h('span', { text: 'Oper nick' }), pwLbl = h('span', { text: 'Oper password' });
    let members = false;
    api('GET', '/api/hello').then(d => {
      if (!d.members) return;
      members = true;
      intro.textContent = 'Your nick and NickServ password. Opers: your oper password opens the admin pages too.';
      nickLbl.textContent = 'Nick'; pwLbl.textContent = 'Password';
    }).catch(() => {});
    const form = h('form', { novalidate: true },
      brand(),
      h('h2', { text: 'Sign in to your server' }),
      intro,
      h('label', { class: 'f', for: 'nick' }, nickLbl, nick),
      h('label', { class: 'f pw', for: 'pw' }, pwLbl, pw, eye),
      err, go);
    form.addEventListener('submit', async e => {
      e.preventDefault();
      err.textContent = '';
      nick.classList.remove('wrong'); pw.classList.remove('wrong');
      if (!nick.value.trim()) { nick.classList.add('wrong'); err.textContent = members ? 'Type your nick.' : 'Type your oper nick.'; nick.focus(); return; }
      if (!pw.value) { pw.classList.add('wrong'); err.textContent = members ? 'Type your password.' : 'Type your oper password.'; pw.focus(); return; }
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
  let mainEl, navEl, crumbEl, pillEl, deco, chatHost;
  async function signOut() {
    await api('POST', '/api/logout').catch(() => {});
    signedOut('');
  }
  function start() {
    navEl = h('div', { class: 'nav' });
    const out = h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Sign out', title: 'Sign out' }, icon('signout'));
    out.addEventListener('click', signOut);
    const side = h('nav', { class: 'side', 'aria-label': 'Hive Panel' },
      brand(), navEl,
      h('div', { class: 'me' }, h('div', { class: 'av', text: me.account.slice(0, 1) }),
        h('div', { class: 'me-text' }, h('b', { text: me.account }), h('span', { class: 'lbl', text: me.member ? 'Member' : me.owner ? 'Owner' : (me.class || 'Oper') + ' · ' + (me.flags || 'no flags') })),
        out));
    crumbEl = h('div', { class: 'crumb' });
    pillEl = h('span', { class: 'pill', role: 'status' }, h('span', { class: 'dot' }),
      h('span', { class: 'pl', text: 'Uptime' }), h('span', { class: 'psep' }), h('span', { class: 'pv', text: '…' }));
    const mac = /Mac|iPhone|iPad/.test(navigator.platform || '');
    const jumpBtn = h('button', { type: 'button', class: 'icon-btn tb-btn', 'aria-label': 'Jump to a page or action', title: 'Jump to (' + (mac ? '⌘K' : 'Ctrl K') + ')' }, icon('search'));
    jumpBtn.addEventListener('click', openJump);
    const themeBtn = themeMenu();
    const refresh = h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Refresh this page', title: 'Refresh' }, icon('refresh'));
    refresh.addEventListener('click', () => {
      route();
      api('GET', '/api/status').then(d => { setOffline(false); gotStatus(d.status); }).catch(() => setOffline(true));
    });
    const topbar = h('header', { class: 'topbar' }, crumbEl,
      h('div', { class: 'tacts' }, jumpBtn, themeBtn, me.member ? null : pillEl, me.member ? null : refresh));
    mainEl = h('main', { class: 'content', id: 'main', tabindex: '-1' });
    // The Chat tab's BeeRoom lives beside the pages and is only hidden, so
    // it stays connected while you look at other pages.
    chatHost = h('div', { class: 'chat-host', hidden: true });
    put(app, h('div', { class: 'hp' }, side, h('div', { class: 'mainwrap' }, topbar, mainEl, chatHost)));
    if (deco) deco.disconnect();
    deco = new MutationObserver(() => decorate());
    deco.observe(mainEl, { childList: true, subtree: true });
    drawNav();
    route();
    // the unread count on Memos, wherever you start
    const memoCheck = () => bg(() => api('GET', '/api/my-memos')).then(d => {
      const n = d.memos.memos.filter(x => !x.read).length;
      if (n > unreadMemos && page && page.newMemos) page.newMemos();
      unreadMemos = n;
      drawCount();
    }).catch(() => {});
    memoCheck();
    clearInterval(memoTimer);
    memoTimer = setInterval(() => { if (me) memoCheck(); else clearInterval(memoTimer); }, 60000);
    if (!me.member) startLive(); // the live overview is for opers
  }
  function drawNav() {
    const here = p => !!page && (page.id === p.id || page.parent === p.id);
    const link = p => h('a', { href: '#/' + p.id, 'data-page': p.id, class: here(p) ? 'on' : null,
      'aria-current': here(p) ? 'page' : null }, icon(p.icon), p.label);
    const you = pages.filter(p => p.you && p.nav !== false && allowed(p)), server = pages.filter(p => !p.you && allowed(p));
    put(navEl, you.length ? h('div', { class: 'nlabel', text: 'You' }) : null, ...you.map(link),
      server.length ? h('div', { class: 'nlabel', text: 'Server' }) : null, ...server.map(link));
    drawCount();
  }
  function route() {
    if (!me) return;
    const home = me.member ? 'home' : 'overview'; // members start on Home, opers on the network Overview
    const id = (location.hash.match(/^#\/([a-z]+)/) || [])[1] || home;
    let p = pages.find(x => x.id === id && allowed(x));
    if (!p) { p = pages.find(x => x.id === home); history.replaceState(null, '', '#/' + home); }
    if (page && page.leave) page.leave();
    page = p.make();
    page.id = p.id;
    page.parent = p.parent;
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
    'Limits': 'gauge', 'Server actions': 'terminal', 'Server details': 'cpu', 'Backups': 'archive', 'Add a ban': 'plus', 'Add a forbid': 'plus', 'Network map': 'map',
    'Settings': 'settings', 'Members': 'people', 'Mail': 'mail', 'What the flags allow': 'opers', 'Change an oper': 'opers', 'Add an oper': 'opers',
    'This server, for the other side': 'lock', 'Server tree': 'servers', 'Link blocks': 'servers', 'Link block': 'servers', 'Not linked': 'servers',
    'Filters': 'list', 'Try a line': 'search', 'Add a filter': 'plus',
    'Waiting for an oper': 'user', 'Vhosts in use': 'user', 'Give a vhost': 'plus', 'Add a bot': 'plus', 'Logon news': 'message', 'Oper news': 'message', 'Random news': 'message',
    'Session exceptions': 'gauge', 'Busiest addresses': 'gauge', 'Add an exception': 'plus', 'DEFCON level': 'alert', 'Services ignore': 'eye-off', 'Add an ignore': 'plus',
  };
  const TILE_ICON = [[/^caught$/i, 'pulse'], [/^blocking$/i, 'bans'], [/^warn only$/i, 'eye'], [/^except$/i, 'check'], [/^filters$/i, 'filter'], [/oper/i, 'opers'], [/bot/i, 'services'], [/memo/i, 'message'], [/logged|nick/i, 'lock'], [/online|guest|people/i, 'people'],
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
      if (h2.querySelector('.sico') || h2.closest('.nm-card, td, .ov-hero')) continue;
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
      ...pages.filter(p => p.nav !== false && allowed(p)).map(p => ({ grp: 'Pages', label: p.label, icon: p.icon, run: () => { location.hash = '#/' + p.id; } })),
      has('a') ? { grp: 'Actions', label: 'Check the config', icon: 'check', run: () => quick('/api/check', checkLines, 'Check') } : null,
      has('a') ? { grp: 'Actions', label: 'Rehash', icon: 'refresh', run: () => quick('/api/rehash', rehashLines, 'Rehash') } : null,
      me.owner ? { grp: 'Actions', label: 'Back up now', icon: 'archive', run: () => quick('/api/backup', backupLines, 'Backup') } : null,
      me.member ? null : { grp: 'Actions', label: 'Arrange the Overview', icon: 'grip', run: () => { editNext = true; if (page && page.id === 'overview' && page.edit) { editNext = false; page.edit(true); } else location.hash = '#/overview'; } },
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
    if (!offline) showPill();
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
    poll = setInterval(() => bg(tick), 15000);
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
    showPill();
    if (page && page.offline) page.offline(on);
  }
  // The top bar badge: "● UPTIME | 5 h 12 min", or "● NOT ANSWERING" in red.
  function showPill() {
    if (!pillEl) return;
    const [lbl, sep, val] = pillEl.querySelectorAll('.pl, .psep, .pv');
    pillEl.className = offline ? 'pill bad' : 'pill';
    lbl.textContent = offline ? 'Not answering' : 'Uptime';
    const up = !offline && status ? since(status.started) : '';
    val.textContent = offline ? '' : status ? up : '…';
    sep.hidden = val.hidden = offline;
    pillEl.title = offline ? "The server isn't answering" : status ? 'Running for ' + up : '';
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
    { id: 'hero', label: 'Network status', wide: true },
    { id: 'stats', label: 'Numbers', wide: true },
    { id: 'room', label: 'This server', wide: true },
    { id: 'map', label: 'Network map', wide: true, need: 'c' },
    { id: 'where', label: 'Where people connect from', col: 'l', need: 'h' },
    { id: 'events', label: 'What happened', col: 'l' },
    { id: 'actions', label: 'Server actions', col: 'l', can: () => has('a') || has('g') || me.owner },
    { id: 'details', label: 'Server details', col: 'l' },
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
    // A block this browser hasn't seen yet goes where it sits in OV (before
    // the next block it knows), not at the end: Network status lands on top.
    ids.forEach((id, n) => {
      if (order.includes(id)) return;
      const next = ids.slice(n + 1).find(x => order.includes(x));
      order.splice(next ? order.indexOf(next) : order.length, 0, id);
    });
    const col = {};
    for (const b of OV) if (!b.wide) col[b.id] = saved.col && (saved.col[b.id] === 'l' || saved.col[b.id] === 'r') ? saved.col[b.id] : b.col;
    const hidden = Array.isArray(saved.hidden) ? saved.hidden.filter(id => ids.includes(id)) : [];
    for (const b of OV) if (b.off && !known.includes(b.id) && !hidden.includes(b.id)) hidden.push(b.id); // new extra blocks start hidden
    return { order, col, hidden };
  }
  function ovSave(L) {
    try { localStorage.setItem(ovKey(), JSON.stringify({ order: L.order, col: L.col, hidden: L.hidden, known: OV.map(b => b.id) })); } catch { /* private window */ }
  }
  // combSVG: the calm honeycomb for the uptime card, walls only. Cells start
  // in the top-right corner and fade out; a small echo sits bottom-left.
  // Walls use the card's text colour, so every theme gets a matching comb.
  function combSVG() {
    const NS = 'http://www.w3.org/2000/svg', W = 400, H = 250, r = 23, w = Math.sqrt(3) * r, hh = 1.5 * r;
    const hex = (x, y, rr) => { let d = ''; for (let i = 0; i < 6; i++) { const a = Math.PI / 180 * (60 * i - 90); d += (i ? 'L' : 'M') + (x + rr * Math.cos(a)).toFixed(1) + ' ' + (y + rr * Math.sin(a)).toFixed(1); } return d + 'Z'; };
    const s = document.createElementNS(NS, 'svg');
    s.setAttribute('class', 'comb'); s.setAttribute('aria-hidden', 'true');
    s.setAttribute('viewBox', `0 0 ${W} ${H}`); s.setAttribute('preserveAspectRatio', 'xMaxYMin slice');
    const add = (d, attrs) => { const p = document.createElementNS(NS, 'path'); p.setAttribute('d', d); for (const k in attrs) p.setAttribute(k, attrs[k]); s.append(p); };
    for (let row = -1; row < 5; row++) for (let col = -1; col < 5; col++) {
      const x = W - col * w - (row & 1 ? w / 2 : 0), y = row * hh;
      const a = Math.max(0, 1 - Math.hypot((W - x) / 140, y / 150));
      if (a <= 0.02) continue;
      add(hex(x, y, r - 2.2), { class: 'wall', 'fill-opacity': (a * 0.05).toFixed(3), 'stroke-opacity': (a * 0.34).toFixed(3) });
      add(hex(x, y + 0.9, r - 2.2), { class: 'shine', 'stroke-opacity': (a * 0.35).toFixed(3) });
    }
    for (const [dx, dy, a] of [[0, 0, 0.12], [w, 0, 0.07], [w / 2, -hh, 0.07]]) add(hex(26 + dx, H - 14 + dy, r - 2.2), { class: 'wall echo', 'stroke-opacity': a });
    return s;
  }
  // healthItems: the Health card's rows as data (lvl 0 fine, 1 look at
  // this, 2 needs you). The Overview header shows the worst one.
  function healthItems(s) {
    const out = [];
    if (s.cert) {
      const ce = s.cert, left = (new Date(ce.not_after) - Date.now()) / 86400000;
      if (ce.renew_failures > 0) out.push({ lvl: 2, label: 'Certificate', text: ce.renew_failures + ' renewals failed', cls: 'bad-t' });
      else if (left < 7) out.push({ lvl: left < 2 ? 2 : 1, label: 'Certificate', text: 'ends ' + day(ce.not_after) });
      else out.push({ lvl: 0, label: 'Certificate', text: ce.renew_at ? 'renews ' + day(ce.renew_at) : 'until ' + day(ce.not_after) });
    }
    out.push({ lvl: 0, label: 'Database', text: bytes(s.db_bytes || 0) });
    if (s.last_backup) out.push({ lvl: (Date.now() - new Date(s.last_backup)) / 86400000 > 2 ? 1 : 0, label: 'Last backup', text: when(s.last_backup) });
    else out.push({ lvl: 1, label: 'Last backup', text: 'none yet' });
    if (s.update) out.push({ lvl: 1, label: 'Update', text: s.update.version + ' is out' });
    else out.push({ lvl: 0, label: 'Update', text: 'up to date' });
    const sf = s.safety || {}, guards = (sf.throttled || 0) + (sf.zlined || 0) + (sf.dnsbl_hits || 0);
    // One calm line on a quiet day; the three guards each get a row once one caught something.
    if (!guards) out.push({ lvl: 0, label: 'Guards today', text: 'quiet' });
    else {
      const n = (v, one, many) => v ? plural(v, one, many) : 'none';
      out.push({ lvl: 0, label: 'Slowed down today', text: n(sf.throttled, 'connection', 'connections') },
        { lvl: 0, label: 'Z-lined today', text: n(sf.zlined, 'blocked', 'blocked') },
        { lvl: 0, label: 'Blocklist today', text: n(sf.dnsbl_hits, 'blocked', 'blocked') });
    }
    return out;
  }
  // plainIRC drops IRC bold, colour, italics and the like from a topic.
  const plainIRC = t => (t || '').replace(/\x03(\d{1,2}(,\d{1,2})?)?|\x04([0-9a-fA-F]{6}(,[0-9a-fA-F]{6})?)?|[\x02\x0f\x11\x16\x1d\x1e\x1f]/g, '').trim();
  // greeting: good morning / afternoon / evening, by the viewer's clock.
  function greeting() {
    const hr = new Date().getHours();
    return hr < 5 ? 'Good evening' : hr < 12 ? 'Good morning' : hr < 18 ? 'Good afternoon' : 'Good evening';
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
    const sub = h('span', { class: 'sub', text: ' ', role: 'status' }); // the Health line; says it when the server stops answering
    const editBtn = h('button', { type: 'button', class: 'btn' }, icon('grip'), 'Arrange');
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
      arrange, area, foot);

    const tile = (ic, label) => {
      const n = h('span', { class: 'num', text: '…' }), l = h('span', { class: 'foot-n', text: ' ' });
      return { el: h('section', { class: 'card tight', 'data-deco': '1' }, h('div', { class: 'slab' }, h('span', { text: label }), h('span', { class: 'sico' }, icon(ic))), n, l), n, l };
    };
    const rowOf = (dotCls, label, end, endCls, mono) => h('div', { class: 'row' },
      h('span', { class: mono ? 'mono' : null }, dotCls !== null ? h('span', { class: 'dot ' + dotCls }) : null, label),
      h('span', { class: 'end ' + (endCls || ''), text: end }));

    // ---- the blocks ----
    const make = {
      hero() {
        // Network status on the left; on the right the Spotlight card with
        // the calm comb (picked 13 Nov): one story at a time, a new one every
        // 10 s, never a number shown somewhere else (uptime is in the top
        // bar badge).
        const eye = h('div', { class: 'eyebrow', text: 'Network status' });
        const head = h('h2', { text: '…' });
        const say = h('p', { class: 'hs-line', text: ' ' });
        const fact = label => { const b = h('b', { text: '…' }); return { el: h('div', {}, h('span', { text: label }), b), b }; };
        const fV = fact('Version'), fB = fact('Last backup'), fC = fact('Certificate');
        const kind = h('span', { class: 'up-k sp-k' });
        const big = h('span', { class: 'sp-big' }), pill = h('span', { class: 'up-u' });
        const line = h('span', { class: 'sp-line' });
        const main = h('button', { type: 'button', class: 'sp-main' }, h('span', { class: 'sp-row' }, big, pill), line);
        const dots = h('div', { class: 'sp-dots', role: 'group', 'aria-label': 'Stories' });
        const card = h('div', { class: 'up-card sp', role: 'group', 'aria-roledescription': 'carousel', 'aria-label': 'Spotlight' }, combSVG(),
          kind, main, dots, h('span', { class: 'up-foot', text: 'BeeIRCd / Hive' }));
        const el = h('section', { class: 'card ov-hero' },
          h('div', { class: 'hs' }, eye, head, say, h('div', { class: 'hs-facts' }, fV.el, fB.el, fC.el)), card);
        let last = null, off = false, stories = [], at = 0, held = false;
        const still = () => window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
        const clock = t => new Date(t).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
        function storiesOf(s) {
          const sp = s.spotlight || {}, out = [];
          if (sp.room) out.push({ key: 'room ' + sp.room.name, kind: 'Busiest room right now', big: sp.room.name, pill: sp.room.talkers + ' talking',
            line: plainIRC(sp.room.topic) ? '“' + plainIRC(sp.room.topic) + '”' : plural(sp.room.members, 'person', 'people') + ' in the room',
            go: () => { location.hash = '#/channels?q=' + encodeURIComponent(sp.room.name); }, title: 'Open ' + sp.room.name });
          if (sp.newest) out.push({ key: 'new ' + sp.newest.name, kind: 'Newest member', big: sp.newest.name, pill: 'welcome', line: 'Registered ' + when(sp.newest.since) });
          if (sp.day_peak) out.push({ key: 'peak', kind: 'Busiest moment today', big: fmt(sp.day_peak.people), pill: sp.day_peak.people === 1 ? 'person' : 'people', line: 'at once, at ' + clock(sp.day_peak.at) });
          for (const a of sp.anniversaries || []) out.push({ key: 'year ' + a.name, kind: 'Anniversary', big: a.name, pill: plural(a.years, 'year', 'years'), line: 'Registered on this day in ' + new Date(a.since).getFullYear() });
          if (!out.length) out.push({ key: 'quiet', kind: 'Spotlight', big: 'Quiet', pill: '', line: 'Nobody has talked in the last 5 minutes' });
          return out;
        }
        function show() {
          const x = stories[at];
          kind.textContent = x.kind; big.textContent = x.big; pill.textContent = x.pill; pill.hidden = !x.pill; line.textContent = x.line;
          main.disabled = !x.go || off; main.title = x.go && !off ? x.title : '';
          main.onclick = x.go || null;
          [...dots.children].forEach((d, i) => d.setAttribute('aria-current', String(i === at)));
        }
        function draw() {
          const s = last;
          if (!s) return;
          const name = s.network || s.server;
          el.classList.toggle('down', off);
          head.textContent = off ? name : name + ' is up';
          const ls = s.links || [], up = ls.filter(l => l.state === 'up').length;
          const net = ls.length === 0 ? 'This server runs alone' : up === ls.length ? plural(ls.length + 1, 'server', 'servers') + ' linked' : (up + 1) + ' of ' + (ls.length + 1) + ' servers linked';
          const svc = !s.services_on ? 'services run on the hub' : (s.services || []).length ? 'all ' + plural(s.services.length, 'service', 'services') + ' up' : 'no services up';
          say.textContent = (off ? 'Last known: ' + net.charAt(0).toLowerCase() + net.slice(1) : net) + ', ' + svc + '.';
          fV.b.textContent = s.version;
          fB.b.textContent = s.last_backup ? when(s.last_backup) : 'none yet';
          fC.b.textContent = s.cert ? (s.cert.source === 'self-signed' ? 'self-signed · ' : '') + 'until ' + day(s.cert.not_after) : 'none';
          // Keep the story on screen if it is still there.
          const cur = stories[at] && stories[at].key;
          stories = storiesOf(s);
          at = Math.max(0, stories.findIndex(x => x.key === cur));
          if (dots.children.length !== stories.length) {
            put(dots, stories.map((x, i) => {
              const d = h('button', { type: 'button', class: 'sp-dot', 'aria-label': 'Story ' + (i + 1) + ' of ' + stories.length });
              d.addEventListener('click', () => { at = i; show(); });
              return d;
            }));
          }
          dots.hidden = stories.length < 2;
          show();
        }
        // Next story every 10 s; held while the pointer or focus is on the
        // card, never moving for people who asked for less motion.
        card.addEventListener('pointerenter', () => { held = true; });
        card.addEventListener('pointerleave', () => { held = false; });
        card.addEventListener('focusin', () => { held = true; });
        card.addEventListener('focusout', () => { held = false; });
        const timer = setInterval(() => {
          if (held || off || document.hidden || still() || stories.length < 2 || !card.isConnected) return;
          at = (at + 1) % stories.length; show();
        }, 10000);
        return { el, float: true,
          status(s) { last = s; draw(); },
          offline(on) { off = on; draw(); },
          leave() { clearInterval(timer); } };
      },
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
        const c = ovCard('gauge', 'This server', 'How this server is doing, and what you may do here');
        const desc = c.card.querySelector('.cdesc');
        // A bar meter: label, value, a track and a printed scale.
        const bar = label => {
          const v = h('b', { text: '…' }), fill = h('span', { class: 'ts-fill' }), track = h('div', { class: 'ts-track' }, fill), scale = h('div', { class: 'ts-scale' });
          return { el: h('div', {}, h('div', { class: 'ts-top' }, h('span', { text: label }), v), track, scale), v, fill, track, scale };
        };
        const pct = (n, of) => Math.max(0, Math.min(100, of > 0 ? n / of * 100 : 0)) + '%';
        const gb = mb => mb >= 1024 ? (Math.round(mb / 1024 * 10) / 10) + ' GB' : Math.round(mb) + ' MB';
        const bP = bar('People online'), bR = bar('Reply time'), bM = bar('Memory');
        const mark = h('span', { class: 'ts-mark' }); mark.style.left = '20%'; bR.track.append(mark);
        put(bR.scale, h('span', { text: '0' }), h('span', { class: 'ts-at2', text: '2 ms smooth' }), h('span', { text: '10 ms' }));
        const mini = h('div', { class: 'ts-mini' });
        const left = h('div', { class: 'ts-l' }, h('div', { class: 'ts-bars' }, bP.el, bR.el, bM.el), mini);
        // You: the signed-in oper and what each of their flags allows.
        const held = FLAGS.filter(([f]) => has(f)), missing = FLAGS.filter(([f]) => !has(f));
        const via = h('span', { class: 'ts-via', text: me.ip ? 'signed in from ' + me.ip : '' });
        const right = h('div', { class: 'ts-r' },
          h('div', { class: 'ts-who' }, h('b', { text: 'You · ' + me.account }), h('span', { class: 'ts-class', text: me.owner ? 'owner' : (me.class || 'oper') }), via),
          held.length ? h('div', { class: 'ts-tiles' }, held.map(([f, name, what]) => h('div', { class: 'ts-tile' },
            h('span', { class: 'fb f-' + f, text: f, 'aria-hidden': 'true' }), h('b', { text: name.charAt(0).toUpperCase() + name.slice(1) }), h('span', { class: 'ts-m', text: what }))))
            : h('p', { class: 'ts-none', text: 'No oper flags: you can look, not change.' }),
          missing.length ? h('div', { class: 'ts-not', 'aria-label': 'not yours: ' + missing.map(([f]) => f).join('') }, 'Not yours',
            missing.map(([f]) => h('span', { class: 'fb sm no', text: f, 'aria-hidden': 'true' }))) : null);
        c.card.append(h('div', { class: 'ts' }, left, right));
        return { el: c.card, tail: c.tail,
          status(s) {
            if (s.server) desc.textContent = 'How ' + s.server + ' is doing, and what you may do here';
            const peak = Math.max(s.peak || 0, s.online || 0);
            put(bP.v, fmt(s.online), h('small', { text: ' / ' + fmt(peak) + ' peak' }));
            bP.fill.style.width = pct(s.online, peak);
            put(bP.scale, h('span', { text: '0' }), h('span', { text: 'peak since start ' + fmt(peak) }));
            const lag = s.lag_avg_ms || 0;
            put(bR.v, lag < 0.1 ? '0.1' : String(Math.round(lag * 10) / 10), h('small', { text: lag <= 2 ? ' ms · smooth' : lag > 100 ? ' ms · slow' : ' ms' }));
            bR.fill.style.width = pct(Math.max(lag, 0.1), 10);
            bR.fill.className = 'ts-fill' + (lag <= 2 ? ' ok' : lag > 100 ? ' bad' : '');
            const total = s.machine_mb || 0;
            put(bM.v, String(Math.round(s.memory_mb)), h('small', { text: total ? ' MB of ' + gb(total) : ' MB' }));
            bM.track.hidden = bM.scale.hidden = !total;
            if (total) { bM.fill.style.width = pct(s.memory_mb, total); put(bM.scale, h('span', { text: '0' }), h('span', { text: gb(total) })); }
            const n = (label, v) => [label, h('b', { text: fmt(v) })];
            if (s.services_on) put(mini, h('span', {}, ...n('Registered nicks', s.nicks)), h('span', {}, ...n('channels', s.reg_channels)), h('span', {}, ...n('memos kept', s.memos)));
            else put(mini, h('span', { text: 'Registered nicks, channels and memos are kept on the hub' }));
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
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) bg(load); }, 10000); },
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
            // Fold the same event from the same person and address in the
            // same minute into one row with ×n; group the rows by day.
            const rows = [];
            for (const e of lst) {
              const last = rows[rows.length - 1], d = new Date(e.at);
              const minute = d.toDateString() + pad(d.getHours()) + pad(d.getMinutes());
              if (last && last.minute === minute && last.e.who === e.who && last.e.what === e.what && last.e.ip === e.ip) { last.n++; continue; }
              if (rows.length === 10) break;
              rows.push({ e, n: 1, minute, fresh: before && !seen.has(e.at + e.who + e.what) });
            }
            const now = new Date(), yest = new Date(now); yest.setDate(now.getDate() - 1);
            const dayName = d => d.toDateString() === now.toDateString() ? 'Today'
              : (d.toDateString() === yest.toDateString() ? 'Yesterday · ' : '') + day(d);
            const counts = { ok: 0, bad: 0, ch: 0 };
            const out = [];
            let lastDay = '';
            for (const r of rows) {
              const d = new Date(r.e.at), dn = dayName(d), k = evKind(r.e.what);
              counts[k] += r.n;
              if (dn !== lastDay) { out.push(h('div', { class: 'ev-day', text: dn })); lastDay = dn; }
              out.push(h('div', { class: 'ev ' + k + (r.fresh ? ' new' : '') },
                evHex(evIcon(r.e.what, k)),
                h('span', { class: 'w' }, h('b', { text: r.e.who }), ' ' + r.e.what, r.n > 1 ? h('span', { class: 'ev-n', text: '×' + r.n }) : null),
                h('span', { class: 't' }, pad(d.getHours()) + ':' + pad(d.getMinutes()), r.e.ip ? h('span', { class: 'ip', text: r.e.ip }) : null)));
            }
            const sum = [['ok', 'sign-in'], ['bad', 'refused'], ['ch', 'change']].filter(([k]) => counts[k])
              .map(([k, w]) => h('span', { class: 'ev-sum ' + k }, counts[k] + ' ' + (k === 'bad' || counts[k] === 1 ? w : w + 's')));
            put(list, ...out, h('div', { class: 'ev-foot' }, h('span', { class: 'ev-sums' }, sum), h('span', { text: 'newest ' + rows.reduce((a, r) => a + r.n, 0) + ' of ' + lst.length })));
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
          has('a') ? btn('refresh', 'Rehash', '/api/rehash', rehashLines) : null,
          has('g') ? globalBox() : null, res));
        return { el: c.card, tail: c.tail };
        // Notice to everyone (OperServ GLOBAL): asks once before sending.
        function globalBox() {
          const note = h('input', { class: 'in', id: 'ov-global', type: 'text', maxlength: '400', autocomplete: 'off', placeholder: 'Everyone on the network sees it' });
          const send = h('button', { type: 'button', class: 'btn', text: 'Send…' });
          const sure = h('div', { class: 'confirm', hidden: true });
          const open = () => {
            if (!note.value.trim()) { note.classList.add('wrong'); note.focus(); return; }
            note.classList.remove('wrong');
            const n = status && status.online ? plural(status.online, 'person', 'people') : 'everyone';
            const go = h('button', { type: 'button', class: 'btn main', text: 'Send to ' + n });
            const cancel = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
            cancel.addEventListener('click', () => { sure.hidden = true; note.focus(); });
            go.addEventListener('click', () => busy(go, async () => {
              try {
                const d = await api('POST', '/api/global', { text: note.value });
                put(res, line('ok', d.done.text)); res.hidden = false;
                note.value = ''; sure.hidden = true;
              } catch (e) { put(res, line('bad', e.message)); res.hidden = false; }
            }));
            put(sure, h('span', { class: 'f', text: 'Send this notice to ' + n + ' on the network, from OperServ?' }), go, cancel);
            sure.hidden = false; go.focus();
          };
          send.addEventListener('click', open);
          note.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); open(); } });
          return h('div', { class: 'ov-global' },
            h('div', { class: 'acts' }, h('div', { class: 'grow' }, h('label', { class: 'f', for: 'ov-global' }, 'Notice to everyone', note), send)), sure);
        }
      },
      details() {
        // What this server runs on. Version, certificate date, memory and
        // uptime already show elsewhere on the page, so they are not here.
        const c = ovCard('cpu', 'Server details', 'What this server runs on');
        const rows = h('div');
        c.card.append(rows);
        const OSN = { linux: 'Linux', windows: 'Windows', darwin: 'macOS', freebsd: 'FreeBSD', openbsd: 'OpenBSD', netbsd: 'NetBSD' };
        const row = (label, end) => h('div', { class: 'row' }, h('span', { text: label }), h('span', { class: 'end mono', text: end }));
        return { el: c.card, tail: c.tail,
          status(s) {
            const seen = new Set(), ports = [];
            for (const p of s.ports || []) {
              const n = (p.address || '').split(':').pop(), tag = n + (p.tls ? ' TLS' : '') + (p.proxy ? ' proxy' : '');
              if (n && !seen.has(tag)) { seen.add(tag); ports.push(tag); }
            }
            if (s.link_port) ports.push(s.link_port + ' link');
            const st = new Date(s.started);
            put(rows,
              ports.length ? row('Ports', ports.join(' · ')) : null,
              s.os ? row('System', [OSN[s.os] || s.os, s.arch, s.cpus ? plural(s.cpus, 'core', 'cores') : ''].filter(Boolean).join(' · ')) : null,
              s.cert && (s.cert.names || []).length ? row('Certificate for', s.cert.names.join(', ')) : null,
              s.started ? row('Started', day(st) + ' ' + pad(st.getHours()) + ':' + pad(st.getMinutes())) : null,
              row('Channel history', s.history_channels ? plural(s.history_channels, 'channel', 'channels') + ' · ' + bytes(s.history_bytes || 0) : 'none yet'));
          } };
      },
      health() {
        const word = h('span', { class: 'okword' });
        const c = ovCard('pulse', 'Health', 'Things to look at now and then', word);
        const rows = h('div');
        c.card.append(rows);
        return { el: c.card, tail: c.tail,
          status(s) {
            const items = healthItems(s), worst = Math.max(0, ...items.map(x => x.lvl));
            put(rows, items.map(x => rowOf(['', 'warn', 'bad'][x.lvl], x.label, x.text, x.cls)));
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
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) bg(load); }, 30000); },
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
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) bg(load); }, 30000); },
          leave() { clearInterval(timer); } };
      },
      where() {
        // People by country or network (GeoIP); hidden when GeoIP is off.
        const c = ovCard('globe', 'Where people connect from', 'Country and network of everyone online');
        let kind = 'countries', last = null, timer = 0;
        const segs = [['countries', 'Countries'], ['networks', 'Networks']].map(([v, label]) =>
          h('button', { type: 'button', 'aria-pressed': v === kind ? 'true' : 'false', text: label }));
        const rows = h('div', { class: 'wh-bars' }), credit = h('div', { class: 'wh-credit lbl' });
        c.card.append(h('div', { class: 'seg wh-seg', role: 'group', 'aria-label': 'Count by' }, segs), rows, credit);
        segs.forEach((b, i) => b.addEventListener('click', () => {
          kind = i ? 'networks' : 'countries';
          segs.forEach(x => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
          draw();
        }));
        const TOP = 7;
        function bar(code, name, n, max, total, cls, go) {
          const pct = total ? Math.round(n / total * 100) : 0;
          const fill = h('i', { class: cls || null });
          fill.style.width = Math.max(1.5, n / max * 100) + '%';
          const label = plural(n, 'person', 'people') + ' · ' + pct + '%' + (go ? ' · open in People' : '');
          const row = h(go ? 'button' : 'div', { class: 'wh-row' + (go ? '' : ' still'), title: label, 'aria-label': name + ': ' + label, type: go ? 'button' : null },
            kind === 'countries' ? ccChip(code) : h('span', { class: 'asn' + (code ? '' : ' none'), text: code || '—' }),
            h('span', { class: 'wh-name', text: name }), h('span', { class: 'wh-track' }, fill), h('b', { text: fmt(n) }));
          if (go) row.addEventListener('click', () => { location.hash = '#/people?q=' + encodeURIComponent(go); });
          return row;
        }
        function draw() {
          const d = last;
          if (!d) return;
          const list = kind === 'countries' ? d.countries : d.networks;
          const top = list.slice(0, TOP), rest = list.slice(TOP);
          const restN = rest.reduce((a, p) => a + p.people, 0) + (kind === 'countries' ? d.more_countries || 0 : d.more_networks || 0);
          const none = kind === 'countries' ? d.no_country : d.no_network;
          const max = Math.max(1, ...top.map(p => p.people), restN, none);
          const out = top.map(p => bar(p.code, p.name || p.code, p.people, max, d.total, null, p.code));
          if (restN) out.push(bar(kind === 'countries' ? '+' + rest.length : '+' + rest.length, rest.length + (rest.length === 1 ? (kind === 'countries' ? ' other country' : ' other network') : (kind === 'countries' ? ' other countries' : ' other networks')) + ((kind === 'countries' ? d.more_countries : d.more_networks) ? ' and more' : ''), restN, max, d.total, 'o'));
          if (none) out.push(bar('', 'Not known', none, max, d.total, 'u'));
          put(rows, out.length ? out : h('p', { class: 'empty', text: 'Nobody is online.' }));
          const when = d.built ? new Date(d.built).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' }) : '';
          credit.textContent = (/^DBIP/i.test(d.source) ? 'IP data by DB-IP' : 'From ' + (d.source || 'GeoIP')) + (when ? ' · updated ' + when : '');
        }
        async function load() {
          if (!c.card.isConnected) return;
          const blk = c.card.closest('.blk');
          try {
            last = (await api('GET', '/api/where')).where;
            if (blk) blk.hidden = false;
            draw();
          } catch (e) {
            if (/GeoIP is off/.test(e.message)) { if (blk) blk.hidden = true; return; }
            put(rows, h('p', { class: 'empty', text: e.message }));
          }
        }
        return { el: c.card, tail: c.tail,
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) bg(load); }, 30000); },
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
          show() { setTimeout(load, 0); clearInterval(timer); timer = setInterval(() => { if (!document.hidden) bg(load); }, 30000); },
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
        if (x.offline && offline) x.offline(true);
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

    // Header (13 Nov, option A): today's date, a greeting, and one line
    // from Health. Network, servers, version and uptime live elsewhere.
    function headLine(s, off) {
      eyebrow.textContent = new Date().toLocaleDateString('en-GB', { weekday: 'long', day: 'numeric', month: 'long' });
      title.textContent = greeting() + ', ' + me.account;
      let lvl = 0, say = 'All quiet in the hive', more = 'nothing needs you right now';
      // The top bar badge already says NOT ANSWERING; this line says what it means here.
      if (off) { lvl = 2; say = 'Showing the last numbers'; more = 'until the server answers again'; }
      else if (s) {
        const items = healthItems(s), worst = Math.max(0, ...items.map(x => x.lvl));
        if (worst) { const x = items.find(i => i.lvl === worst); lvl = worst; say = x.label + ': ' + x.text; more = 'see Health'; }
      }
      const toHealth = () => { const c = B.health && B.health.el; if (c && c.isConnected) { c.scrollIntoView({ behavior: 'smooth', block: 'center' }); } };
      const btn = h('button', { type: 'button', class: 'hl hl-' + lvl, title: lvl ? 'Show Health' : '' }, h('span', { class: 'hl-dot' }), h('span', { class: 'hl-say', text: say }), h('span', { class: 'hl-more', text: '· ' + more }));
      btn.disabled = !lvl || off;
      btn.addEventListener('click', toHealth);
      put(sub, btn);
    }
    function onStatus(s) {
      headLine(s, false);
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
      offline(on) { headLine(status, on); for (const id of visible()) if (B[id].offline) B[id].offline(on); },
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
  // Auto-join: [server] auto_join, auto_join_skip, oper_join and
  // oper_join_flag as rows of chips. A changed row gets the yellow bar;
  // Save sends only the settings that changed.
  const FLAG_WORDS = { a: 'admin', b: 'bots', c: 'links', g: 'global', h: 'hosts', k: 'kill', s: 'services' };
  function autoJoinCard() {
    const rowsEl = h('div', { class: 'aj-rows' });
    const count = h('span');
    const undoBtn = h('button', { type: 'button', class: 'btn ghost', text: 'Undo' });
    const saveBtn = h('button', { type: 'button', class: 'btn main', text: 'Save' });
    const bar = h('div', { class: 'unsaved', hidden: true }, count, h('div', { class: 'row-btns' }, undoBtn, ' ', saveBtn));
    const flagSel = h('select', { class: 'in aj-flag', 'aria-label': 'Add a row for an oper flag' });
    const flagAdd = h('div', { class: 'aj-more' }, flagSel);
    const el = h('section', { class: 'card', hidden: true }, h('h2', { text: 'Auto-join' }),
      h('p', { class: 'note', text: 'Channels people are put in. It applies from the next connect or /oper; people already on stay where they are.' }),
      rowsEl, flagAdd, bar);
    let set = {}, rows = [], groups = [], flags = [];
    const list = v => (!v || v === 'off') ? [] : v.split(',').filter(Boolean);
    const okChan = v => /^#[^\s,\x00-\x1f]{1,49}$/.test(v);
    const same = (a, b) => a.length === b.length && a.every((x, i) => x === b[i]);

    function flagValue() {
      const parts = flags.map(f => rows.find(r => r.flag === f)).filter(r => r && r.items.length).map(r => r.flag + ':' + r.items.join(','));
      return parts.length ? parts.join(' ') : 'off';
    }
    function dirtyNames() {
      const out = [];
      for (const n of ['auto-join', 'auto-join-skip', 'oper-join']) {
        const r = rows.find(x => x.name === n);
        if (r && !same(r.items, r.orig)) out.push(n);
      }
      if (set['oper-join-flag'] && flagValue() !== set['oper-join-flag'].value) out.push('oper-join-flag');
      return out;
    }
    function refresh() {
      const n = rows.filter(r => !same(r.items, r.orig)).length;
      bar.hidden = n === 0;
      count.textContent = n === 1 ? '1 unsaved change' : n + ' unsaved changes';
      const used = rows.filter(r => r.flag).map(r => r.flag);
      const free = flags.filter(f => !used.includes(f));
      put(flagSel, h('option', { value: '', text: '+ Opers with flag…' }), ...free.map(f => h('option', { value: f, text: f + ' · ' + (FLAG_WORDS[f] || f) })));
      flagAdd.hidden = !set['oper-join-flag'] || free.length === 0;
    }
    function row(r) {
      const chips = h('div', { class: 'chips aj-chips' });
      const hint = h('span', { class: 'hint' });
      let adder;
      if (r.kind === 'groups') {
        adder = h('select', { class: 'in aj-in', 'aria-label': 'Add a group to ' + r.label });
        adder.addEventListener('change', () => { if (adder.value) { r.items.push(adder.value); draw(); } });
      } else {
        adder = h('input', { class: 'in aj-in', type: 'text', placeholder: '#channel', autocomplete: 'off', spellcheck: 'false', 'aria-label': 'Add a channel to ' + r.label });
        const add = () => {
          const v = adder.value.trim();
          if (!v) return;
          const names = v.split(/[\s,]+/).filter(Boolean);
          const bad = names.find(x => !okChan(x));
          if (bad) { hint.textContent = bad + ' is not a channel name. Channels start with #.'; hint.classList.add('wrong'); return; }
          for (const x of names) if (!r.items.some(y => y.toLowerCase() === x.toLowerCase())) r.items.push(x);
          adder.value = '';
          draw();
          adder.focus();
        };
        adder.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); add(); } });
        adder.addEventListener('blur', add);
      }
      function draw() {
        hint.classList.remove('wrong');
        put(chips, ...r.items.map((x, i) => h('span', { class: 'chip2 aj-chip' }, x,
          h('button', { type: 'button', class: 'aj-x', 'aria-label': 'Remove ' + x, title: 'Remove', text: '×', onclick: () => { r.items.splice(i, 1); draw(); } }))));
        if (!r.items.length) chips.append(h('span', { class: 'lbl', text: r.empty }));
        if (r.kind === 'groups') {
          const free = groups.filter(g => !r.items.some(y => y.toLowerCase() === g.toLowerCase()));
          put(adder, h('option', { value: '', text: '+ group' }), ...free.map(g => h('option', { value: g, text: g })));
          adder.hidden = free.length === 0;
        }
        chips.append(adder);
        hint.textContent = !same(r.items, r.orig) ? 'was ' + (r.orig.join(', ') || 'none') : r.hint;
        r.el.classList.toggle('changed', !same(r.items, r.orig));
        refresh();
      }
      r.el = h('div', { class: 'aj-row' }, h('div', { class: 'aj-lbl' }, h('b', { text: r.label }), hint), chips);
      r.draw = draw;
      return r;
    }
    function build() {
      rows = [];
      const mk = (name, label, kind, empty, hintText, items) => rows.push(row({ name, label, kind, empty, hint: hintText, items: items.slice(), orig: items.slice() }));
      if (set['auto-join']) mk('auto-join', 'Everyone on connect', 'channels', 'No channels', 'a normal join: bans and keys still apply', list(set['auto-join'].value));
      if (set['auto-join-skip']) mk('auto-join-skip', 'Except these groups', 'groups', 'Nobody skips it', 'security groups that are not auto-joined', list(set['auto-join-skip'].value));
      if (set['oper-join']) mk('oper-join', 'Every oper at /oper', 'channels', 'No channels', 'joined even past bans and keys', list(set['oper-join'].value));
      if (set['oper-join-flag']) {
        const v = set['oper-join-flag'].value;
        for (const part of (v && v !== 'off') ? v.split(' ') : []) {
          const [f, chans] = part.split(':');
          addFlag(f, list(chans));
        }
      }
      rows.forEach(r => r.draw());
      put(rowsEl, ...rows.map(r => r.el));
      refresh();
    }
    function addFlag(f, items) {
      const r = row({ name: 'oper-join-flag', flag: f, label: 'Opers with flag ' + f, kind: 'channels', empty: 'No channels', hint: FLAG_WORDS[f] ? 'flag ' + f + ': ' + FLAG_WORDS[f] : '', items: items.slice(), orig: items.slice() });
      rows.push(r);
      return r;
    }
    flagSel.addEventListener('change', () => {
      const f = flagSel.value;
      if (!f) return;
      const r = addFlag(f, []);
      r.draw();
      rowsEl.append(r.el);
      r.el.querySelector('input').focus();
    });
    undoBtn.addEventListener('click', build);
    saveBtn.addEventListener('click', () => busy(saveBtn, async () => {
      const value = n => n === 'oper-join-flag' ? flagValue() : (rows.find(r => r.name === n).items.join(',') || 'off');
      const done = [];
      for (const n of dirtyNames()) {
        try {
          const d = await api('POST', '/api/set', { name: n, value: value(n) });
          done.push(d.changed);
          set[n].value = d.changed.new;
        } catch (e) {
          toastErr(n + ' was not saved: ' + e.message);
        }
      }
      build();
      if (!done.length) return;
      toast(['Auto-join saved · applies from the next connect or /oper'], async () => {
        for (const c of done.slice().reverse()) if (c.id) await api('POST', '/api/undo', { id: c.id });
        await load();
        toast(['Put back as it was']);
      });
    }));
    let load = async () => {};
    return {
      el,
      fill(byName, reload) {
        load = reload;
        set = {};
        for (const n of ['auto-join', 'auto-join-skip', 'oper-join', 'oper-join-flag']) if (byName[n]) set[n] = Object.assign({}, byName[n]);
        el.hidden = !Object.keys(set).length;
        groups = (set['auto-join-skip'] && set['auto-join-skip'].choices) || [];
        flags = (set['oper-join-flag'] && set['oper-join-flag'].choices) || [];
        build();
      },
    };
  }

  // Login prompt ([authprompt]) and spam report places ([[spamreport]]).
  // A DroneBL key is written here but never shown again, only "key set".
  function loginSpamCard() {
    const AP = [
      { name: 'auth-prompt-text', label: 'First notice', hint: 'then: Turn on SASL in your client, or type now: /AUTH nick:password' },
      { name: 'auth-prompt-fail', label: 'After a wrong password', hint: 'they may try again' },
      { name: 'auth-prompt-time', label: 'Time to log in', hint: '10s to 10m, then the ban message' },
    ];
    const onBox = h('input', { type: 'checkbox', id: 'ap-on' });
    const apFields = h('div');
    const count = h('span');
    const undoBtn = h('button', { type: 'button', class: 'btn ghost', text: 'Undo' });
    const saveBtn = h('button', { type: 'button', class: 'btn main', text: 'Save' });
    const bar = h('div', { class: 'unsaved', hidden: true }, count, h('div', { class: 'row-btns' }, undoBtn, ' ', saveBtn));
    const places = h('div', { class: 'sr-places' });
    const el = h('section', { class: 'card', hidden: true }, h('h2', { text: 'Login prompt & spam reports' }),
      h('p', { class: 'note', text: 'A soft AKILL or K-line (Logged-in people may pass, or % before the mask) and a DNS blocklist with soft-reject hold people who are not logged in and ask them to log in.' }),
      h('div', { class: 'my-row' }, h('label', { class: 'ck', for: 'ap-on' }, onBox, 'Ask them to log in'),
        h('span', { class: 'hint', text: 'Off: they are refused at once with "log in with SASL to connect".' })),
      apFields, bar,
      h('h3', { class: 'sr-head', text: 'Spam reports' }),
      h('p', { class: 'note', text: '/SPAMREPORT nick and Report spam on People send the address to these places. Local network addresses, opers and exceptions are never sent.' }),
      places);
    let set = {}, fields = [], load = async () => {};

    function check(f) {
      const v = f.input.value.trim();
      f.ok = f.s.name === 'auth-prompt-time' ? /^(\d+[smh]?|(\d+m)?(\d+s)?)$/.test(v) && v !== '' : v.length <= 300;
      f.dirty = v !== f.s.value;
      f.row.classList.toggle('changed', f.ok && f.dirty);
      f.input.classList.toggle('wrong', !f.ok);
      f.hint.classList.toggle('wrong', !f.ok);
      f.hint.textContent = !f.ok ? (f.s.name === 'auth-prompt-time' ? 'Use a time like 60s or 2m.' : 'At most 300 characters.') : f.dirty ? 'was ' + f.s.value : f.def.hint;
      const n = fields.filter(x => x.dirty).length;
      bar.hidden = n === 0;
      count.textContent = n === 1 ? '1 unsaved change' : n + ' unsaved changes';
      saveBtn.disabled = fields.some(x => x.dirty && !x.ok);
    }
    function build() {
      fields = AP.filter(d => set[d.name]).map(d => {
        const id = 'set-' + d.name;
        const input = h('input', { class: 'in', id, type: 'text', autocomplete: 'off', maxlength: '300', value: set[d.name].value, 'aria-describedby': id + '-hint' });
        const hint = h('span', { class: 'hint', id: id + '-hint' });
        const row = h('label', { class: 'f ap-row', for: id }, d.label, input, hint);
        const f = { s: set[d.name], def: d, input, hint, row };
        input.addEventListener('input', () => check(f));
        input.addEventListener('keydown', e => { if (e.key === 'Enter' && !saveBtn.disabled) saveBtn.click(); });
        return f;
      });
      put(apFields, ...fields.map(f => f.row));
      fields.forEach(check);
      if (!fields.length) bar.hidden = true;
    }
    undoBtn.addEventListener('click', build);
    saveBtn.addEventListener('click', () => busy(saveBtn, async () => {
      const done = [];
      for (const f of fields.filter(x => x.dirty && x.ok)) {
        try {
          const d = await api('POST', '/api/set', { name: f.s.name, value: f.input.value.trim() || 'default' });
          done.push(d.changed);
          set[f.s.name].value = d.changed.new;
        } catch (e) { toastErr(f.def.label + ' was not saved: ' + e.message); }
      }
      build();
      if (!done.length) return;
      toast(['Login prompt saved and applied'], async () => {
        for (const c of done.slice().reverse()) if (c.id) await api('POST', '/api/undo', { id: c.id });
        await load();
        toast(['Put back as it was']);
      });
    }));
    onBox.addEventListener('change', async () => {
      onBox.disabled = true;
      try {
        const d = await api('POST', '/api/set', { name: 'auth-prompt', value: onBox.checked ? 'on' : 'off' });
        toast([d.changed.new === 'on' ? 'People held by a soft ban are asked to log in' : 'Login prompt off: soft bans refuse at once'], async () => {
          await api('POST', '/api/undo', { id: d.changed.id });
          onBox.checked = d.changed.old === 'on';
          toast(['Put back as it was']);
        });
      } catch (e) { onBox.checked = !onBox.checked; toastErr(e.message); }
      onBox.disabled = false;
    });

    // One row per place; with none yet, a row that adds "dronebl" with a key.
    function placeRow(pl) {
      const name = pl.name || 'dronebl';
      const id = 'sr-key-' + name;
      const key = h('input', { class: 'in mono', id, type: 'password', autocomplete: 'off', spellcheck: 'false',
        placeholder: pl.keySet ? 'Key set · paste a new one to replace it' : 'Paste your DroneBL rpckey' });
      const saveKey = h('button', { type: 'button', class: 'btn main', text: pl.name ? 'Save key' : 'Add DroneBL' });
      const err = h('span', { class: 'hint' });
      saveKey.addEventListener('click', () => busy(saveKey, async () => {
        err.classList.remove('wrong'); err.textContent = '';
        if (!key.value.trim()) { key.classList.add('wrong'); key.focus(); return; }
        try {
          const d = await api('POST', '/api/spam-report-key', { name, value: key.value.trim() });
          key.value = '';
          toast([d.done.text]);
          drawPlaces(d.spamReports.places);
        } catch (e) { err.classList.add('wrong'); err.textContent = e.message; }
      }));
      key.addEventListener('input', () => key.classList.remove('wrong'));
      key.addEventListener('keydown', e => { if (e.key === 'Enter') saveKey.click(); });
      const kids = [];
      if (!pl.name) {
        kids.push(h('div', { class: 'act-text' }, h('b', { text: 'DroneBL' }),
          h('span', { class: 'lbl' }, 'No place yet. Get your own key at ', h('a', { href: 'https://dronebl.org/rpckey_signup', target: '_blank', rel: 'noopener', text: 'dronebl.org/rpckey_signup' }), ', then paste it here.')));
      } else {
        kids.push(h('div', { class: 'act-text' }, h('b', { text: pl.name }), h('span', { class: 'lbl', text: pl.line })));
      }
      if (pl.type === 'http') return h('div', { class: 'act sr-place' }, kids);
      const keyRow = h('div', { class: 'sr-key' }, h('label', { class: 'sr', for: id, text: 'DroneBL key for ' + name }), key, saveKey);
      kids.push(keyRow);
      if (pl.keySet) {
        const rm = h('button', { type: 'button', class: 'btn ghost', text: 'Remove key' });
        rm.addEventListener('click', () => {
          const yes = h('button', { type: 'button', class: 'btn bad', text: 'Remove' });
          const no = h('button', { type: 'button', class: 'btn ghost', text: 'Keep' });
          const box = h('span', { class: 'rm' }, yes, no);
          rm.replaceWith(box);
          no.addEventListener('click', () => box.replaceWith(rm));
          yes.addEventListener('click', () => busy(yes, async () => {
            try {
              const d = await api('POST', '/api/spam-report-key', { name, value: '' });
              toast([d.done.text]);
              drawPlaces(d.spamReports.places);
            } catch (e) { toastErr(e.message); box.replaceWith(rm); }
          }));
          yes.focus();
        });
        keyRow.append(rm);
      }
      if (pl.name) {
        const sid = 'sr-stage-' + pl.name;
        const stage = h('input', { type: 'checkbox', id: sid });
        stage.checked = !!pl.staging;
        stage.addEventListener('change', async () => {
          stage.disabled = true;
          try {
            const d = await api('POST', '/api/spam-report-staging', { name, value: stage.checked ? 'on' : 'off' });
            toast([d.done.text]);
            drawPlaces(d.spamReports.places);
          } catch (e) { stage.checked = !stage.checked; toastErr(e.message); stage.disabled = false; }
        });
        kids.push(h('label', { class: 'ck', for: sid }, stage, 'Test list (staging): nothing is really listed'));
      }
      kids.push(err);
      return h('div', { class: 'act sr-place' }, kids);
    }
    function drawPlaces(list) {
      put(places, ...(list.length ? list.map(placeRow) : [placeRow({ name: '', type: 'dronebl' })]));
    }
    return {
      el,
      async fill(byName, reload) {
        load = reload;
        set = {};
        for (const d of AP) if (byName[d.name]) set[d.name] = Object.assign({}, byName[d.name]);
        el.hidden = !byName['auth-prompt'];
        if (el.hidden) return;
        onBox.checked = byName['auth-prompt'].value === 'on';
        build();
        try {
          const d = await api('GET', '/api/spam-reports');
          drawPlaces(d.spamReports.places);
        } catch (e) { put(places, h('p', { class: 'empty', text: e.message })); }
      },
    };
  }

  // Messages ([messages]): fixed quit/part texts, quit hiding and the ban contact.
  function messagesCard() {
    const count = h('span');
    const undoBtn = h('button', { type: 'button', class: 'btn ghost', text: 'Undo' });
    const saveBtn = h('button', { type: 'button', class: 'btn main', text: 'Save' });
    const bar = h('div', { class: 'unsaved', hidden: true }, count, h('div', { class: 'row-btns' }, undoBtn, ' ', saveBtn));
    const body = h('div');
    const el = h('section', { class: 'card', hidden: true }, h('h2', { text: 'Messages' }),
      h('p', { class: 'note', text: 'Texts people see when they quit, part, get turned away or banned. Empty means the built-in text.' }),
      body, bar);
    let set = {}, fields = [], load = async () => {};

    // "10s", "1m30s", "1h", a bare number of seconds, "off" or "0": seconds, or -1.
    function secsOf(v) {
      v = v.trim().toLowerCase();
      if (v === 'off' || v === '0') return 0;
      if (/^\d+$/.test(v)) return Number(v);
      const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(v);
      return m && v !== '' ? Number(m[1] || 0) * 3600 + Number(m[2] || 0) * 60 + Number(m[3] || 0) : -1;
    }
    const isOff = v => v === '' || v.toLowerCase() === 'off';
    const NAMES = ['static-quit', 'static-part', 'quit-message-min-time', 'quit-prefix', 'reject-full', 'reject-too-many', 'reject-password',
      'ban-notice', 'ban-contact', 'ban-quit', 'hide-killed-by'];
    const GROUP = {};
    for (const n of NAMES.slice(0, 4)) GROUP[n] = 'Quit and part';
    for (const n of NAMES.slice(4, 7)) GROUP[n] = 'Turned away';
    for (const n of NAMES.slice(7)) GROUP[n] = 'Bans and kills';
    const textRow = (id, label, max, cls) => {
      const input = h('input', { class: 'in', id, type: 'text', autocomplete: 'off', maxlength: String(max), 'aria-describedby': id + '-hint' });
      const hint = h('span', { class: 'hint', id: id + '-hint' });
      return { input, hint, row: h('label', { class: cls || 'f ap-row', for: id }, label, input, hint) };
    };
    const mk = (name, label, loaded) => ({ name, label, loaded, ok: true, dirty: false, now: () => '', draw() {}, reset() {} });

    function build() {
      fields = [];
      if (set['static-quit']) {
        const s = set['static-quit'];
        const f = mk('static-quit', 'Quit message for everyone', isOff(s.value) ? 'off' : s.value);
        const t = textRow('msg-static-quit', f.label, 300);
        f.row = t.row; f.input = t.input; f.hint = t.hint;
        f.reset = () => { t.input.value = f.loaded === 'off' ? '' : f.loaded; };
        f.now = () => t.input.value.trim() || 'off';
        f.draw = () => {
          f.ok = t.input.value.trim().length <= 300;
          f.err = 'At most 300 characters.';
          f.was = f.loaded === 'off' ? 'off' : f.loaded;
          f.base = 'Empty is off. Everyone\'s quit reason becomes this text.';
        };
        fields.push(f);
      }
      if (set['static-part']) {
        const s = set['static-part'];
        const v = s.value.trim();
        const mode0 = isOff(v) || v.toLowerCase() === 'no' ? 'keep' : v.toLowerCase() === 'yes' ? 'remove' : 'replace';
        const f = mk('static-part', 'Part messages', mode0 === 'keep' ? 'off' : mode0 === 'remove' ? 'yes' : v);
        let mode = mode0;
        const t = textRow('msg-static-part', 'Text people see instead', 300, 'f');
        const btns = [['keep', 'Keep'], ['remove', 'Remove'], ['replace', 'Replace with text']].map(([m, lab]) =>
          h('button', { type: 'button', text: lab, 'aria-pressed': 'false', 'data-mode': m }));
        const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Part messages' }, ...btns);
        const hint = h('span', { class: 'hint', id: 'msg-static-part-hint' });
        f.row = h('div', { class: 'f ap-row' }, 'Part messages', seg, t.row, hint);
        f.input = t.input; f.hint = hint;
        t.hint.hidden = true;
        f.reset = () => { mode = mode0; t.input.value = mode0 === 'replace' ? v : ''; };
        f.now = () => mode === 'keep' ? 'off' : mode === 'remove' ? 'yes' : t.input.value.trim();
        f.draw = () => {
          btns.forEach(b => b.setAttribute('aria-pressed', b.dataset.mode === mode ? 'true' : 'false'));
          t.row.hidden = mode !== 'replace';
          f.ok = mode !== 'replace' || (t.input.value.trim() !== '' && t.input.value.trim().length <= 300);
          f.err = t.input.value.trim() === '' ? 'Write the text people will see instead.' : 'At most 300 characters.';
          f.was = mode0 === 'keep' ? 'Keep' : mode0 === 'remove' ? 'Remove' : 'Replace with "' + v + '"';
          f.base = 'Remove drops the reason from every part.';
        };
        btns.forEach(b => b.addEventListener('click', () => { mode = b.dataset.mode; check(f); if (mode === 'replace') t.input.focus(); }));
        fields.push(f);
      }
      if (set['quit-message-min-time']) {
        const s = set['quit-message-min-time'];
        const f = mk('quit-message-min-time', 'Hide quit messages of people online less than', isOff(s.value) ? 'off' : s.value);
        const t = textRow('msg-quit-min', f.label, 12);
        t.input.classList.add('mono');
        f.row = t.row; f.input = t.input; f.hint = t.hint;
        f.reset = () => { t.input.value = f.loaded === 'off' ? '0' : f.loaded; };
        f.now = () => secsOf(t.input.value) === 0 ? 'off' : t.input.value.trim().toLowerCase();
        f.dirtyNow = () => secsOf(t.input.value) !== secsOf(f.loaded);
        f.draw = () => {
          const n = secsOf(t.input.value);
          f.ok = n >= 0 && n <= 3600;
          f.err = 'Use a time like 10s or 1m, up to 1h. 0 is off.';
          f.was = f.loaded === 'off' ? '0' : f.loaded;
          f.base = '0 for off. Up to 1h. Not used when a quit message for everyone is set.';
        };
        fields.push(f);
      }
      if (set['quit-prefix']) {
        // The server sends "Quit: " in quotes (or none); a typed prefix goes back in quotes so a trailing space stays.
        const s = set['quit-prefix'];
        let raw = s.value;
        if (raw.length >= 2 && raw.startsWith('"') && raw.endsWith('"')) raw = raw.slice(1, -1);
        const norm = v => v.trim().toLowerCase() === 'none' ? 'none' : v.trim() === '' || v === 'Quit: ' ? '' : v;
        const f = mk('quit-prefix', 'Quit prefix', norm(raw));
        const t = textRow('msg-quit-prefix', f.label, 300);
        t.input.placeholder = 'Quit: ';
        f.row = t.row; f.input = t.input; f.hint = t.hint;
        f.reset = () => { t.input.value = f.loaded; };
        f.now = () => { const v = norm(t.input.value); return v === '' ? 'default' : v === 'none' ? 'none' : '"' + t.input.value + '"'; };
        f.dirtyNow = () => norm(t.input.value) !== f.loaded;
        f.draw = () => {
          f.ok = t.input.value.length <= 300;
          f.err = 'At most 300 characters.';
          f.was = f.loaded === '' ? 'Quit: ' : f.loaded === 'none' ? 'none' : '"' + f.loaded + '"';
          f.base = 'Shown before a quit reason. Empty is "Quit: ". Write none for no prefix.';
        };
        fields.push(f);
      }
      // A text with a built-in default: empty puts the built-in text back ("default" for the server).
      const builtin = (name, label, dflt) => {
        if (!set[name]) return;
        const v = set[name].value.trim();
        const f = mk(name, label, v === '' || v.toLowerCase() === 'default' || v === dflt ? 'default' : v);
        const t = textRow('msg-' + name, label, 300);
        t.input.placeholder = dflt;
        f.row = t.row; f.input = t.input; f.hint = t.hint;
        f.reset = () => { t.input.value = f.loaded === 'default' ? '' : f.loaded; };
        f.now = () => t.input.value.trim() || 'default';
        f.draw = () => {
          f.ok = t.input.value.trim().length <= 300;
          f.err = 'At most 300 characters.';
          f.was = f.loaded === 'default' ? 'the built-in text' : f.loaded;
          f.base = 'Empty uses the built-in text shown in grey.';
        };
        fields.push(f);
      };
      builtin('reject-full', 'Server full', 'This server is full. Please try again later.');
      builtin('reject-too-many', 'Too many from one IP', 'Too many connections from your IP');
      builtin('reject-password', 'Wrong password', 'Password incorrect');
      // A text where empty is off.
      const offText = (name, label, base) => {
        if (!set[name]) return;
        const s = set[name];
        const f = mk(name, label, isOff(s.value) ? 'off' : s.value);
        const t = textRow('msg-' + name, label, 300);
        f.row = t.row; f.input = t.input; f.hint = t.hint;
        f.reset = () => { t.input.value = f.loaded === 'off' ? '' : f.loaded; };
        f.now = () => t.input.value.trim() || 'off';
        f.draw = () => {
          const v = t.input.value.trim();
          f.ok = v.length <= 300;
          f.err = 'At most 300 characters.';
          f.was = f.loaded === 'off' ? 'off' : f.loaded;
          f.base = base(v);
        };
        fields.push(f);
      };
      offText('ban-notice', 'Notice to banned people', () => 'Sent to a banned person just before they are disconnected. Empty is off.');
      offText('ban-contact', 'Ban contact', v => v ? 'Banned people see: "… Email ' + v + ' for more information."' : 'Off');
      if (set['ban-quit']) {
        const s = set['ban-quit'];
        const v = s.value.trim();
        const mode0 = isOff(v) || v.toLowerCase() === 'no' ? 'keep' : v.toLowerCase() === 'yes' ? 'remove' : 'replace';
        const f = mk('ban-quit', 'Others see a ban as', mode0 === 'keep' ? 'off' : mode0 === 'remove' ? 'yes' : v);
        let mode = mode0;
        const t = textRow('msg-ban-quit', 'Text others see instead', 300, 'f');
        const btns = [['keep', 'Banned: reason'], ['remove', 'Banned'], ['replace', 'Own text']].map(([m, lab]) =>
          h('button', { type: 'button', text: lab, 'aria-pressed': 'false', 'data-mode': m }));
        const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Others see a ban as' }, ...btns);
        const hint = h('span', { class: 'hint', id: 'msg-ban-quit-hint' });
        f.row = h('div', { class: 'f ap-row' }, 'Others see a ban as', seg, t.row, hint);
        f.input = t.input; f.hint = hint;
        t.hint.hidden = true;
        f.reset = () => { mode = mode0; t.input.value = mode0 === 'replace' ? v : ''; };
        f.now = () => mode === 'keep' ? 'off' : mode === 'remove' ? 'yes' : t.input.value.trim();
        f.draw = () => {
          btns.forEach(b => b.setAttribute('aria-pressed', b.dataset.mode === mode ? 'true' : 'false'));
          t.row.hidden = mode !== 'replace';
          const own = t.input.value.trim();
          const clash = ['yes', 'no', 'off'].includes(own.toLowerCase());
          f.ok = mode !== 'replace' || (own !== '' && own.length <= 300 && !clash);
          f.err = own === '' ? 'Write the text others will see instead.' : clash ? 'Write a different text.' : 'At most 300 characters.';
          f.was = mode0 === 'keep' ? 'Banned: reason' : mode0 === 'remove' ? 'Banned' : 'Own text "' + v + '"';
          f.base = 'What everyone else sees as the quit reason of a banned person.';
        };
        btns.forEach(b => b.addEventListener('click', () => { mode = b.dataset.mode; check(f); if (mode === 'replace') t.input.focus(); }));
        fields.push(f);
      }
      offText('hide-killed-by', 'Show kills as done by', () => 'Empty shows the real oper. Opers always see who really did it.');
      for (const f of fields) {
        f.reset();
        f.input.addEventListener('input', () => check(f));
        f.input.addEventListener('keydown', e => { if (e.key === 'Enter' && !saveBtn.disabled) saveBtn.click(); });
      }
      const rows = [];
      let last = '';
      for (const f of fields) {
        const g = GROUP[f.name];
        if (g !== last) rows.push(h('div', { class: 'grp msg-grp', text: g }));
        last = g;
        rows.push(f.row);
      }
      put(body, ...rows);
      fields.forEach(check);
      if (!fields.length) bar.hidden = true;
    }
    function check(f) {
      f.draw();
      f.dirty = f.dirtyNow ? f.dirtyNow() : f.now().toLowerCase() !== f.loaded.toLowerCase();
      f.row.classList.toggle('changed', f.ok && f.dirty);
      f.input.classList.toggle('wrong', !f.ok);
      f.hint.classList.toggle('wrong', !f.ok);
      f.hint.textContent = !f.ok ? f.err : f.dirty ? 'was ' + f.was : f.base;
      const n = fields.filter(x => x.dirty).length;
      bar.hidden = n === 0;
      count.textContent = n === 1 ? '1 unsaved change' : n + ' unsaved changes';
      saveBtn.disabled = fields.some(x => x.dirty && !x.ok);
    }
    undoBtn.addEventListener('click', build);
    saveBtn.addEventListener('click', () => busy(saveBtn, async () => {
      const done = [];
      for (const f of fields.filter(x => x.dirty && x.ok)) {
        try {
          const d = await api('POST', '/api/set', { name: f.name, value: f.now() });
          done.push(d.changed);
          set[f.name].value = d.changed.new;
        } catch (e) { toastErr(f.label + ' was not saved: ' + e.message); }
      }
      build();
      if (!done.length) return;
      toast(['Messages saved and applied'], async () => {
        for (const c of done.slice().reverse()) if (c.id) await api('POST', '/api/undo', { id: c.id });
        await load();
        toast(['Put back as it was']);
      });
    }));
    return {
      el,
      fill(byName, reload) {
        load = reload;
        set = {};
        for (const n of NAMES) if (byName[n]) set[n] = Object.assign({}, byName[n]);
        el.hidden = !Object.keys(set).length;
        if (!el.hidden) build();
      },
    };
  }

  function settingsPage() {
    const aj = autoJoinCard();
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
        if (byName.members) { membersCard.hidden = false; membersBox.checked = byName.members.value === 'on'; }
        aj.fill(byName, load);
        ls.fill(byName, load);
        ms.fill(byName, load);
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
    const mailBox = me.owner ? mailCard() : null;
    const ls = loginSpamCard();
    const ms = messagesCard();

    // Member sign-in: people with a registered nick get their own pages
    // (Home, Chat, My nick, My channels, Memos), never the admin ones.
    const membersBox = h('input', { type: 'checkbox', id: 'set-members' });
    membersBox.addEventListener('change', async () => {
      membersBox.disabled = true;
      try {
        const d = await api('POST', '/api/set', { name: 'members', value: membersBox.checked ? 'on' : 'off' });
        toast([d.changed.new === 'on' ? 'Members can sign in now' : 'Member sign-in is off; member sessions end'], async () => {
          await api('POST', '/api/undo', { id: d.changed.id });
          membersBox.checked = d.changed.old === 'on';
          toast(['Put back as it was']);
        });
      } catch (e) { membersBox.checked = !membersBox.checked; toastErr(e.message); }
      membersBox.disabled = false;
    });
    const membersCard = h('section', { class: 'card', hidden: true }, h('h2', { text: 'Members' }),
      h('div', { class: 'my-row' }, h('label', { class: 'ck', for: 'set-members' }, membersBox, 'Let members sign in'),
        h('span', { class: 'hint', text: 'Anyone with a registered nick signs in with their NickServ password and sees only their own pages: Home, Chat, My nick, My channels and Memos. The server checks every request.' })),
      h('p', { class: 'note', text: 'To let them in from outside, put the panel behind HTTPS on your web server and list it in [web] trusted_proxy (docs/ADMIN.md, "Let members reach the Hive Panel").' }));

    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Settings' }),
        h('span', { class: 'sub', text: 'Saved to beeircd.conf and applied at once' }))),
      h('div', { class: 'two' },
        h('section', { class: 'card' }, h('h2', { text: 'Limits' }), grid, bar),
        h('section', { class: 'card' }, h('h2', { text: 'Server actions' }), actions)),
      aj.el,
      ls.el,
      ms.el,
      membersCard,
      mailBox && mailBox.el,
      backups && backups.el);
    load();
    return {
      el,
      status(s) {
        if (backups) backups.status(s);
        if (s.update) {
          updateRow.hidden = false;
          put(updateRow, h('div', { class: 'act-text' }, h('b', { text: 'Update' }),
            h('span', { class: 'lbl', text: s.update.version + ' is out. Run ' + (s.update.how || 'beeircd update') + ' on the server.' })));
        } else updateRow.hidden = true;
      },
    };
  }

  // ---------- People ----------
  // ccChip is a country code in a small box; a dashed empty one when GeoIP
  // doesn't know the address.
  function ccChip(cc) {
    return cc ? h('span', { class: 'cc', text: cc }) : h('span', { class: 'cc none', 'aria-label': 'not known', text: '—' });
  }
  function asnCell(asn, org) {
    if (!asn) return h('span', { class: 'asn none', text: '—' });
    return h('span', { class: 'asn', title: org || '' }, 'AS' + asn, org ? h('small', { text: org }) : null);
  }
  // ---------- People: Accounts (flag s) ----------
  // Every registered account, online or not; one opened can be suspended,
  // dropped, given a new password or email. Each is NickServ's own command.
  const servDo = body => api('POST', '/api/serv-do', body);
  const LIVES = [['1d', '1 day'], ['7d', '7 days'], ['30d', '30 days'], ['0', 'Until lifted']];
  function accountsView() {
    const PAGE = 50;
    let q = '', filter = 'all', offset = 0, openName = null, data = null, typing = 0;
    const tile = label => { const n = h('span', { class: 'num', text: '…' }); return { n, el: h('section', { class: 'card tight' }, h('span', { class: 'lbl', text: label }), n) }; };
    const tAll = tile('Accounts'), tOn = tile('Online now'), tSus = tile('Suspended'), tUn = tile('Not confirmed');
    const search = h('input', { class: 'search', type: 'search', placeholder: 'Search account, nick or email (* and ? too)', 'aria-label': 'Search accounts', autocomplete: 'off', spellcheck: 'false' });
    const segs = [['all', 'All'], ['online', 'Online'], ['suspended', 'Suspended'], ['unconfirmed', 'Not confirmed']].map(([v, label]) =>
      h('button', { type: 'button', 'aria-pressed': v === filter ? 'true' : 'false', 'data-f': v, text: label }));
    const tbody = h('tbody');
    const COLS = 6;
    const count = h('span', { class: 'lbl' });
    const back = h('button', { type: 'button', class: 'btn', text: 'Back' });
    const next = h('button', { type: 'button', class: 'btn', text: 'Next' });
    const el = h('div', { class: 'main-stack' },
      h('div', { class: 'tiles4' }, tAll.el, tOn.el, tSus.el, tUn.el),
      h('section', { class: 'card list-card' },
        h('div', { class: 'tools' }, search, h('div', { class: 'seg', role: 'group', 'aria-label': 'Show' }, segs)),
        h('div', { class: 'wrap' }, h('table', {},
          h('thead', {}, h('tr', {}, ['Account', 'Email', 'Nicks', 'Registered', 'Last seen', 'State'].map(t => h('th', { scope: 'col', text: t })))), tbody)),
        h('div', { class: 'pager' }, count, h('div', { class: 'row-btns' }, back, next))));

    async function load() {
      try {
        data = (await api('GET', '/api/accounts?' + new URLSearchParams({ q, filter, offset }))).accounts;
        draw();
      } catch (e) { put(tbody, h('tr', {}, h('td', { colspan: COLS, class: 'empty', text: e.message }))); }
    }
    const state = r => r.suspended ? h('span', { class: 'tag sus', text: 'suspended' })
      : r.online ? h('span', { class: 'tag reg', text: r.online > 1 ? 'online ×' + r.online : 'online' })
      : r.unconfirmed ? h('span', { class: 'tag', text: 'not confirmed' }) : h('span', { class: 'lbl', text: '—' });
    function draw() {
      const d = data;
      if (!d) return;
      tAll.n.textContent = fmt(d.total); tOn.n.textContent = fmt(d.online); tSus.n.textContent = fmt(d.suspended); tUn.n.textContent = fmt(d.unconfirmed);
      const rows = [];
      if (!d.rows.length) rows.push(h('tr', {}, h('td', { colspan: COLS, class: 'empty', text: q || filter !== 'all' ? 'No account matches.' : 'Nobody has registered yet.' })));
      for (const r of d.rows) {
        const open = r.name === openName;
        const tr = h('tr', { class: 'person' + (open ? ' open' : '') },
          h('td', {}, h('button', { type: 'button', class: 'rowbtn', 'aria-expanded': open ? 'true' : 'false' }, r.name), r.oper ? [' ', h('span', { class: 'tag', text: 'oper' })] : null),
          h('td', { class: 'mono', text: r.email || '—' }),
          h('td', { text: String(r.nicks) }),
          h('td', { class: 'lbl', text: day(r.registered) }),
          h('td', { class: 'lbl', text: r.online ? 'now' : r.last_seen ? ago(r.last_seen) : '—' }),
          h('td', {}, state(r)));
        tr.addEventListener('click', () => { openName = openName === r.name ? null : r.name; draw(); });
        rows.push(tr);
        if (open) rows.push(detailRow(r.name));
      }
      put(tbody, ...rows);
      const from = d.matched ? d.offset + 1 : 0, to = d.offset + d.rows.length;
      count.textContent = fmt(from) + ' to ' + fmt(to) + ' of ' + fmt(d.matched);
      back.disabled = d.offset <= 0;
      next.disabled = to >= d.matched;
    }
    function detailRow(name) {
      const box = h('div', {}, h('p', { class: 'empty', text: 'Loading…' }));
      const tr = h('tr', { class: 'detail' }, h('td', { colspan: COLS }, box));
      // Clicks inside the open account don't close it.
      tr.addEventListener('click', e => e.stopPropagation());
      api('GET', '/api/account?name=' + encodeURIComponent(name)).then(d => fill(box, d.account))
        .catch(e => put(box, h('p', { class: 'empty', text: e.message })));
      return tr;
    }
    const item = (label, value, cls, wide) => h('div', { class: wide ? 'wide' : null }, h('span', { class: 'lbl', text: label }), h('span', { class: cls || null, text: value || '—' }));
    function fill(box, a) {
      const items = [
        item('Nicks', a.nick_list.join(' '), 'mono', true),
        item('Email', a.email || 'none', 'mono'),
        item('Registered', day(a.registered)),
        item('Last seen', a.online ? 'online now' + (a.online > 1 ? ' (' + a.online + ' connections)' : '') : a.last_seen ? ago(a.last_seen) : '—'),
        item('Last host', a.last_host, 'mono'),
        item('Vhost', a.vhost || 'none', 'mono'),
        item('Founder of', (a.founder || []).join(' ') || 'no channels', 'mono', true),
      ];
      if (a.unconfirmed) items.push(item('Email confirmed', 'not yet'));
      if (a.no_expire) items.push(item('Expires', 'never (kept)'));
      if (a.suspended) items.push(item('Suspended', 'by ' + (a.suspended_by || '?') + (a.suspended_at ? ' ' + ago(a.suspended_at) : '') +
        ' · ' + (a.suspend_until ? 'ends ' + ends(a.suspend_until) : 'until lifted') + (a.suspend_reason ? ' · ' + a.suspend_reason : ''), null, true));
      if (a.locked) {
        put(box, h('div', { class: 'dl' }, items), h('p', { class: 'hint', text: a.locked }));
        return;
      }
      const sure = h('div', { class: 'confirm', hidden: true });
      const run = (btn, body) => busy(btn, async () => {
        try { toast([(await servDo(body)).done.text]); sure.hidden = true; load(); }
        catch (e) { toastErr(e.message); }
      });
      // ask shows one small form under the account; its button runs it.
      const ask = (label, fields, goText, goCls, body) => {
        const go = h('button', { type: 'button', class: 'btn ' + goCls, text: goText });
        const cancel = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
        cancel.addEventListener('click', () => { sure.hidden = true; });
        go.addEventListener('click', () => { const b = body(); if (b) run(go, b); });
        for (const f of fields) if (f.tagName === 'INPUT') f.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); go.click(); } });
        put(sure, h('span', { class: 'f', text: label }), ...fields, go, cancel);
        sure.hidden = false;
        (fields.find(f => f.tagName === 'INPUT') || go).focus();
      };
      const acts = [];
      const btn = (text, cls, f) => { const b = h('button', { type: 'button', class: 'btn' + (cls ? ' ' + cls : ''), text }); b.addEventListener('click', () => f(b)); acts.push(b); };
      if (a.suspended) btn('Unsuspend', 'main', b => run(b, { kind: 'unsuspend', name: a.name }));
      else btn('Suspend…', 'bad', () => {
        const reason = h('input', { class: 'in', type: 'text', maxlength: '200', autocomplete: 'off', placeholder: 'Reason (opers see it)', 'aria-label': 'Reason' });
        const life = h('select', { class: 'in', 'aria-label': 'Suspend for' }, LIVES.map(([v, t]) => h('option', { value: v, text: t })));
        life.value = '7d';
        ask('Suspend ' + a.name + "? They are logged out and can't identify until it ends.", [reason, life], 'Suspend ' + a.name, 'bad',
          () => ({ kind: 'suspend', name: a.name, reason: reason.value, life: life.value }));
      });
      btn('New password…', '', () => {
        const pw = h('input', { class: 'in', type: 'password', maxlength: '200', autocomplete: 'new-password', placeholder: 'New password', 'aria-label': 'New password' });
        ask('New password for ' + a.name + ". Tell them yourself; services don't send it.", [pw], 'Set password', 'main', () => {
          if (!pw.value) { pw.classList.add('wrong'); pw.focus(); return null; }
          return { kind: 'password', name: a.name, password: pw.value };
        });
      });
      btn('Email…', '', () => {
        const em = h('input', { class: 'in mono', type: 'email', maxlength: '200', autocomplete: 'off', placeholder: 'Empty removes it', 'aria-label': 'Email' });
        em.value = a.email || '';
        ask('Email for ' + a.name + ':', [em], 'Save email', 'main', () => ({ kind: 'email', name: a.name, value: em.value.trim() }));
      });
      btn('Drop…', 'bad', () => {
        ask('Drop ' + a.name + '? Its ' + plural(a.nick_list.length, 'nick', 'nicks') + ' and memos go for good' +
          ((a.founder || []).length ? '; its channels pass to their successors or are dropped' : '') + '.', [], 'Drop ' + a.name, 'bad',
          () => ({ kind: 'drop', name: a.name }));
      });
      put(box, h('div', { class: 'dl' }, items), h('div', { class: 'acts' }, h('div', { class: 'end' }, acts)), sure);
    }
    search.addEventListener('input', () => {
      clearTimeout(typing);
      typing = setTimeout(() => { q = search.value.trim(); offset = 0; openName = null; load(); }, 250);
    });
    for (const b of segs) b.addEventListener('click', () => {
      filter = b.dataset.f; offset = 0; openName = null;
      segs.forEach(x => x.setAttribute('aria-pressed', x === b ? 'true' : 'false'));
      load();
    });
    back.addEventListener('click', () => { offset = Math.max(0, offset - PAGE); openName = null; load(); });
    next.addEventListener('click', () => { offset += PAGE; openName = null; load(); });
    let timer = 0;
    return { el,
      show() { load(); clearInterval(timer); timer = setInterval(() => { if (!openName && !document.hidden) bg(load); }, 30000); },
      hide() { clearInterval(timer); },
      esc() { if (!openName) return false; openName = null; draw(); return true; } };
  }

  function peoplePage() {
    const PAGE = 50;
    let q = '', filter = 'all', offset = 0, openUID = null, data = null, typing = 0;
    const tile = label => { const n = h('span', { class: 'num', text: '…' }); return { n, el: h('section', { class: 'card tight' }, h('span', { class: 'lbl', text: label }), n) }; };
    const tOn = tile('Online'), tIn = tile('Logged in'), tGuests = tile('Guests'), tOpers = tile('Opers');
    const search = h('input', { class: 'search', type: 'search', placeholder: has('h') ? 'Search nick, host, address, account, country or network' : 'Search nick, host or account', 'aria-label': 'Search people', autocomplete: 'off', spellcheck: 'false' });
    // #/people?q=MY (a row of the Overview's Where card): that search.
    const asked = new URLSearchParams(location.hash.split('?')[1] || '').get('q');
    if (asked) { q = asked; search.value = asked; }
    const segs = [['all', 'All'], ['in', 'Logged in'], ['guests', 'Guests'], ['opers', 'Opers']].map(([v, label]) =>
      h('button', { type: 'button', 'aria-pressed': v === filter ? 'true' : 'false', 'data-f': v, text: label }));
    const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Show' }, segs);
    const tbody = h('tbody');
    const thead = h('thead');
    let cols = 0; // columns of the table (country and network when GeoIP is on)
    const heads = geo => {
      const names = geo ? ['Nick', 'Host', 'Country', 'Network', 'Server', 'Channels', 'Online for'] : ['Nick', 'Host', 'Server', 'Channels', 'Online for'];
      if (cols === names.length) return;
      cols = names.length;
      put(thead, h('tr', {}, names.map(t => h('th', { scope: 'col', text: t }))));
    };
    heads(false);
    const count = h('span', { class: 'lbl' });
    const back = h('button', { type: 'button', class: 'btn', text: 'Back' });
    const next = h('button', { type: 'button', class: 'btn', text: 'Next' });
    const onlineBox = h('div', { class: 'main-stack' },
      h('div', { class: 'tiles4' }, tOn.el, tIn.el, tGuests.el, tOpers.el),
      h('section', { class: 'card list-card' },
        h('div', { class: 'tools' }, search, seg),
        h('div', { class: 'wrap' }, h('table', {}, thead, tbody)),
        h('div', { class: 'pager' }, count, h('div', { class: 'row-btns' }, back, next))));
    // Online now or Accounts (every registered nick, flag s): #/people?view=accounts.
    const accts = has('s') ? accountsView() : null;
    let view = accts && new URLSearchParams(location.hash.split('?')[1] || '').get('view') === 'accounts' ? 'accounts' : 'online';
    const sub = h('span', { class: 'sub' });
    const views = accts ? [['online', 'Online now'], ['accounts', 'Accounts']].map(([v, label]) =>
      h('button', { type: 'button', 'data-v': v, text: label })) : [];
    function showView() {
      views.forEach(b => b.setAttribute('aria-pressed', b.dataset.v === view ? 'true' : 'false'));
      onlineBox.hidden = view !== 'online';
      sub.textContent = view === 'online' ? 'Everyone online on the network, on every server' : 'Every registered account, online or not';
      if (!accts) return;
      accts.el.hidden = view !== 'accounts';
      if (view === 'accounts') accts.show(); else accts.hide();
    }
    for (const b of views) b.addEventListener('click', () => {
      if (view === b.dataset.v) return;
      view = b.dataset.v; openUID = null;
      history.replaceState(null, '', '#/people' + (view === 'accounts' ? '?view=accounts' : ''));
      showView();
      if (view === 'online') load();
    });
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'People' }), sub),
        accts ? h('div', { class: 'seg', role: 'group', 'aria-label': 'Show' }, views) : null),
      onlineBox, accts ? accts.el : null);
    showView();

    async function load() {
      try {
        const d = await api('GET', '/api/people?' + new URLSearchParams({ q, filter, offset }));
        data = d.people;
        draw();
      } catch (e) {
        put(tbody, h('tr', {}, h('td', { colspan: cols, class: 'empty', text: e.message })));
      }
    }
    function draw() {
      const p = data;
      heads(!!p.geo);
      tOn.n.textContent = fmt(p.online); tIn.n.textContent = fmt(p.logged_in); tGuests.n.textContent = fmt(p.guests); tOpers.n.textContent = fmt(p.opers);
      const rows = [];
      if (!p.rows.length) rows.push(h('tr', {}, h('td', { colspan: cols, class: 'empty', text: q || filter !== 'all' ? 'Nobody matches.' : 'Nobody is online.' })));
      for (const r of p.rows) {
        const open = r.uid === openUID;
        const nickBtn = h('button', { type: 'button', class: 'rowbtn', 'aria-expanded': open ? 'true' : 'false' }, r.nick);
        const tr = h('tr', { class: 'person' + (open ? ' open' : '') },
          h('td', {}, nickBtn, r.oper ? [' ', h('span', { class: 'tag', text: 'oper' })] : null),
          h('td', { class: 'mono', text: r.host }),
          p.geo ? h('td', {}, ccChip(r.country)) : null,
          p.geo ? h('td', {}, asnCell(r.asn, r.as_org)) : null,
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
      const tr = h('tr', { class: 'detail' }, h('td', { colspan: cols }, box));
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
      if (data && data.geo) {
        items.push(item('Country', p.country ? (p.country_name || p.country) + ' (' + p.country + ')' : 'not known'));
        items.push(item('Network', p.asn ? 'AS' + p.asn + (p.as_org ? ' · ' + p.as_org : '') : 'not known', null, true));
      }

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
      function askReport() {
        const go = h('button', { type: 'button', class: 'btn bad', text: 'Report spam' });
        const cancel = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
        cancel.addEventListener('click', () => { confirmBox.hidden = true; });
        go.addEventListener('click', () => busy(go, async () => {
          try {
            const d = await api('POST', '/api/spam-report', { uid: p.uid });
            toast([d.done.text]);
            confirmBox.hidden = true;
          } catch (e) { toastErr(e.message); }
        }));
        put(confirmBox, h('span', { class: 'f' }, 'Report ' + (p.ip ? p.ip + ' (' + p.nick + ')' : p.nick) + ' to the spam blocklists? Other networks will block this address too.'), go, cancel);
        confirmBox.hidden = false;
        go.focus();
      }
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
        if (!p.oper_as) {
          const rb = h('button', { type: 'button', class: 'btn ghost', text: 'Report spam' });
          rb.addEventListener('click', askReport);
          ends.push(rb);
        }
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
    const onKey = e => {
      if (e.key !== 'Escape') return;
      if (view === 'accounts') accts.esc();
      else if (openUID) { openUID = null; draw(); }
    };
    document.addEventListener('keydown', onKey);
    // Fresh list every 15 s, unless a person is open (don't move things under the oper's hands).
    const timer = setInterval(() => { if (view === 'online' && !openUID && !document.hidden) bg(load); }, 15000);
    if (view === 'online') load();
    return { el, leave() { clearInterval(timer); if (accts) accts.hide(); document.removeEventListener('keydown', onKey); } };
  }

  // ---------- Bans ----------
  const KINDS = [
    { kind: 'akill', name: 'AKILL', what: 'user@host', life: '30d', ph: '*@203.0.113.9' },
    { kind: 'zline', name: 'ZLINE', what: 'IP, before connect', life: '30d', ph: '203.0.113.0/24' },
    { kind: 'shun', name: 'SHUN', what: "stays, can't talk", life: '30d', ph: '*@203.0.113.9' },
    { kind: 'snline', name: 'SNLINE', what: 'by realname', life: '30d', ph: '*free bitcoin*', field: 'Realname',
      hint: "* and ? as wildcards, or a /regex/. A nick takes that person's realname.", note: 'BeeIRCd servers only' },
    { kind: 'eline', name: 'Exception', what: 'never banned', life: '0', ph: '*@staff.example.org' },
    { kind: 'forbid', name: 'Forbid', what: 'nick or channel', life: '0', ph: 'BadNick or #badchan', field: 'Nick or channel', need: 's',
      hint: 'One nick or #channel, * and ? as wildcards. Nobody may use or register it; opers are never moved.', note: 'NickServ and ChanServ' },
  ];
  // Forbids need flag s, the network bans k.
  const shownKinds = () => KINDS.filter(k => has(k.need || 'k'));
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
    const SHOWN = shownKinds();
    let kind = SHOWN[0].kind, data = null, freshMask = null, matchTimer = 0, matchSeq = 0;
    const tiles = SHOWN.map(k => {
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
    const type = h('select', { class: 'in', id: 'ban-type' }, SHOWN.map(k => h('option', { value: k.kind, text: k.name + ' · ' + k.what })));
    const mask = h('input', { class: 'in mono', id: 'ban-mask', type: 'text', autocomplete: 'off', spellcheck: 'false' });
    const hit = h('span', { class: 'hit', role: 'status' });
    const maskLabel = h('span', { text: 'Mask' });
    const maskHint = h('span', { class: 'hint', id: 'ban-mask-hint', hidden: true });
    const reason = h('input', { class: 'in', id: 'ban-reason', type: 'text', maxlength: '200', autocomplete: 'off', placeholder: 'They see it' });
    const life = h('select', { class: 'in', id: 'ban-life' },
      [['1h', 'in 1 hour'], ['1d', 'in 1 day'], ['7d', 'in 7 days'], ['30d', 'in 30 days'], ['0', 'never (until removed)']].map(([v, t]) => h('option', { value: v, text: t })));
    const add = h('button', { type: 'button', class: 'btn main full', text: 'Add ban' });
    // A soft AKILL (% before the mask) lets people logged in to an account pass.
    const soft = h('input', { type: 'checkbox', id: 'ban-soft' });
    const softRow = h('label', { class: 'ck', for: 'ban-soft' }, soft, 'Logged-in people may pass');
    const banMask = () => { const v = mask.value.trim(); return v && type.value === 'akill' && soft.checked && !v.startsWith('%') ? '%' + v : v; };
    function typeChanged() {
      softRow.hidden = type.value !== 'akill';
      const k = kindOf(type.value);
      mask.placeholder = k.ph;
      maskLabel.textContent = k.field || 'Mask';
      maskHint.textContent = k.hint || '';
      maskHint.hidden = !k.hint;
      if (k.hint) mask.setAttribute('aria-describedby', 'ban-mask-hint'); else mask.removeAttribute('aria-describedby');
      life.value = k.life;
      add.textContent = k.kind === 'eline' ? 'Add exception' : k.kind === 'forbid' ? 'Add forbid' : 'Add ' + k.name;
      checkMask();
    }
    function checkMask() {
      clearTimeout(matchTimer);
      const v = banMask();
      mask.classList.remove('wrong');
      if (!v) { hit.className = 'hit'; hit.textContent = ''; return; }
      const seq = ++matchSeq;
      matchTimer = setTimeout(async () => {
        try {
          const d = await api('GET', '/api/ban-match?' + new URLSearchParams({ kind: type.value, mask: v }));
          if (seq !== matchSeq) return;
          const n = d.match.people, ex = type.value === 'eline', fb = type.value === 'forbid';
          hit.className = 'hit' + (n ? ' some' : '');
          put(hit, h('span', { class: 'dot ' + (n ? 'warn' : 'off') }),
            fb ? (n ? plural(n, 'person', 'people') + (v.startsWith('#') ? ' would be kicked out' : ' would be moved off the nick') : 'Nobody online uses it')
              : (ex ? 'Covers ' : 'Matches ') + plural(n, 'person', 'people') + ' online' + (d.match.mask !== v ? ' · saved as ' + d.match.mask : ''));
        } catch (e) {
          if (seq !== matchSeq) return;
          hit.className = 'hit bad';
          hit.textContent = e.message;
          mask.classList.add('wrong');
        }
      }, 300);
    }
    mask.addEventListener('input', checkMask);
    soft.addEventListener('change', checkMask);
    type.addEventListener('change', typeChanged);
    add.addEventListener('click', () => busy(add, async () => {
      if (!mask.value.trim()) { mask.classList.add('wrong'); mask.focus(); return; }
      try {
        const d = await api('POST', '/api/ban-add', { kind: type.value, mask: banMask(), reason: reason.value, life: life.value });
        toast([d.done.text]);
        freshMask = (d.done.text.match(/ on (\S+) /) || [])[1] || null;
        if (type.value !== kind) { kind = type.value; tiles.forEach(t => t.b.setAttribute('aria-pressed', t.k.kind === kind ? 'true' : 'false')); }
        mask.value = ''; reason.value = ''; soft.checked = false; checkMask();
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
          h('td', { class: 'mono' }, b.mask.startsWith('%') ? [b.mask.slice(1), ' ', h('span', { class: 'tag', text: 'soft', title: 'Logged-in people may pass' })] : b.mask),
          h('td', { text: b.reason }),
          h('td', { text: b.set_by }),
          h('td', { class: 'lbl', text: ends(b.expires), title: b.expires ? new Date(b.expires).toLocaleString('en-GB') : 'until removed' }),
          cell);
      }));
      freshMask = null;
    }

    const tileRow = h('div', { class: 'tiles5' }, tiles.map(t => t.b));
    tileRow.style.setProperty('--n', tiles.length); // CSSOM: the CSP allows no style attributes
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Bans' }),
        h('span', { class: 'sub', text: 'Network bans reach every linked server' }))),
      tileRow,
      h('div', { class: 'band21' },
        h('section', { class: 'card list-card' },
          h('div', { class: 'ch tools' }, title, listNote),
          h('div', { class: 'wrap' }, h('table', { class: 'bans' },
            h('thead', {}, h('tr', {}, h('th', { scope: 'col', text: 'Type' }), maskHead, ['Reason', 'Set by', 'Ends'].map(t => h('th', { scope: 'col', text: t })), h('th', { scope: 'col' }, h('span', { class: 'sr', text: 'Remove' })))),
            tbody))),
        h('section', { class: 'card' }, h('h2', { text: SHOWN.every(x => x.kind === 'forbid') ? 'Add a forbid' : 'Add a ban' }),
          h('label', { class: 'f', for: 'ban-type' }, 'Type', type),
          h('label', { class: 'f', for: 'ban-mask' }, maskLabel, mask, maskHint),
          softRow,
          hit,
          h('label', { class: 'f', for: 'ban-reason' }, 'Reason', reason),
          h('label', { class: 'f', for: 'ban-life' }, 'Ends', life),
          add)));
    typeChanged();
    load();
    const timer = setInterval(() => { if (!document.hidden && !el.querySelector('.rm')) bg(load); }, 30000);
    return { el, leave() { clearInterval(timer); } };
  }

  // ---------- Spam filter (after 1.0, flag k) ----------
  // Rules that catch spam as it is sent; the same list as /SPAMFILTER.
  const SF_CHECKS = [['c', 'Channel messages'], ['p', 'Private messages'], ['nN', 'Notices'], ['Pq', 'Part and quit'], ['t', 'Topics'], ['a', 'Away'], ['u', 'Nick and realname']];
  const SF_ACTIONS = [['warn', 'Warn opers only (let through)'], ['block', 'Block the message'], ['kill', 'Kill (disconnect)'], ['tempshun', 'Shun until they reconnect'],
    ['shun', 'Shun (timed)'], ['gline', 'AKILL (timed)'], ['gzline', 'ZLINE (timed)']];
  const SF_BANS = { shun: 1, gline: 1, gzline: 1 };
  const SF_TRY = [['c', 'Channel message'], ['p', 'Private message'], ['n', 'Notice'], ['P', 'Part or quit'], ['t', 'Topic'], ['a', 'Away'], ['u', 'Nick line']];
  function spamPage() {
    let data = null, freshID = 0, checkTimer = 0, checkSeq = 0, tryTimer = 0, trySeq = 0, unsafe = false;
    const tile = (label, what) => {
      const n = h('span', { class: 'num', text: '…' });
      return { el: h('div', { class: 'card tile' }, h('span', { class: 'lbl', text: label }), n, h('span', { class: 'lbl', text: what })), n };
    };
    const tFilters = tile('Filters', 'on every server'), tCaught = tile('Caught', 'last 24 hours'), tBlock = tile('Blocking', 'stop or ban'),
      tWarn = tile('Warn only', 'trying out, let through'), tExcept = tile('Except', 'channels never filtered');
    const tbody = h('tbody');

    // Add a filter
    const match = h('input', { class: 'in mono', id: 'sf-match', type: 'text', autocomplete: 'off', spellcheck: 'false', maxlength: '500', placeholder: '*free nitro*' });
    let type = 'simple';
    const tSimple = h('button', { type: 'button', 'aria-pressed': 'true', text: 'Simple', title: '* and ? as wildcards' });
    const tRegex = h('button', { type: 'button', 'aria-pressed': 'false', text: 'Expert regex', title: 'A regular expression (RE2)' });
    for (const [b, v] of [[tSimple, 'simple'], [tRegex, 'regex']]) b.addEventListener('click', () => {
      type = v; tSimple.setAttribute('aria-pressed', v === 'simple' ? 'true' : 'false'); tRegex.setAttribute('aria-pressed', v === 'regex' ? 'true' : 'false');
      match.placeholder = v === 'simple' ? '*free nitro*' : '^[a-z]{3}[0-9]{5}!'; check();
    });
    const boxes = SF_CHECKS.map(([letters, label], i) => {
      const cb = h('input', { type: 'checkbox', id: 'sf-c' + i });
      if (letters === 'c' || letters === 'p') cb.checked = true;
      cb.addEventListener('change', check);
      return { letters, cb, el: h('label', { for: 'sf-c' + i }, cb, label) };
    });
    const action = h('select', { class: 'in', id: 'sf-action' }, SF_ACTIONS.map(([v, t]) => h('option', { value: v, text: t })));
    const life = h('select', { class: 'in', id: 'sf-life' },
      [['1h', '1 hour'], ['1d', '1 day'], ['7d', '7 days'], ['30d', '30 days'], ['0', 'until removed']].map(([v, t]) => h('option', { value: v, text: t })));
    life.value = '1d';
    const lifeRow = h('label', { class: 'f', for: 'sf-life', hidden: true }, 'Ban length', life);
    const reason = h('input', { class: 'in', id: 'sf-reason', type: 'text', maxlength: '200', autocomplete: 'off', placeholder: 'Spam is not allowed here' });
    const safe = h('div', { class: 'sf-safe', role: 'status', hidden: true });
    const add = h('button', { type: 'button', class: 'btn main full', text: 'Add filter' });
    const targets = () => boxes.filter(b => b.cb.checked).map(b => b.letters).join('');
    const rule = force => ({ type, targets: targets(), action: action.value, life: SF_BANS[action.value] ? life.value : '', reason: reason.value.trim(), match: match.value.trim(), force: !!force });
    action.addEventListener('change', () => { lifeRow.hidden = !SF_BANS[action.value]; });
    function showSafe(kind, parts) {
      safe.hidden = false;
      safe.className = 'sf-safe ' + kind;
      put(safe, icon(kind === 'ok' ? 'check' : 'alert'), h('span', {}, ...parts));
    }
    function check() {
      clearTimeout(checkTimer);
      unsafe = false;
      add.textContent = 'Add filter';
      match.classList.remove('wrong');
      if (!match.value.trim()) { safe.hidden = true; return; }
      const seq = ++checkSeq;
      checkTimer = setTimeout(async () => {
        try {
          const d = (await api('POST', '/api/spam-check', rule())).spamCheck;
          if (seq !== checkSeq) return;
          if (d.problem) { showSafe('bad', [d.problem.charAt(0).toUpperCase() + d.problem.slice(1) + '.']); match.classList.add('wrong'); return; }
          if (d.unsafe && d.unsafe.length) {
            unsafe = true;
            add.textContent = 'Add anyway';
            showSafe('bad', [h('b', { text: 'It would also catch normal chat: ' }), d.unsafe.map(l => '"' + l + '"').join(' and '), '. Narrow the match, or add it anyway.']);
            return;
          }
          showSafe('ok', ['Tried on ' + fmt(d.lines) + ' normal chat lines: catches none of them.']);
        } catch (e) { if (seq === checkSeq) showSafe('bad', [e.message]); }
      }, 300);
    }
    match.addEventListener('input', check);
    add.addEventListener('click', () => busy(add, async () => {
      if (!match.value.trim()) { match.classList.add('wrong'); match.focus(); return; }
      if (!targets()) { showSafe('bad', ['Tick at least one place to check.']); return; }
      try {
        const d = await api('POST', '/api/spam-add', rule(unsafe));
        toast([d.done.text]);
        match.value = ''; reason.value = ''; safe.hidden = true; unsafe = false; add.textContent = 'Add filter';
        freshID = -1;
        await load();
      } catch (e) { showSafe('bad', [e.message]); }
    }));

    // Try a line
    const tryText = h('input', { class: 'in', id: 'sf-try', type: 'text', autocomplete: 'off', maxlength: '1000', placeholder: 'Paste a message to see which rule catches it' });
    const tryWhere = h('select', { class: 'in', id: 'sf-try-where', 'aria-label': 'Where it was sent' }, SF_TRY.map(([v, t]) => h('option', { value: v, text: t })));
    const tryRes = h('div', { class: 'sf-try-res', role: 'status' });
    function tryIt() {
      clearTimeout(tryTimer);
      const v = tryText.value;
      if (!v.trim()) { put(tryRes); return; }
      const seq = ++trySeq;
      tryTimer = setTimeout(async () => {
        try {
          const d = (await api('POST', '/api/spam-test', { targets: tryWhere.value, text: v })).spamTest;
          if (seq !== trySeq) return;
          if (!d.hits.length) { put(tryRes, h('span', { class: 'dot ok' }), 'Nothing would catch it.'); return; }
          const f = d.hits[0];
          put(tryRes, h('span', { class: 'dot bad' }), 'Caught by ', h('b', { class: 'mono', text: f.match }), ' → ' + f.does + '.',
            d.hits.length > 1 ? ' ' + plural(d.hits.length - 1, 'more rule', 'more rules') + ' would too.' : '', h('span', { class: 'lbl', text: ' Nothing is sent: this only tests.' }));
        } catch (e) { if (seq === trySeq) put(tryRes, h('span', { class: 'dot bad' }), e.message); }
      }, 250);
    }
    tryText.addEventListener('input', tryIt);
    tryWhere.addEventListener('change', tryIt);

    async function load() {
      try {
        data = (await api('GET', '/api/spam')).spam;
        draw();
        tryIt(); // the rules changed: "Try a line" answers for the new ones
      } catch (e) {
        put(tbody, h('tr', {}, h('td', { colspan: 5, class: 'empty', text: e.message })));
      }
    }
    function draw() {
      const rules = data.rules.slice().sort((a, b) => b.hits - a.hits || new Date(b.set_at) - new Date(a.set_at));
      tFilters.n.textContent = fmt(rules.length);
      tCaught.n.textContent = fmt(data.caught24h);
      tBlock.n.textContent = fmt(rules.filter(r => r.action !== 'warn').length);
      tWarn.n.textContent = fmt(rules.filter(r => r.action === 'warn').length);
      tExcept.n.textContent = fmt(data.except.length);
      tExcept.el.title = data.except.join(' ') || 'none ([spamfilter] except in beeircd.conf)';
      if (freshID === -1) freshID = rules.reduce((m, r) => Math.max(m, r.id), 0);
      if (!rules.length) {
        put(tbody, h('tr', {}, h('td', { colspan: 5, class: 'empty', text: 'No filters yet. Add one on the right; start with "Warn opers only" to try it out.' })));
        return;
      }
      put(tbody, ...rules.map(r => {
        const cell = h('td', { class: 'end-cell' });
        const trash = h('button', { type: 'button', class: 'x', 'aria-label': 'Remove the filter on ' + r.match, title: 'Remove' }, icon('trash'));
        cell.append(trash);
        trash.addEventListener('click', () => {
          const yes = h('button', { type: 'button', class: 'btn bad', text: 'Remove' });
          const no = h('button', { type: 'button', class: 'btn ghost', text: 'Keep' });
          no.addEventListener('click', () => put(cell, trash));
          yes.addEventListener('click', () => busy(yes, async () => {
            try {
              const d = await api('POST', '/api/spam-del', { id: r.id });
              toast([d.done.text]);
              load();
            } catch (e) { toastErr(e.message); put(cell, trash); }
          }));
          put(cell, h('span', { class: 'rm' }, yes, no));
          yes.focus();
        });
        const caught = h('td', { class: 'mono' }, fmt(r.hits), h('span', { class: 'sf-ago', text: r.hits ? ago(r.last) : 'never' }));
        return h('tr', { class: r.id === freshID ? 'fresh' : null, title: 'Set by ' + r.set_by },
          h('td', { class: 'mono' }, r.match, r.type === 'regex' ? h('span', { class: 'tag sf-exp', text: 'expert' }) : null,
            r.unusable ? h('span', { class: 'tag sus', text: 'not used here', title: 'This regex came from another server and does not work here' }) : null),
          h('td', {}, ...r.words.filter(Boolean).map(w => h('span', { class: 'tag', text: w }))),
          h('td', { class: r.action === 'warn' ? 'sf-warn' : null, text: r.does }),
          h('td', { class: 'sf-why', text: r.reason }),
          caught, cell);
      }));
      freshID = 0;
    }

    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Spam filter' }),
        h('span', { class: 'sub', text: 'Rules that catch spam as it is sent, on every linked server' }))),
      h('div', { class: 'tiles5' }, [tFilters, tCaught, tBlock, tWarn, tExcept].map(t => t.el)),
      h('div', { class: 'band21 sf-band' },
        h('div', { class: 'sf-col' },
          h('section', { class: 'card list-card' },
            h('div', { class: 'ch tools' }, h('h2', { text: 'Filters' }), h('span', { class: 'lbl', text: 'most caught first' })),
            h('div', { class: 'wrap' }, h('table', { class: 'bans sf-tbl' },
              h('thead', {}, h('tr', {}, ['Match', 'Checks', 'Action', 'Reason', 'Caught'].map(t => h('th', { scope: 'col', text: t })), h('th', { scope: 'col' }, h('span', { class: 'sr', text: 'Remove' })))),
              tbody))),
          h('section', { class: 'card' }, h('h2', { text: 'Try a line' }),
            h('div', { class: 'sf-try' }, tryText, tryWhere), tryRes)),
        h('section', { class: 'card' }, h('h2', { text: 'Add a filter' }),
          h('label', { class: 'f', for: 'sf-match' }, 'Match', match),
          h('div', { class: 'seg sf-seg', role: 'group', 'aria-label': 'Match type' }, tSimple, tRegex),
          h('fieldset', { class: 'sf-checks' }, h('legend', { class: 'f', text: 'Checks' }), boxes.map(b => b.el)),
          h('label', { class: 'f', for: 'sf-action' }, 'Action', action),
          lifeRow,
          h('label', { class: 'f', for: 'sf-reason' }, 'Reason', reason),
          safe, add)));
    load();
    const timer = setInterval(() => { if (!document.hidden && !el.querySelector('.rm')) bg(load); }, 30000);
    return { el, leave() { clearInterval(timer); clearTimeout(checkTimer); clearTimeout(tryTimer); } };
  }

  // ---------- Mail card (Settings, owner only) ----------
  const APP_PW = {
    gmail: ['Not your Gmail password: make one at ', 'https://myaccount.google.com/apppasswords', 'myaccount.google.com/apppasswords', ' (needs 2-Step Verification).'],
    outlook: ['Make an app password in ', 'https://account.microsoft.com/security', 'account.microsoft.com/security', '. Microsoft is turning off password sign-in for sending mail; if the test fails, use Gmail or Zoho.'],
    zoho: ['Make an app password in ', 'https://accounts.zoho.com/home#security/app_password', 'Zoho security settings', '. EU accounts: pick My own mail server with smtp.zoho.eu.'],
  };
  function mailCard() {
    let d = null, prov = 'off', checks = null, testRes = null;
    const sub = h('span', { class: 'lbl', text: 'Password resets, memo mails and account confirmation · owner only' });
    const pill = h('span', { class: 'pill', hidden: true });
    const picks = h('div', { class: 'ml-picks', role: 'group', 'aria-label': 'Send with' });
    const fieldsBox = h('div', { class: 'grid2 ml-fields' });
    const inp = (id, attrs) => h('input', Object.assign({ class: 'in', id, type: 'text', autocomplete: 'off', spellcheck: 'false' }, attrs));
    const from = inp('ml-from', { type: 'email', maxlength: 254, placeholder: 'services@example.org' });
    const name = inp('ml-name', { maxlength: 60, placeholder: 'BeeNet Services' });
    const server = inp('ml-server', { maxlength: 200, placeholder: 'mail.example.org:587' });
    const user = inp('ml-user', { maxlength: 200 });
    const pw = inp('ml-pw', { type: 'password', autocomplete: 'new-password', maxlength: 200 });
    const to = inp('ml-to', { type: 'email', maxlength: 254, 'aria-label': 'Send the test mail to', placeholder: 'you@example.org' });
    const saveBtn = h('button', { type: 'button', class: 'btn main', text: 'Save' });
    const testBtn = h('button', { type: 'button', class: 'btn' }, icon('send'), 'Send test mail');
    const testBox = h('div', { class: 'ml-res', hidden: true });
    const dnsHead = h('div', { class: 'ml-sect', hidden: true });
    const dnsBox = h('div', { class: 'ml-dns', hidden: true });
    const regSeg = h('div', { class: 'seg ml-seg', role: 'group', 'aria-label': 'New NickServ accounts' });
    const regHint = h('span', { class: 'hint' });
    const actRow = h('div', { class: 'ml-act' }, saveBtn, to, testBtn);
    const regBox = h('div', { class: 'ml-reg' }, h('div', { class: 'ml-sect' }, icon('people'), 'New NickServ accounts'), h('div', { class: 'ml-regrow' }, regSeg, regHint));
    const presetOf = id => (d.presets || []).find(p => p.id === id);
    const srvOf = p => (prov === d.provider && d.server ? d.server : p.server);

    function form() {
      return { provider: prov, from: from.value.trim(), name: name.value.trim(), server: server.value.trim(), user: user.value.trim(), password: pw.value };
    }
    function field(label, input, hint) {
      return h('label', { class: 'f', for: input.id }, label, input, hint ? h('span', { class: 'hint' }, hint) : null);
    }
    function drawPicks() {
      const opts = (d.presets || []).map(p => [p.id, p.name, p.server]);
      opts.push(['own', 'My own mail server', 'host, port, login']);
      if (d.provider === 'sendmail') opts.push(['sendmail', 'This machine', 'sendmail program']);
      opts.push(['off', 'Off', 'no mail']);
      put(picks, ...opts.map(([id, label, small]) => {
        const b = h('button', { type: 'button', class: 'ml-pk', 'aria-pressed': prov === id ? 'true' : 'false' }, h('b', { text: label }), h('small', { text: small }));
        b.addEventListener('click', () => { prov = id; testRes = null; drawForm(); });
        return b;
      }));
    }
    function drawForm() {
      drawPicks();
      const p = presetOf(prov);
      pw.placeholder = d.hasPassword && prov === d.provider ? 'saved · type to change' : '';
      if (prov === 'off') {
        put(fieldsBox, h('p', { class: 'note ml-wide', text: 'Services send no mail: no password resets by email, no memo mails, no confirmation by email.' }));
      } else if (p) {
        const a = APP_PW[p.id];
        put(fieldsBox,
          field('Your ' + p.name.split(' /')[0] + ' address', from, 'Mails go out from this address.'),
          field('App password', pw, a ? [a[0], h('a', { href: a[1], target: '_blank', rel: 'noopener noreferrer', text: a[2] }), a[3]] : null),
          field('Name people see', name),
          h('div', { class: 'f' }, 'Connection', h('span', { class: 'in ro', text: srvOf(p) + ' · ' + (/:465$/.test(srvOf(p)) ? 'TLS' : 'STARTTLS') + ' · filled in for you' })));
      } else if (prov === 'sendmail') {
        put(fieldsBox, field('Send from', from), field('Name people see', name),
          h('p', { class: 'note ml-wide', text: "Sent with this machine's sendmail program (sendmail_path in beeircd.conf)." }));
      } else {
        put(fieldsBox,
          field('Mail server', server, 'Like mail.example.org:587 (STARTTLS) or :465 (TLS).'),
          field('Send from', from),
          field('Login', user, 'Often the same as the address. Empty: no login.'),
          field('Password', pw),
          field('Name people see', name));
      }
      actRow.hidden = prov === 'off' && d.provider === 'off';
      testBtn.hidden = to.hidden = prov === 'off';
      drawTest();
    }
    function line(ok, ...text) {
      return h('div', { class: 'ml-line ' + (ok ? 'ok' : 'bad') }, icon(ok ? 'check' : 'x'), h('span', {}, text));
    }
    function drawTest() {
      testBox.hidden = !testRes;
      if (testRes) {
        const t = testRes;
        put(testBox, t.ok
          ? line(true, 'Sent to ' + t.to + ' in ' + (t.took / 1000).toFixed(1) + ' s. Look in your inbox; if it landed in spam, mark it "not spam" once.')
          : line(false, h('b', { text: t.explain }), h('span', { class: 'ml-raw', text: ' (' + t.raw + ')' })));
      }
      const c = checks;
      dnsHead.hidden = dnsBox.hidden = !c || !c.checks || !c.checks.length || prov === 'off';
      if (dnsHead.hidden) return;
      put(dnsHead, icon('lock'), 'Will it land in the inbox?', h('span', { class: 'lbl', text: 'checks ' + c.domain }));
      put(dnsBox, ...c.checks.map(k => {
        const fix = k.fix ? h('code', { text: k.fix }) : null;
        const copy = k.fix ? h('button', { type: 'button', class: 'btn sm', 'aria-label': 'Copy the ' + k.name + ' record' }, 'Copy') : h('span');
        if (k.fix) copy.addEventListener('click', () => {
          navigator.clipboard.writeText(k.fix).then(() => toast(['Copied the ', h('b', { text: k.name }), ' record'])).catch(() => toastErr("Couldn't copy; select the text instead."));
        });
        return h('div', { class: 'ml-dr ' + k.state }, icon(k.state === 'ok' ? 'check' : k.state === 'warn' ? 'alert' : 'x'),
          h('b', { text: k.name }), h('span', { class: 'why' }, k.text, fix), copy);
      }));
      const bad = c.checks.filter(k => k.state === 'bad').length;
      if (bad) dnsBox.append(line(false, 'Gmail and Outlook often put mails in spam while ' + (bad === 1 ? 'this row is' : 'these rows are') + ' red. Fix ' + (bad === 1 ? 'it' : 'them') + ', or send with Gmail.'));
    }
    function drawReg() {
      const can = !!d.tested || d.registration === 'mail';
      put(regSeg, ...[['none', 'Ready at once'], ['mail', 'Confirm by email'], ['admin', 'An oper confirms']].map(([v, label]) => {
        const lock = v === 'mail' && !can;
        const b = h('button', { type: 'button', 'aria-pressed': d.registration === v ? 'true' : 'false', text: label, disabled: lock, title: lock ? 'Send a test mail that goes through first' : null });
        b.addEventListener('click', async () => {
          if (d.registration === v) return;
          await busy(b, async () => {
            try {
              d = (await api('POST', '/api/mail-reg', { value: v })).mail;
              toast(['New NickServ accounts: ', h('b', { text: label })]);
            } catch (e) { toastErr(e.message); }
          });
          drawReg();
        });
        return b;
      }));
      put(regHint, !can ? 'Confirm by email opens once a test mail goes through. ' : null,
        d.registration === 'none' ? 'New accounts can register channels at once.' :
          ['Unconfirmed accounts go after ', h('b', { text: d.expire }), ' · they can identify, not register channels']);
    }
    function drawPill() {
      pill.hidden = false;
      const [cls, dot, text] = d.provider === 'off' ? ['pill off', 'off', 'mail off']
        : d.tested ? ['pill', 'ok', 'sending works · tested ' + since(d.tested * 1000) + ' ago'] : ['pill warn', 'warn', 'not tested yet'];
      pill.className = cls;
      put(pill, h('span', { class: 'dot ' + dot }), text);
    }
    function fill() {
      prov = d.provider;
      from.value = d.from || ''; name.value = d.name || ''; server.value = d.server || ''; user.value = d.user || ''; pw.value = '';
      if (!to.value) to.value = d.from || '';
      drawForm(); drawReg(); drawPill();
    }
    async function loadDNS() {
      if (d.provider === 'off') { checks = null; drawTest(); return; }
      try { checks = (await api('GET', '/api/mail-dns')).mailTest; } catch (e) { checks = null; }
      drawTest();
    }
    async function load() {
      try {
        d = (await api('GET', '/api/mail')).mail;
        fill();
        loadDNS();
      } catch (e) { put(fieldsBox, h('p', { class: 'empty ml-wide', text: e.message })); }
    }
    saveBtn.addEventListener('click', () => busy(saveBtn, async () => {
      try {
        d = (await api('POST', '/api/mail-save', { mail: form() })).mail;
        toast([d.provider === 'off' ? 'Mail is off' : d.tested ? 'Mail saved · tested' : 'Mail saved · send a test mail next']);
        fill();
        loadDNS();
      } catch (e) { toastErr('Mail not saved: ' + e.message); }
    }));
    testBtn.addEventListener('click', () => busy(testBtn, async () => {
      try {
        testRes = (await api('POST', '/api/mail-test', { mail: form(), to: to.value.trim() })).mailTest;
        checks = testRes;
        drawTest();
        d = Object.assign((await api('GET', '/api/mail')).mail, {});
        drawReg(); drawPill();
      } catch (e) { toastErr(e.message); }
    }));
    to.addEventListener('keydown', e => { if (e.key === 'Enter') testBtn.click(); });
    const el = h('section', { class: 'card ml-card' },
      h('div', { class: 'tools ml-head' }, h('div', { class: 'head-text' }, h('h2', { text: 'Mail' }), sub), pill),
      h('div', { class: 'ml-sect', text: 'Send with' }), picks, fieldsBox, actRow, testBox, dnsHead, dnsBox, regBox);
    load();
    return { el };
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
      FLAGS.map(([f]) => h('span', { class: 'c' + (all.includes(f) ? ' f-' + f : ' no'), text: f, 'aria-hidden': 'true' })));
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
          h('td', {}, btn, r.reach === 'network' ? [' ', h('span', { class: 'tag', text: 'network' })] : null,
            r.disabled ? [' ', h('span', { class: 'tag', text: 'disabled' })] : null),
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
          h('p', { class: 'note', text: 'Title, WHOIS lines and vhost: the owner sets them on IRC with /as oper ' + r.account + ' set title|swhois|vhost <text>.' }),
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
      const ownerView = !!data.owner_view;
      const reachNow = r.reach === 'network' ? 'network' : 'home';
      const reachRadios = {};
      const reachField = h('fieldset', { class: 'f plain' }, h('legend', { class: 'lbl', text: 'Reach' }));
      if (ownerView) {
        reachField.append(h('div', { class: 'picks' }, [['home', 'Home server only'], ['network', 'Whole network']].map(([v, t]) => {
          const rb = h('input', { type: 'radio', name: 'op-reach', value: v });
          rb.checked = v === reachNow;
          reachRadios[v] = rb;
          return h('label', { class: 'pk' }, rb, ' ' + t);
        })),
        h('span', { class: 'note', text: 'Whole network: their admin commands work on every server that takes outside admins.' }));
      } else {
        reachField.append(h('p', { text: reachNow === 'network' ? 'Whole network' : 'Home server only' }),
          h('span', { class: 'note', text: 'Only the network owner can change this.' }));
      }
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
          const body = { name, class: cls.value, flags: own, hosts: hosts.value, password: pw.value, new: adding };
          if (ownerView) body.reach = reachRadios.network.checked ? 'network' : 'home';
          const d = await api('POST', '/api/oper-save', body);
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
        reachField,
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
    const outside = h('section', { class: 'card' });
    let outsideBusy = false; // a click is saving: don't redraw the card under it
    const body = h('div', { class: 'band21' }, left, h('div', { class: 'stack' }, form, mine, outside));
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
      if (!outsideBusy) drawOutside();
      if (!form.dataset.dirty) drawForm();
    }

    // Admins from other servers: [link] outside_admins. The owner flips it;
    // everyone else just reads it.
    function drawOutside() {
      const cur = data.outside_admins === 'none' ? 'none' : 'network';
      const note = cur === 'none'
        ? 'Only this server\'s own admins may run AdminServ here.'
        : 'Admins whose reach is network (set by the network owner) may run AdminServ here. They can never change this switch, the link blocks or the config admin.';
      const head = h('h2', { text: 'Admins from other servers' });
      if (!me.owner) {
        put(outside, head, h('p', { class: 'lbl', text: 'Admins from other servers: ' + (cur === 'none' ? 'kept out' : 'let in') }), h('p', { class: 'note', text: note }));
        return;
      }
      const seg = h('div', { class: 'seg ml-seg', role: 'group', 'aria-label': 'Admins from other servers' });
      put(seg, ...[['network', 'Let in network reach'], ['none', 'Keep out']].map(([v, label]) => {
        const b = h('button', { type: 'button', 'aria-pressed': cur === v ? 'true' : 'false', text: label });
        b.addEventListener('click', async () => {
          if (cur === v || outsideBusy) return;
          outsideBusy = true;
          await busy(b, async () => {
            try {
              await api('POST', '/api/set', { name: 'outside-admins', value: v });
              toast(['Admins from other servers: ', h('b', { text: v === 'none' ? 'kept out' : 'let in' })]);
            } catch (e) { toastErr(e.message); }
          });
          outsideBusy = false;
          await load();
        });
        return b;
      }));
      put(outside, head, seg, h('p', { class: 'note', text: note }));
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
    timer = setInterval(() => { if (!document.hidden && !el.querySelector('.split-ask')) bg(load); }, 10000);
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
    ]],
  ];
  // The events the services log shows (log-events): name, label, hint.
  const LOG_EVENTS = [
    ['Shown in the log', [
      ['connect', 'Connects', 'someone joins the network'], ['quit', 'Quits', 'someone leaves'],
      ['nick', 'Nick changes', ''], ['oper', 'Opers', 'oper up and down'],
      ['kill', 'Kills', ''], ['server', 'Servers', 'links and splits'],
      ['bans', 'Ban hits', 'AKILL, Q-line, spamfilter…'], ['other', 'Other', 'DEFCON, session limits'],
    ]],
    ['Busy, off by default', [
      ['umode', 'User modes', ''], ['host', 'Host changes', ''], ['channel', 'Channels', 'joins, parts, new, empty'],
    ]],
  ];
  // The names a log-events value turns on ("*" all, "off" none).
  const eventsOn = (s) => s.value === '*' ? (s.choices || []).slice() : s.value === 'off' ? [] : s.value.split(',');
  const BOT_TEXT = {
    NickServ: 'Registers nicks, logs people in', ChanServ: 'Registers and guards channels',
    MemoServ: 'Messages for people who are away', OperServ: 'Oper tools and network bans',
    HostServ: 'Virtual hosts', AdminServ: 'Server settings and opers', BotServ: 'Channel bots', Global: 'Notices to everyone',
    HelpServ: 'Help with the other bots',
  };
  // toSecs reads 90s / 30m / 12h / 30d / 2w (a bare number is seconds); -1 when it isn't a time.
  function toSecs(v) {
    const m = /^(\d+)\s*([smhdw]?)$/i.exec(v.trim());
    return m ? Number(m[1]) * { '': 1, s: 1, m: 60, h: 3600, d: 86400, w: 604800 }[m[2].toLowerCase()] : -1;
  }
  function secs(d) { return d >= 86400 && d % 86400 === 0 ? d / 86400 + 'd' : d >= 3600 && d % 3600 === 0 ? d / 3600 + 'h' : d >= 60 && d % 60 === 0 ? d / 60 + 'm' : d + 's'; }
  // ---------- Chat: BeeRoom, signed in as you ----------
  // The panel asks the server for a one-time key (a minute, once) and opens
  // BeeRoom with it as the SASL password; BeeRoom connects at once and
  // drops the key from its address.
  async function openChat() {
    const retry = h('button', { type: 'button', class: 'btn', text: 'Try again' });
    retry.addEventListener('click', openChat);
    put(chatHost, h('p', { class: 'empty chat-wait', text: 'Signing you in to BeeRoom…' }));
    try {
      const d = (await api('POST', '/api/chat-key')).chat;
      const user = d.account.toLowerCase().replace(/[^a-z0-9]/g, '').slice(0, 10) || 'beeroom';
      const q = new URLSearchParams({ autoconnect: '1', nick: d.account, username: user, sasl: 'plain', saslAccount: d.account, saslPassword: d.key });
      put(chatHost, h('iframe', { class: 'chat-frame', src: d.url.replace(/\/+$/, '') + '/?' + q.toString(), title: 'BeeRoom chat', referrerpolicy: 'no-referrer' }));
    } catch (e) {
      put(chatHost, h('div', { class: 'content' }, h('section', { class: 'card' }, h('h2', { text: 'Chat' }),
        h('p', { class: 'note', text: e.message }), h('div', { class: 'row-btns' }, retry))));
    }
  }
  function chatPage() {
    mainEl.hidden = true;
    chatHost.hidden = false;
    if (!chatHost.firstChild) openChat();
    return { el: h('div'), leave() { chatHost.hidden = true; mainEl.hidden = false; } };
  }

  // ---------- Home and Memos (members and opers, their own account) ----------
  // Home sums up and links; every detail stays on its own page.
  const hmHead = (ic, title, end) => h('h2', { class: 'hm-h' }, h('span', { class: 'sico' }, icon(ic)), title, end ? h('span', { class: 'hm-end', text: end }) : null);
  const ago = t => {
    const s = (Date.now() - new Date(t).getTime()) / 1000;
    return s < 60 ? 'just now' : s < 3600 ? since(t) + ' ago' : s < 86400 ? Math.floor(s / 3600) + ' h ago' : Math.floor(s / 86400) + ' d ago';
  };
  const NO_MEMOS = nick => 'No memos. Others leave you one with /msg MemoServ SEND ' + nick + ' and a message.';

  // The Memos menu item shows how many are unread.
  let unreadMemos = 0, memoTimer = 0;
  function drawCount() {
    const a = navEl && navEl.querySelector('a[data-page="memos"]');
    if (!a) return;
    let b = a.querySelector('.ncount');
    if (!unreadMemos) { if (b) b.remove(); return; }
    if (!b) { b = h('span', { class: 'ncount' }); a.append(b); }
    b.textContent = unreadMemos;
  }

  // An oper with no oper password yet signs in with their NickServ
  // password and gets the member pages: Home says why and how to fix it.
  const operHint = () => {
    const how = me.member && {
      owner: ['On IRC, identified and connected from your [services] admin_hosts, type ', h('code', { text: '/oper ' + me.account + ' <new password>' }), ' once.'],
      'owner-conf': ['Make one with ', h('code', { text: 'beeircd mkpasswd' }), ', put it in beeircd.conf as [services] admin_password, and rehash.'],
      oper: ['An admin sets it with ', h('code', { text: '/as oper ' + me.account + ' set password <password>' }), '.'],
    }[me.noOperPass];
    if (!how) return null;
    return h('section', { class: 'hm-hint', role: 'note' },
      h('b', { text: me.noOperPass === 'oper' ? "You're an oper, but have no oper password yet" : "You're the owner, but have no oper password yet" }),
      h('p', {}, "So you're seeing the member pages. ", how, ' Then sign out and sign in here with that password for the oper pages.'));
  };

  function homePage() {
    const title = h('h1', { text: me.account });
    const facts = h('div', { class: 'hm-facts' });
    const tiles = h('div', { class: 'tiles4' });
    const left = h('div', { class: 'hm-col' }, h('p', { class: 'empty', text: 'Loading…' }));
    const right = h('div', { class: 'hm-col' });
    const net = h('div', { class: 'hm-net' });
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head', 'data-deco': '1' }, h('div', { class: 'head-text' }, h('div', { class: 'eyebrow', text: 'Welcome back' }), title, facts)),
      operHint(), tiles, h('div', { class: 'hm-cols' }, left, right), net);
    const tile = (href, ic, label, n, foot, fresh) => h('a', { href, class: 'card tight' + (fresh ? ' hm-new' : ''), 'data-deco': '1' },
      h('div', { class: 'slab' }, h('span', { text: label }), h('span', { class: 'sico' }, icon(ic))),
      h('span', { class: 'num', text: fmt(n) }), h('span', { class: 'foot-n', text: foot }));
    const go = (pg, label) => h('a', { class: 'btn', href: '#/' + pg }, label, icon('arrow'));
    api('GET', '/api/my-home').then(d => {
      const m = d.home;
      title.textContent = m.name;
      const online = m.conns.length;
      put(facts,
        h('span', { class: 'role' + (online ? ' ok' : ''), text: online ? 'online · ' + plural(online, 'connection', 'connections') : 'not on IRC now' }),
        m.vhost ? h('span', { class: 'role', text: 'vhost ' + m.vhost }) : null,
        h('span', { class: 'role', text: 'registered ' + day(m.registered) }),
        m.nicks.length > 1 ? h('span', { class: 'role', text: 'nicks: ' + m.nicks.join(', ') }) : null);
      const founded = m.channels.filter(c => c.founder).length;
      put(tiles,
        tile('#/memos', 'mail', 'New memos', m.unread, m.kept + ' of ' + m.memoLimit + ' kept', m.unread > 0),
        tile('#/mychannels', 'hash', 'My channels', m.channels.length, founded ? 'founder of ' + founded : 'on access lists'),
        tile('#/mychannels', 'list', 'On my lists', m.onMyLists, founded ? 'people in channels you founded' : 'you founded none yet'),
        tile(me.chat ? '#/chat' : '#/home', 'monitor', 'Connected', online, online ? (m.conns.some(c => c.web) ? 'web chat and IRC' : 'IRC') : 'nowhere right now'));
      unreadMemos = m.unread;
      drawCount();

      const memoRows = m.memos.length ? m.memos.map(x => h('div', { class: 'hm-memo' + (x.read ? ' read' : '') },
        h('span', { class: 'dot ' + (x.read ? 'off' : 'warn') }), h('b', { text: x.from }), h('span', { class: 'tx', text: x.text }), h('span', { class: 'tm', text: when(x.sent) })))
        : [h('p', { class: 'empty', text: NO_MEMOS(m.name) })];
      put(left,
        h('section', { class: 'card' }, hmHead('mail', 'Memos', 'newest first'), memoRows,
          h('div', { class: 'hm-foot' }, go('memos', 'Open memos'))),
        m.needs.length ? h('section', { class: 'card hm-needs' }, hmHead('alert', 'Needs you'),
          m.needs.map(n => h('div', { class: 'row' },
            h('span', {}, h('span', {}, h('b', { text: n.text }), h('br'), h('span', { class: 'hint', text: n.hint }))),
            n.page.startsWith('chan/') ? h('a', { class: 'btn', href: chanHref(n.page.slice(5)) }, 'Set successor', icon('arrow'))
              : n.page ? go(n.page, (pages.find(p => p.id === n.page) || {}).label || 'Open') : null))) : null);

      put(right,
        h('section', { class: 'card' }, hmHead('hash', 'My channels now'),
          m.channels.length ? m.channels.map(c => h('div', { class: 'row' },
            h('span', {}, h('a', { class: 'mono cm-link', href: chanHref(c.name), text: c.name }), h('span', { class: 'role' + (c.founder ? ' on' : ''), text: c.level })),
            h('span', { class: 'end', text: [c.users + ' here', c.status ? 'you have ' + c.status : '', c.topicBy ? 'topic by ' + c.topicBy + ' ' + ago(c.topicAt) : ''].filter(Boolean).join(' · ') })))
            : h('p', { class: 'empty', text: 'No channels yet. Register one on IRC: /msg ChanServ REGISTER #channel' })),
        m.access.length ? h('section', { class: 'card' }, hmHead('list', 'Access lists', 'newest additions'),
          m.access.map(a => h('div', { class: 'row' },
            h('span', {}, h('span', {}, a.level === 'AKICK'
              ? [h('b', { class: 'mono', text: a.who }), ' on AKICK in ' + a.channel]
              : [h('b', { text: a.who === m.name ? 'You' : a.who }), (a.who === m.name ? ' were' : '') + ' added as ', h('b', { text: a.level }), ' in ' + a.channel],
            a.by ? ' by ' + (a.by === m.name ? 'you' : a.by) : '')),
            h('span', { class: 'end mono', text: a.at ? ago(a.at) : '' })))) : null,
        h('section', { class: 'card' }, hmHead('monitor', 'Where you are connected'),
          online ? m.conns.map(c => h('div', { class: 'row' },
            h('span', {}, icon(c.web ? 'globe' : 'terminal'), h('span', {}, h('b', { text: c.nick }), h('br'),
              h('span', { class: 'hint', text: (c.web ? 'web chat' : 'IRC client') + ' · since ' + when(c.since) + (c.away ? ' · away' : '') })))))
            : h('p', { class: 'empty', text: 'You are not on IRC right now.' })));
      put(net, h('span', { text: 'NETWORK' }), h('span', {}, h('b', { text: fmt(m.users) }), ' online'),
        h('span', {}, h('b', { text: fmt(m.chanCount) }), m.chanCount === 1 ? ' channel' : ' channels'), h('span', {}, h('b', { text: fmt(m.servers) }), m.servers === 1 ? ' server' : ' servers'));
    }).catch(e => put(left, h('p', { class: 'empty', text: e.message })));
    return { el };
  }

  function memosPage() {
    const list = h('div', {}, h('p', { class: 'empty', text: 'Loading…' }));
    const count = h('span', { class: 'hm-end' });
    const to = h('input', { class: 'in', id: 'mm-to', type: 'text', maxlength: '64', autocomplete: 'off', spellcheck: 'false', placeholder: 'nick or #channel' });
    const text = h('textarea', { class: 'in', id: 'mm-text', maxlength: '300', placeholder: 'Your message' });
    const left = h('span', { class: 'hint', text: 'Up to 300 characters.' });
    text.addEventListener('input', () => { left.textContent = (300 - text.value.length) + ' characters left'; });
    const send = h('button', { type: 'submit', class: 'btn main' }, icon('send'), 'Send');
    const form = h('form', { class: 'mm-form' },
      h('label', { class: 'f', for: 'mm-to' }, 'To', to),
      h('label', { class: 'f', for: 'mm-text' }, 'Message', text), left,
      h('div', { class: 'row-btns' }, send));
    form.addEventListener('submit', e => {
      e.preventDefault();
      if (!to.value.trim() || !text.value.trim()) { toastErr('Give a nick (or #channel) and a message.'); return; }
      busy(send, async () => {
        try {
          const d = await api('POST', '/api/memo-send', { to: to.value.trim(), text: text.value.trim() });
          toast([d.done.text]);
          text.value = '';
          left.textContent = 'Up to 300 characters.';
        } catch (err) { toastErr(err.message); }
      });
    });
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Memos' }),
        h('span', { class: 'sub', text: 'Your MemoServ box: the same memos as /msg MemoServ LIST on IRC.' }))),
      h('div', { class: 'band21' },
        h('section', { class: 'card' }, h('h2', { class: 'hm-h' }, h('span', { class: 'sico' }, icon('mail')), 'Inbox', count), list),
        h('section', { class: 'card' }, hmHead('send', 'Send a memo'), form)));
    let kept = 0, limit = 0;
    const drawHead = () => { count.textContent = (unreadMemos ? unreadMemos + ' new · ' : '') + kept + ' of ' + limit + ' kept'; drawCount(); };
    async function load() {
      let d;
      try { d = (await api('GET', '/api/my-memos')).memos; } catch (e) { put(list, h('p', { class: 'empty', text: e.message })); return; }
      const memos = d.memos.slice().reverse(); // newest first
      kept = memos.length;
      limit = d.limit;
      unreadMemos = memos.filter(x => !x.read).length;
      drawHead();
      put(list, memos.length ? memos.map(row) : h('p', { class: 'empty', text: NO_MEMOS(me.account) }));
    }
    function row(x) {
      const body = h('div', { class: 'mm-body', hidden: true });
      const dot = h('span', { class: 'dot ' + (x.read ? 'off' : 'warn') });
      const btn = h('button', { type: 'button', class: 'mm-head', 'aria-expanded': 'false' },
        dot, h('b', { text: x.from }), h('span', { class: 'tx', text: x.text }), h('span', { class: 'tm', text: when(x.sent) }));
      const r = h('div', { class: 'mm-row' + (x.read ? ' read' : '') }, btn, body);
      const del = h('button', { type: 'button', class: 'btn bad' }, icon('trash'), 'Delete');
      del.addEventListener('click', () => busy(del, async () => {
        try {
          await api('POST', '/api/memo-del', { id: x.id });
          toast(['Memo from ' + x.from + ' deleted.']);
          load();
        } catch (e) { toastErr(e.message); }
      }));
      const reply = h('button', { type: 'button', class: 'btn' }, icon('send'), 'Reply');
      reply.addEventListener('click', () => { to.value = x.from; text.focus(); });
      put(body, h('p', { text: x.text }), h('div', { class: 'row-btns' }, reply, del));
      btn.addEventListener('click', async () => {
        const open = body.hidden;
        body.hidden = !open;
        r.classList.toggle('open', open);
        btn.setAttribute('aria-expanded', String(open));
        if (!open || x.read) return;
        try {
          await api('POST', '/api/memo-read', { id: x.id });
          x.read = true;
          r.classList.add('read');
          dot.className = 'dot off';
          unreadMemos = Math.max(0, unreadMemos - 1);
          drawHead();
        } catch (e) { toastErr(e.message); }
      });
      return r;
    }
    load();
    return { el, newMemos: load };
  }


  // ---------- Channel manager: one channel, by your ChanServ level ----------
  // Every change is the real ChanServ command (ACCESS, AKICK, SET, TOPIC,
  // DROP) with ChanServ's own answer. Tabs you can't use aren't shown; the
  // server checks again on every click.
  const chanHref = name => '#/chan/' + encodeURIComponent(name);
  const LEVELS = ['VOP', 'HOP', 'AOP', 'SOP', 'QOP'];
  const TOGGLES = [
    ['KEEPTOPIC', 'Keep the topic', 'It comes back when the channel reopens.'],
    ['TOPICLOCK', 'Lock the topic', 'Only those with the TOPIC privilege can change it.'],
    ['SECUREOPS', 'Secure ops', 'Only people on the access list may hold @.'],
    ['RESTRICTED', 'Members only', 'People not on the access list are kicked.'],
    ['PEACE', 'Peace', 'Nobody can kick, ban or deop someone at their level or above.'],
    ['PRIVATE', 'Private', 'Hidden from ChanServ LIST.'],
    ['SIGNKICK', 'Signed kicks', 'ChanServ kicks say who asked for them.'],
    ['PERSIST', 'Stay open', 'ChanServ keeps the channel open when everyone leaves.'],
    ['KEEPMODES', 'Keep modes', 'The modes come back when the channel reopens.'],
    ['SECURE', 'Secure', 'Access only for people who identified, not just recognised.'],
  ];
  const QUICK = ['KEEPTOPIC', 'TOPICLOCK', 'SECUREOPS', 'RESTRICTED'];
  function chanPage() {
    const name = decodeURIComponent((location.hash.match(/^#\/chan\/(.+)$/) || [])[1] || '');
    let tab = 'access', d = null;
    const title = h('h1', { text: name });
    const chips = h('div', { class: 'hm-facts' });
    const tabs = h('div', { class: 'cm-tabs', role: 'tablist' });
    const body = h('div', { class: 'main-stack' }, h('p', { class: 'empty', text: 'Loading…' }));
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head', 'data-deco': '1' }, h('div', { class: 'head-text' }, h('div', { class: 'eyebrow', text: 'ChanServ' }), title, chips),
        me.chat ? h('div', { class: 'head-btns' }, h('a', { class: 'btn', href: '#/chat' }, icon('message'), 'Chat')) : null),
      tabs, body);

    // run sends one ChanServ change and reloads the page on success.
    async function run(btn, kind, value, mask, reason) {
      const go = async () => {
        try {
          const r = await api('POST', '/api/chan-do', { name: d.name, kind, value: value || '', mask: mask || '', reason: reason || '' });
          toast([r.done.text]);
          await load();
          return true;
        } catch (e) { toastErr(e.message); return false; }
      };
      return btn ? busy(btn, go) : go();
    }
    const ago2 = t => t && new Date(t).getFullYear() > 1971 ? ago(t) : '';

    function peopleTab() {
      return h('section', { class: 'card' }, hmHead('people', 'People here now', d.users + ' here'),
        d.people.length ? h('div', { class: 'chips' }, d.people.map(p => h('span', { class: 'chip2' + (/^[~&@%]/.test(p.prefix) ? ' op' : p.prefix === '+' ? ' v' : ''), text: (p.prefix || '') + p.nick + (p.away ? ' (away)' : '') })))
          : h('p', { class: 'empty', text: 'Nobody is in ' + d.name + ' right now.' }));
    }

    // levelPick offers only the levels ChanServ lets you give (below your own).
    function levelPick(value, id) {
      const give = d.give || LEVELS;
      const sel = h('select', { class: 'in cm-lvl', id, 'aria-label': 'Level' }, give.map(l => h('option', { value: l, text: l })));
      if (value && !give.includes(value)) sel.prepend(h('option', { value, text: value }));
      sel.value = value || give[0];
      return sel;
    }

    function accessTab() {
      const can = d.can.accessChange && (d.give || []).length > 0;
      const rows = (d.access || []).map(a => {
        const mine = can && !a.locked; // at or above your level: ChanServ won't let you touch it
        const lv = mine ? levelPick(a.level) : h('span', { class: 'role', text: a.level });
        if (mine) lv.addEventListener('change', async () => { lv.disabled = true; if (!await run(null, 'ACCESS', 'ADD', a.nick, lv.value)) lv.value = a.level; lv.disabled = false; });
        const del = mine ? h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Remove ' + a.nick, title: 'Remove ' + a.nick }, icon('x')) : null;
        if (del) del.addEventListener('click', () => run(del, 'ACCESS', 'DEL', a.nick));
        return h('tr', {}, h('td', {}, h('b', { text: a.nick }), a.online ? h('span', { class: 'dot cm-on', title: 'online now' }) : null), h('td', {}, lv),
          h('td', { class: 'mono', text: a.by || '' }), h('td', { class: 'mono', text: ago2(a.at) }), h('td', { class: 'cm-x' }, del));
      });
      const list = h('section', { class: 'card' }, hmHead('list', 'Access list', can ? 'who gets what · ChanServ ACCESS' : 'you can see it, not change it'),
        rows.length ? h('div', { class: 'wrap' }, h('table', { class: 'cm-tbl' }, h('thead', {}, h('tr', {}, ['Nick', 'Level', 'Added by', 'When', ''].map(t => h('th', { text: t })))), h('tbody', {}, rows)))
          : h('p', { class: 'empty', text: 'Nobody is on the list yet. Founders don\'t need to be.' }));
      if (can) {
        const nick = h('input', { class: 'in', type: 'text', maxlength: '40', autocomplete: 'off', spellcheck: 'false', placeholder: 'nick (must be registered)', 'aria-label': 'Nick to add' });
        const lv = levelPick();
        const add = h('button', { type: 'submit', class: 'btn main' }, icon('plus'), 'Add');
        const f = h('form', { class: 'cm-add' }, nick, lv, add);
        f.addEventListener('submit', async e => { e.preventDefault(); if (!nick.value.trim()) { nick.focus(); return; } if (await run(add, 'ACCESS', 'ADD', nick.value.trim(), lv.value)) nick.value = ''; });
        list.append(f);
      }
      const side = [];
      if (d.can.founder) side.push(successorCard());
      if (d.can.set) side.push(togglesCard('Quick settings', QUICK, 'the rest on Settings'));
      if (d.can.founder) side.push(dangerCard());
      return side.length ? h('div', { class: 'band21' }, list, h('div', { class: 'hm-col' }, side)) : list;
    }

    function successorCard() {
      const inp = h('input', { class: 'in', type: 'text', maxlength: '40', value: d.successor || '', autocomplete: 'off', spellcheck: 'false', placeholder: 'a registered nick', 'aria-label': 'Successor' });
      const save = h('button', { type: 'submit', class: 'btn', text: 'Save' });
      const f = h('form', { class: 'cm-add plain' }, inp, save);
      f.addEventListener('submit', e => { e.preventDefault(); run(save, 'SET', 'SUCCESSOR', '', inp.value.trim() || 'OFF'); });
      return h('section', { class: 'card' }, hmHead('user', 'Successor', d.successor ? '' : 'none yet'),
        h('p', { class: 'note', text: 'Takes ' + d.name + ' over if your nick expires. Empty removes it.' }), f);
    }

    function togglesCard(titleText, names, end) {
      return h('section', { class: 'card' }, hmHead('settings', titleText, end),
        names.map(n => {
          const t = TOGGLES.find(x => x[0] === n), id = 'cm-' + n;
          const input = h('input', { type: 'checkbox', id });
          input.checked = !!d.toggles[n];
          input.addEventListener('change', async () => {
            input.disabled = true;
            if (!await run(null, 'SET', n, '', input.checked ? 'ON' : 'OFF')) input.checked = !input.checked;
            input.disabled = false;
          });
          return h('div', { class: 'my-row' }, h('label', { class: 'ck', for: id }, input, t[1]), h('span', { class: 'hint', text: t[2] }));
        }));
    }

    // dangerCard: hand over and drop, founder only, asked twice (the
    // channel name typed again, like /cs drop #chan #chan).
    function dangerCard() {
      const ask = h('div', { class: 'confirm', hidden: true });
      const hand = h('button', { type: 'button', class: 'btn', text: 'Hand over' });
      const drop = h('button', { type: 'button', class: 'btn bad', text: 'Drop' });
      hand.addEventListener('click', () => {
        const who = h('input', { class: 'in', type: 'text', maxlength: '40', autocomplete: 'off', placeholder: 'the new founder\'s nick' });
        const again = h('input', { class: 'in', type: 'text', maxlength: '64', autocomplete: 'off', placeholder: d.name });
        const ok = h('button', { type: 'button', class: 'btn main', text: 'Give it away' });
        const no = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
        no.addEventListener('click', () => { ask.hidden = true; });
        ok.addEventListener('click', () => {
          if (again.value.trim().toLowerCase() !== d.name.toLowerCase()) { again.classList.add('wrong'); again.focus(); return; }
          if (!who.value.trim()) { who.focus(); return; }
          run(ok, 'SET', 'FOUNDER', '', who.value.trim()).then(done => { if (done) location.hash = '#/mychannels'; });
        });
        put(ask, h('label', { class: 'f' }, 'New founder', who), h('label', { class: 'f' }, 'Type ' + d.name + ' to confirm', again), ok, no);
        ask.hidden = false; who.focus();
      });
      drop.addEventListener('click', () => {
        const again = h('input', { class: 'in', type: 'text', maxlength: '64', autocomplete: 'off', placeholder: d.name });
        const ok = h('button', { type: 'button', class: 'btn bad', text: 'Drop ' + d.name });
        const no = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
        no.addEventListener('click', () => { ask.hidden = true; });
        ok.addEventListener('click', () => {
          if (again.value.trim().toLowerCase() !== d.name.toLowerCase()) { again.classList.add('wrong'); again.focus(); return; }
          busy(ok, async () => {
            try {
              toast([(await api('POST', '/api/chan-do', { name: d.name, kind: 'DROP', value: '', mask: again.value.trim(), reason: '' })).done.text]);
              location.hash = '#/mychannels';
            } catch (e) { toastErr(e.message); }
          });
        });
        put(ask, h('label', { class: 'f' }, 'The access list, AKICKs and settings go. Type ' + d.name + ' to drop it', again), ok, no);
        ask.hidden = false; again.focus();
      });
      return h('section', { class: 'card' }, hmHead('alert', 'Hand over or drop', 'founder only'),
        h('div', { class: 'row' }, h('span', {}, 'Give ' + d.name + ' to someone else'), hand),
        h('div', { class: 'row' }, h('span', {}, 'Drop the channel'), drop), ask);
    }

    function akickTab() {
      const rows = (d.akicks || []).map(k => {
        const del = h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Remove ' + k.mask, title: 'Remove' }, icon('x'));
        del.addEventListener('click', () => run(del, 'AKICK', 'DEL', k.mask));
        return h('tr', {}, h('td', { class: 'mono', text: k.mask }), h('td', { text: k.reason || '' }), h('td', { class: 'mono', text: k.by || '' }),
          h('td', { class: 'mono', text: ago2(k.at) }), h('td', { class: 'cm-x' }, del));
      });
      const mask = h('input', { class: 'in', type: 'text', maxlength: '120', autocomplete: 'off', spellcheck: 'false', placeholder: 'nick or mask, like *!*@bad.example', 'aria-label': 'Nick or mask' });
      const why = h('input', { class: 'in', type: 'text', maxlength: '200', autocomplete: 'off', placeholder: 'reason (shown in the kick)', 'aria-label': 'Reason' });
      const add = h('button', { type: 'submit', class: 'btn main' }, icon('plus'), 'Add');
      const f = h('form', { class: 'cm-add' }, mask, why, add);
      f.addEventListener('submit', async e => { e.preventDefault(); if (!mask.value.trim()) { mask.focus(); return; } if (await run(add, 'AKICK', 'ADD', mask.value.trim(), why.value.trim())) { mask.value = ''; why.value = ''; } });
      return h('section', { class: 'card' }, hmHead('bans', 'AKICK', 'kicked and banned when they join'),
        rows.length ? h('div', { class: 'wrap' }, h('table', { class: 'cm-tbl' }, h('thead', {}, h('tr', {}, ['Mask', 'Reason', 'Added by', 'When', ''].map(t => h('th', { text: t })))), h('tbody', {}, rows)))
          : h('p', { class: 'empty', text: 'The AKICK list is empty.' }), f);
    }

    function settingsTab() {
      const cards = [];
      if (d.can.topic) {
        const t = h('input', { class: 'in', type: 'text', maxlength: '390', value: d.topic || '', autocomplete: 'off', 'aria-label': 'Topic' });
        const save = h('button', { type: 'submit', class: 'btn', text: 'Set topic' });
        const f = h('form', { class: 'cm-add plain' }, t, save);
        f.addEventListener('submit', e => { e.preventDefault(); run(save, 'TOPIC', '', '', t.value.trim()); });
        cards.push(h('section', { class: 'card' }, hmHead('message', 'Topic'), f));
      }
      if (d.can.set) {
        const text = (label, opt, value, max) => {
          const i = h('input', { class: 'in', type: 'text', maxlength: String(max), value: value || '', autocomplete: 'off', 'aria-label': label });
          const save = h('button', { type: 'submit', class: 'btn', text: 'Save' });
          const f = h('form', { class: 'cm-add plain' }, i, save);
          f.addEventListener('submit', e => { e.preventDefault(); if (i.value.trim()) run(save, 'SET', opt, '', i.value.trim()); else i.focus(); });
          return h('label', { class: 'f' }, label, f);
        };
        cards.push(h('section', { class: 'card' }, hmHead('hash', 'About the channel'),
          text('Description', 'DESCRIPTION', d.description, 200), text('Web address', 'URL', d.url, 200)));
        cards.push(togglesCard('Switches', TOGGLES.map(x => x[0])));
      }
      if (d.can.founder) cards.push(successorCard());
      return h('div', { class: 'hm-cols' }, h('div', { class: 'hm-col' }, cards.filter((_, i) => i % 2 === 0)), h('div', { class: 'hm-col' }, cards.filter((_, i) => i % 2 === 1)));
    }

    function memoTab() {
      const memos = (d.memos || []).slice().reverse();
      const rows = memos.map(x => {
        const bodyEl = h('div', { class: 'mm-body', hidden: true });
        const dot = h('span', { class: 'dot ' + (x.read ? 'off' : 'warn') });
        const btn = h('button', { type: 'button', class: 'mm-head', 'aria-expanded': 'false' },
          dot, h('b', { text: x.from }), h('span', { class: 'tx', text: x.text }), h('span', { class: 'tm', text: when(x.sent) }));
        const r = h('div', { class: 'mm-row' + (x.read ? ' read' : '') }, btn, bodyEl);
        const del = h('button', { type: 'button', class: 'btn bad' }, icon('trash'), 'Delete');
        del.addEventListener('click', () => busy(del, async () => {
          try { await api('POST', '/api/chan-memo', { name: d.name, id: x.id, verb: 'del' }); toast(['Memo from ' + x.from + ' deleted.']); await load(); } catch (e) { toastErr(e.message); }
        }));
        put(bodyEl, h('p', { text: x.text }), h('div', { class: 'row-btns' }, del));
        btn.addEventListener('click', async () => {
          const open = bodyEl.hidden;
          bodyEl.hidden = !open;
          r.classList.toggle('open', open);
          btn.setAttribute('aria-expanded', String(open));
          if (!open || x.read) return;
          try { await api('POST', '/api/chan-memo', { name: d.name, id: x.id, verb: 'read' }); x.read = true; r.classList.add('read'); dot.className = 'dot off'; } catch (e) { toastErr(e.message); }
        });
        return r;
      });
      return h('section', { class: 'card' }, hmHead('mail', 'Memo box', memos.length + ' of ' + d.memoLimit + ' kept'),
        rows.length ? rows : h('p', { class: 'empty', text: 'No memos for ' + d.name + '. Anyone can leave one: /msg MemoServ SEND ' + d.name + ' and a message.' }));
    }

    function draw() {
      const list = [['people', 'people', 'People now', d.people.length, true], ['access', 'list', 'Access', (d.access || []).length, d.can.accessList],
        ['akick', 'bans', 'AKICK', (d.akicks || []).length, d.can.akick], ['settings', 'settings', 'Settings', null, d.can.set || d.can.topic || d.can.founder],
        ['memo', 'mail', 'Memo box', (d.memos || []).length, d.can.memo]].filter(x => x[4]);
      if (!list.some(x => x[0] === tab)) tab = list[0][0];
      put(tabs, list.map(([id, ic, label, n]) => {
        const b = h('button', { type: 'button', role: 'tab', class: 'cm-tab' + (id === tab ? ' on' : ''), 'aria-selected': String(id === tab) },
          icon(ic), label, n != null ? h('span', { class: 'cm-n', text: n }) : null);
        b.addEventListener('click', () => { tab = id; draw(); });
        return b;
      }));
      tabIntoView(tabs);
      const make = { people: peopleTab, access: accessTab, akick: akickTab, settings: settingsTab, memo: memoTab }[tab];
      put(body, make());
    }
    async function load() {
      try { d = (await api('GET', '/api/my-chan?name=' + encodeURIComponent(name))).chan; } catch (e) { put(body, h('p', { class: 'empty', text: e.message })); put(tabs); return; }
      title.textContent = d.name;
      put(crumbEl, 'Hive Panel / ', h('a', { href: '#/mychannels', text: 'My channels' }), ' / ', h('b', { text: d.name }));
      document.title = d.name + ' · Hive Panel';
      put(chips,
        h('span', { class: 'role' + (d.can.founder ? ' on' : ''), text: d.level }),
        h('span', { class: 'role', text: d.users + ' here' }),
        h('span', { class: 'role', text: 'registered ' + day(d.registered) }),
        d.topic ? h('span', { class: 'role cm-topic', text: 'topic: ' + d.topic }) : null);
      draw();
    }
    load();
    return { el };
  }

  // ---------- My nick and My channels (members and opers) ----------
  // NickServ SET as switches: each change runs the real SET on the server
  // and shows NickServ's own answer.
  const KILL_TEXT = { on: 'on, after a minute', quick: 'on, quickly', immed: 'on, at once', off: 'off' };
  async function mySet(name, value) {
    const d = await api('POST', '/api/my-set', { name, value });
    toast([d.done ? d.done.text : 'Saved.']);
  }
  function myNickPage() {
    const sub = h('span', { class: 'sub', text: 'Your NickServ account. A change applies at once, as if you typed the command.' });
    const title = h('h1', { text: me.account });
    const settings = h('div', {}, h('p', { class: 'empty', text: 'Loading…' }));
    const about = h('div', {});
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, title, sub)),
      h('div', { class: 'band21' },
        h('section', { class: 'card' }, h('h2', { text: 'Settings' }), settings),
        h('section', { class: 'card' }, h('h2', { text: 'Your account' }), about)));
    const sw = (id, label, hint, on, opt, val) => {
      const input = h('input', { type: 'checkbox', id });
      input.checked = on;
      input.addEventListener('change', async () => {
        input.disabled = true;
        try { await mySet(opt, val(input.checked)); } catch (e) { input.checked = !input.checked; toastErr(e.message); }
        input.disabled = false;
      });
      return h('div', { class: 'my-row' }, h('label', { class: 'ck', for: id }, input, label), h('span', { class: 'hint', text: hint }));
    };
    async function load() {
      let d;
      try { d = (await api('GET', '/api/my-nick')).myNick; } catch (e) { put(settings, h('p', { class: 'empty', text: e.message })); return; }
      title.textContent = d.name;
      const greet = h('input', { class: 'in', id: 'my-greet', type: 'text', maxlength: '200', value: d.greet || '', placeholder: 'hi all' });
      const save = h('button', { type: 'button', class: 'btn', text: 'Save' });
      save.addEventListener('click', () => busy(save, async () => {
        try { await mySet('GREET', greet.value.trim()); } catch (e) { toastErr(e.message); }
      }));
      put(settings,
        sw('my-kill', 'Protect my nick', 'Someone using it without your password is renamed (now ' + (KILL_TEXT[d.kill] || d.kill) + ').',
          d.kill !== 'off', 'KILL', on => on ? 'ON' : 'OFF'),
        sw('my-hide-email', 'Hide my email', 'Others don\'t see it in NickServ INFO.', d.hideEmail, 'HIDE', on => 'EMAIL ' + (on ? 'ON' : 'OFF')),
        me.member ? null : sw('my-hide-status', 'Hide that I am an oper', 'NickServ INFO doesn\'t say it.', d.hideStatus, 'HIDE', on => 'STATUS ' + (on ? 'ON' : 'OFF')),
        sw('my-autoop', 'Get my status when I join', 'ChanServ gives you @ or + in your channels.', d.autoOp, 'AUTOOP', on => on ? 'ON' : 'OFF'),
        sw('my-private', 'Keep me out of the nick list', 'NickServ LIST doesn\'t show you.', d.private, 'PRIVATE', on => on ? 'ON' : 'OFF'),
        h('div', { class: 'my-row' }, h('label', { class: 'f', for: 'my-greet' }, 'Greet message', h('div', { class: 'my-inline' }, greet, save)),
          h('span', { class: 'hint', text: 'A channel bot says it when you join, where the channel allows greets. Empty removes it.' })));
      const reg = new Date(d.registered);
      put(about,
        h('div', { class: 'row' }, h('span', { text: 'Registered' }), h('span', { class: 'end mono', text: isNaN(reg) ? '' : reg.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' }) })),
        d.email ? h('div', { class: 'row' }, h('span', { text: 'Email' }), h('span', { class: 'end mono', text: d.email })) : null,
        h('div', { class: 'row' }, h('span', { text: 'Nicks in your account' }), h('span', { class: 'end mono', text: (d.nicks || [d.name]).join(', ') })),
        h('p', { class: 'note', text: 'Password and email are changed on IRC for now: /msg NickServ SET PASSWORD or SET EMAIL.' }));
    }
    load();
    return { el };
  }
  function myChannelsPage() {
    const list = h('div', {}, h('p', { class: 'empty', text: 'Loading…' }));
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'My channels' }),
        h('span', { class: 'sub', text: 'Where you are the founder or on the access list. Open one to run it.' }))),
      h('section', { class: 'card' }, h('h2', { text: 'Channels' }), list));
    api('GET', '/api/my-nick').then(d => {
      const chans = (d.myNick && d.myNick.channels) || [];
      put(list, chans.length ? chans.map(c => h('div', { class: 'row' },
        h('span', {}, h('a', { class: 'mono cm-link', href: chanHref(c.name), text: c.name }), h('span', { class: 'hint', text: '  ' + c.users + ' here' })),
        h('span', { class: 'role' + (c.level === 'Founder' ? ' on' : ''), text: c.level })))
        : h('p', { class: 'empty', text: 'You aren\'t on the access list of any channel yet. Register one on IRC: /msg ChanServ REGISTER #channel' }));
    }).catch(e => put(list, h('p', { class: 'empty', text: e.message })));
    return { el };
  }

  // ---------- Services: tabs ----------
  // Overview (the settings, flag a), then one tab per services job an oper
  // may have: Vhosts (h), Bots (b), News (g), Sessions (k), DEFCON (a or k).
  // Each tab reads /api/svc?kind and changes through /api/serv-do, which runs
  // the real HostServ, BotServ or OperServ command.
  const SVC_TABS = [
    ['overview', 'hive', 'Overview', ''],
    ['vhosts', 'user', 'Vhosts', 'h'],
    ['bots', 'services', 'Bots', 'b'],
    ['news', 'message', 'News', 'g'],
    ['sessions', 'gauge', 'Sessions', 'k'],
    ['defcon', 'alert', 'DEFCON', 'ak'],
  ];
  const SVC_LIVES = [['1h', '1 hour'], ['1d', '1 day'], ['7d', '7 days'], ['30d', '30 days'], ['0', 'Until removed']];
  // svcKit is what every Services tab shares: its card, loading, a table,
  // a remove button that asks once and a form field.
  function svcKit(kind) {
    const box = h('div', { class: 'main-stack' }, h('p', { class: 'empty', text: 'Loading…' }));
    const kit = {
      box, d: null, draw: () => {},
      async load() {
        try { kit.d = (await api('GET', '/api/svc?kind=' + kind)).svc; kit.draw(kit.d); }
        catch (e) { put(box, h('section', { class: 'card' }, h('p', { class: 'empty', text: e.message }))); }
      },
      // busyNow is true while a remove or a form is half done, so the timer
      // doesn't redraw under the oper.
      busyNow: () => !!box.querySelector('.rm, .confirm:not([hidden])'),
      async run(btn, body, after) {
        return busy(btn, async () => {
          try { toast([(await servDo(body)).done.text]); if (after) after(); await kit.load(); }
          catch (e) { toastErr(e.message); }
        });
      },
      table(heads, rows, empty) {
        return h('div', { class: 'wrap' }, h('table', { class: 'svc' },
          h('thead', {}, h('tr', {}, heads.map(t => t ? h('th', { scope: 'col', text: t }) : h('th', { scope: 'col' }, h('span', { class: 'sr', text: 'Actions' }))))),
          h('tbody', {}, rows.length ? rows : h('tr', {}, h('td', { colspan: heads.length, class: 'empty', text: empty })))));
      },
      // rm is a cell with a remove button that asks once; extra (say a
      // Change button) sits before it and comes back on Keep.
      rm(label, body, ...extra) {
        const cell = h('td', { class: 'end-cell' });
        const trash = h('button', { type: 'button', class: 'x', 'aria-label': label, title: 'Remove' }, icon('trash'));
        cell.append(...extra, trash);
        trash.addEventListener('click', () => {
          const yes = h('button', { type: 'button', class: 'btn bad', text: 'Remove' });
          const no = h('button', { type: 'button', class: 'btn ghost', text: 'Keep' });
          no.addEventListener('click', () => put(cell, ...extra, trash));
          yes.addEventListener('click', () => kit.run(yes, body));
          put(cell, h('span', { class: 'rm' }, yes, no));
          yes.focus();
        });
        return cell;
      },
      input(id, label, attrs, hint) {
        const i = h('input', Object.assign({ class: 'in', id, type: 'text', autocomplete: 'off', spellcheck: 'false' }, attrs));
        return { i, el: h('label', { class: 'f', for: id }, label, i, hint ? h('span', { class: 'hint', text: hint }) : null) };
      },
      select(id, label, opts, value) {
        const i = h('select', { class: 'in', id }, opts.map(([v, t]) => h('option', { value: v, text: t })));
        i.value = value;
        return { i, el: h('label', { class: 'f', for: id }, label, i) };
      },
      // enter makes Enter in the inputs press go.
      enter(inputs, go) {
        for (const i of inputs) i.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); go.click(); } });
      },
    };
    return kit;
  }

  function vhostsTab() {
    const k = svcKit('vhosts');
    const nick = k.input('vh-nick', 'Nick', { maxlength: '64', placeholder: 'Their registered nick' });
    const vhost = k.input('vh-host', 'Vhost', { class: 'in mono', maxlength: '200', placeholder: 'cool.example or ident@cool.example' }, 'Shown instead of their real host once they identify.');
    const give = h('button', { type: 'button', class: 'btn main', text: 'Give vhost' });
    give.addEventListener('click', () => {
      if (!nick.i.value.trim()) { nick.i.classList.add('wrong'); nick.i.focus(); return; }
      if (!vhost.i.value.trim()) { vhost.i.classList.add('wrong'); vhost.i.focus(); return; }
      k.run(give, { kind: 'vhost-set', name: nick.i.value.trim(), value: vhost.i.value.trim() }, () => { nick.i.value = ''; vhost.i.value = ''; });
    });
    for (const f of [nick, vhost]) f.i.addEventListener('input', () => f.i.classList.remove('wrong'));
    k.enter([nick.i, vhost.i], give);
    const form = h('section', { class: 'card' }, h('h2', { text: 'Give a vhost' }), nick.el, vhost.el, give);
    k.draw = d => {
      const waiting = d.waiting || [], active = d.active || [];
      const wRows = waiting.map(v => {
        const cell = h('td', { class: 'end-cell' });
        const ok = h('button', { type: 'button', class: 'btn main', text: 'Approve' });
        const no = h('button', { type: 'button', class: 'btn', text: 'Reject…' });
        ok.addEventListener('click', () => k.run(ok, { kind: 'vhost-ok', name: v.nick }));
        no.addEventListener('click', () => {
          const why = h('input', { class: 'in', type: 'text', maxlength: '200', autocomplete: 'off', placeholder: 'Reason (they see it)', 'aria-label': 'Reason for ' + v.nick });
          const yes = h('button', { type: 'button', class: 'btn bad', text: 'Reject' });
          const keep = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
          keep.addEventListener('click', () => put(cell, h('span', { class: 'row-btns' }, ok, no)));
          yes.addEventListener('click', () => k.run(yes, { kind: 'vhost-no', name: v.nick, reason: why.value }));
          k.enter([why], yes);
          put(cell, h('span', { class: 'rm' }, why, yes, keep));
          why.focus();
        });
        cell.append(h('span', { class: 'row-btns' }, ok, no));
        return h('tr', {}, h('td', { text: v.nick }), h('td', { class: 'mono', text: v.vhost }), h('td', { class: 'lbl', text: ago(v.created) }), cell);
      });
      const aRows = active.map(v => h('tr', {}, h('td', { text: v.nick }), h('td', { class: 'mono', text: v.vhost }),
        h('td', { text: v.creator || '—' }), h('td', { class: 'lbl', text: day(v.created) }),
        k.rm('Remove the vhost of ' + v.nick, { kind: 'vhost-del', name: v.nick })));
      put(k.box,
        h('div', { class: 'band21' },
          h('div', { class: 'main-stack' },
            h('section', { class: 'card list-card' },
              h('div', { class: 'ch tools' }, h('h2', { text: 'Waiting for an oper' }), h('span', { class: 'lbl', text: waiting.length ? plural(waiting.length, 'request', 'requests') : '' })),
              k.table(['Nick', 'Asked for', 'Asked', ''], wRows, 'No vhost requests are waiting. People ask with /msg HostServ REQUEST.')),
            h('section', { class: 'card list-card' },
              h('div', { class: 'ch tools' }, h('h2', { text: 'Vhosts in use' }), h('span', { class: 'lbl', text: plural(active.length, 'vhost', 'vhosts') })),
              k.table(['Nick', 'Vhost', 'Set by', 'Since', ''], aRows, 'Nobody has a vhost yet.'))),
          form));
    };
    return k;
  }

  function botsTab() {
    const k = svcKit('bots');
    const nick = k.input('bot-nick', 'Nick', { maxlength: '30', placeholder: 'Beebot' });
    const mask = k.input('bot-mask', 'User@host', { class: 'in mono', maxlength: '200', placeholder: 'bot@services.hive' });
    const real = k.input('bot-real', 'Real name', { maxlength: '200', placeholder: 'Shown in /whois (the nick if empty)' });
    const add = h('button', { type: 'button', class: 'btn main', text: 'Add bot' });
    add.addEventListener('click', () => {
      if (!nick.i.value.trim()) { nick.i.classList.add('wrong'); nick.i.focus(); return; }
      if (!mask.i.value.trim()) { mask.i.classList.add('wrong'); mask.i.focus(); return; }
      k.run(add, { kind: 'bot-add', name: nick.i.value.trim(), mask: mask.i.value.trim(), reason: real.i.value }, () => { nick.i.value = mask.i.value = real.i.value = ''; });
    });
    for (const f of [nick, mask]) f.i.addEventListener('input', () => f.i.classList.remove('wrong'));
    k.enter([nick.i, mask.i, real.i], add);
    const form = h('section', { class: 'card' }, h('h2', { text: 'Add a bot' }), nick.el, mask.el, real.el,
      h('p', { class: 'hint', text: 'Channel founders put it in with /msg BotServ ASSIGN #channel nick.' }), add);
    let open = null;
    const COLS = 6;
    k.busyNow = () => !!open || !!k.box.querySelector('.rm');
    k.draw = d => {
      const bots = d.bots || [];
      const rows = [];
      for (const b of bots) {
        const isOpen = b.nick === open;
        const change = h('button', { type: 'button', class: 'btn', text: 'Change…', 'aria-expanded': String(isOpen) });
        change.addEventListener('click', () => { open = isOpen ? null : b.nick; k.draw(k.d); });
        const rmCell = k.rm('Delete the bot ' + b.nick, { kind: 'bot-del', name: b.nick }, change);
        rows.push(h('tr', { class: isOpen ? 'open' : null },
          h('td', {}, b.nick, b.private ? [' ', h('span', { class: 'tag', text: 'private' })] : null),
          h('td', { class: 'mono', text: b.user + '@' + b.host }),
          h('td', { text: b.real }),
          h('td', { text: String(b.channels) }),
          h('td', { class: 'lbl', text: day(b.created) }),
          rmCell));
        if (isOpen) {
          const n = h('input', { class: 'in', type: 'text', maxlength: '30', autocomplete: 'off', 'aria-label': 'Nick', value: b.nick });
          const m = h('input', { class: 'in mono', type: 'text', maxlength: '200', autocomplete: 'off', 'aria-label': 'User@host', value: b.user + '@' + b.host });
          const r = h('input', { class: 'in', type: 'text', maxlength: '200', autocomplete: 'off', 'aria-label': 'Real name', value: b.real });
          const save = h('button', { type: 'button', class: 'btn main', text: 'Save' });
          const cancel = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
          cancel.addEventListener('click', () => { open = null; k.draw(k.d); });
          save.addEventListener('click', () => k.run(save, { kind: 'bot-change', name: b.nick, value: n.value.trim(), mask: m.value.trim(), reason: r.value }, () => { open = null; }));
          k.enter([n, m, r], save);
          rows.push(h('tr', { class: 'detail' }, h('td', { colspan: COLS },
            h('div', { class: 'bot-edit' },
              h('label', { class: 'f' }, 'Nick', n), h('label', { class: 'f' }, 'User@host', m), h('label', { class: 'f' }, 'Real name', r),
              h('div', { class: 'row-btns' }, save, cancel)))));
        }
      }
      put(k.box, h('div', { class: 'band21' },
        h('section', { class: 'card list-card' },
          h('div', { class: 'ch tools' }, h('h2', { text: 'Bots' }), h('span', { class: 'lbl', text: plural(bots.length, 'bot', 'bots') })),
          k.table(['Nick', 'User@host', 'Real name', 'Channels', 'Made', ''], rows, 'No BotServ bots yet.')),
        form));
    };
    return Object.assign(k, { esc() { if (!open) return false; open = null; k.draw(k.d); return true; } });
  }

  const NEWS_TITLE = { logon: 'Logon news', oper: 'Oper news', random: 'Random news' };
  function newsTab() {
    const k = svcKit('news');
    k.draw = d => {
      put(k.box, (d.news || []).map(l => {
        const rows = l.items.map(n => h('tr', {}, h('td', { text: n.text }), h('td', { text: n.set_by }), h('td', { class: 'lbl', text: ago(n.set_at) }),
          k.rm('Remove this ' + l.kind + ' news', { kind: 'news-del', name: l.kind, id: n.id })));
        const full = l.items.length >= l.max;
        const text = h('input', { class: 'in', type: 'text', maxlength: '400', autocomplete: 'off', 'aria-label': 'New ' + l.kind + ' news',
          placeholder: full ? 'Full: remove one first' : 'Type the news' });
        const add = h('button', { type: 'button', class: 'btn main', text: 'Add' });
        text.disabled = add.disabled = full;
        add.addEventListener('click', () => {
          if (!text.value.trim()) { text.classList.add('wrong'); text.focus(); return; }
          k.run(add, { kind: 'news-add', name: l.kind, value: text.value });
        });
        text.addEventListener('input', () => text.classList.remove('wrong'));
        k.enter([text], add);
        return h('section', { class: 'card list-card' },
          h('div', { class: 'ch tools' }, h('h2', { text: NEWS_TITLE[l.kind] || l.kind }), h('span', { class: 'lbl', text: l.items.length + ' of ' + l.max })),
          h('p', { class: 'note pad', text: l.about }),
          k.table(['News', 'Set by', 'Set', ''], rows, 'None.'),
          h('div', { class: 'tools' }, text, add));
      }));
    };
    k.busyNow = () => !!k.box.querySelector('.rm') || [...k.box.querySelectorAll('input')].some(i => i.value);
    return k;
  }

  function sessionsTab() {
    const k = svcKit('sessions');
    const mask = k.input('se-mask', 'Address', { class: 'in mono', maxlength: '200', placeholder: '203.0.113.7 or 203.0.113.*' });
    const limit = k.input('se-limit', 'Connections allowed', { class: 'in mono', type: 'number', min: '0', max: '10000', value: '10' }, '0 = no limit.');
    const reason = k.input('se-reason', 'Reason', { maxlength: '200', placeholder: 'A school, a bouncer host…' });
    const life = k.select('se-life', 'Ends', SVC_LIVES, '0');
    const add = h('button', { type: 'button', class: 'btn main', text: 'Add exception' });
    add.addEventListener('click', () => {
      if (!mask.i.value.trim()) { mask.i.classList.add('wrong'); mask.i.focus(); return; }
      const n = Number(limit.i.value);
      if (limit.i.value === '' || !Number.isInteger(n) || n < 0 || n > 10000) { limit.i.classList.add('wrong'); limit.i.focus(); return; }
      k.run(add, { kind: 'except-add', mask: mask.i.value.trim(), value: String(n), reason: reason.i.value, life: life.i.value },
        () => { mask.i.value = reason.i.value = ''; });
    });
    for (const f of [mask, limit]) f.i.addEventListener('input', () => f.i.classList.remove('wrong'));
    k.enter([mask.i, limit.i, reason.i], add);
    const form = h('section', { class: 'card' }, h('h2', { text: 'Add an exception' }), mask.el, limit.el, reason.el, life.el, add);
    k.draw = d => {
      const ex = d.excepts || [], busiest = d.busiest || [];
      const lim = n => n ? String(n) : 'no limit';
      const bRows = busiest.map(b => {
        const pick = h('button', { type: 'button', class: 'btn ghost', text: 'Except…' });
        pick.addEventListener('click', () => { mask.i.value = b.ip; mask.i.classList.remove('wrong'); limit.i.focus(); limit.i.select(); });
        return h('tr', { class: b.limit && b.count >= b.limit ? 'full' : null }, h('td', { class: 'mono', text: b.ip }),
          h('td', { text: b.count + ' of ' + lim(b.limit) }), h('td', { class: 'end-cell' }, pick));
      });
      const eRows = ex.map(e => h('tr', {}, h('td', { class: 'mono', text: e.mask }), h('td', { text: lim(e.limit) }), h('td', { text: e.reason || '—' }),
        h('td', { text: e.set_by }), h('td', { class: 'lbl', text: ends(e.expires) }),
        k.rm('Remove the exception for ' + e.mask, { kind: 'except-del', mask: e.mask })));
      put(k.box, h('div', { class: 'band21' },
        h('div', { class: 'main-stack' },
          h('section', { class: 'card list-card' },
            h('div', { class: 'ch tools' }, h('h2', { text: 'Session exceptions' }), h('span', { class: 'lbl', text: 'Everyone else: ' + (d.session_limit ? plural(d.session_limit, 'connection', 'connections') + ' per address' : 'no limit') })),
            k.table(['Address', 'Allowed', 'Reason', 'Set by', 'Ends', ''], eRows, 'No exceptions. Every address gets the same limit.')),
          h('section', { class: 'card list-card' },
            h('div', { class: 'ch tools' }, h('h2', { text: 'Busiest addresses' }), h('span', { class: 'lbl', text: 'now' })),
            k.table(['Address', 'Connections', ''], bRows, 'Nobody is connected.'))),
        form));
    };
    k.busyNow = () => !!k.box.querySelector('.rm') || !!mask.i.value;
    return k;
  }

  function defconTab() {
    const k = svcKit('defcon');
    const mask = k.input('ig-mask', 'Nick or mask', { class: 'in mono', maxlength: '200', placeholder: 'Spammer or *!*@203.0.113.*' });
    const reason = k.input('ig-reason', 'Reason', { maxlength: '200', placeholder: 'Only opers see it' });
    const life = k.select('ig-life', 'Ends', SVC_LIVES, '1d');
    const add = h('button', { type: 'button', class: 'btn main', text: 'Add ignore' });
    add.addEventListener('click', () => {
      if (!mask.i.value.trim()) { mask.i.classList.add('wrong'); mask.i.focus(); return; }
      k.run(add, { kind: 'ignore-add', mask: mask.i.value.trim(), reason: reason.i.value, life: life.i.value }, () => { mask.i.value = reason.i.value = ''; });
    });
    mask.i.addEventListener('input', () => mask.i.classList.remove('wrong'));
    k.enter([mask.i, reason.i], add);
    k.draw = d => {
      const sure = h('div', { class: 'confirm', hidden: true });
      const levels = (d.levels || []).map((words, i) => {
        const n = i + 1, on = n === d.defcon;
        const kids = [h('b', { text: 'DEFCON ' + n }), h('span', { class: 'lbl', text: words })];
        if (!d.can_defcon) return h('div', { class: 'dc-lv' + (on ? ' on' : '') }, kids);
        const b = h('button', { type: 'button', class: 'dc-lv' + (on ? ' on' : ''), 'aria-pressed': String(on) }, kids);
        b.addEventListener('click', () => {
          if (on) { sure.hidden = true; return; }
          const go = h('button', { type: 'button', class: 'btn ' + (n < d.defcon ? 'bad' : 'main'), text: 'Set DEFCON ' + n });
          const cancel = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
          cancel.addEventListener('click', () => { sure.hidden = true; });
          go.addEventListener('click', () => k.run(go, { kind: 'defcon', value: String(n) }));
          put(sure, h('span', { class: 'f', text: 'Set DEFCON ' + n + ' (' + words + ')?' + (n === 5 ? '' : ' Opers are told, and it stays until someone sets it back.') }), go, cancel);
          sure.hidden = false;
          go.focus();
        });
        return b;
      });
      const parts = [h('section', { class: 'card' }, h('h2', { text: 'DEFCON level' }),
        h('p', { class: 'note', text: d.defcon === 5 ? 'Normal: nothing is held back.' : 'DEFCON ' + d.defcon + ' is on. 5 is normal.' }),
        h('div', { class: 'dc-list' }, levels), sure,
        d.can_defcon ? null : h('p', { class: 'hint', text: 'Changing the level needs oper flag a (admin).' }))];
      if (d.can_ignore) {
        const ig = d.ignores || [];
        const rows = ig.map(g => h('tr', {}, h('td', { class: 'mono', text: g.mask }), h('td', { text: g.reason || '—' }), h('td', { text: g.set_by }),
          h('td', { class: 'lbl', text: ends(g.expires) }), k.rm('Stop ignoring ' + g.mask, { kind: 'ignore-del', mask: g.mask })));
        parts.push(h('div', { class: 'band21' },
          h('section', { class: 'card list-card' },
            h('div', { class: 'ch tools' }, h('h2', { text: 'Services ignore' }), h('span', { class: 'lbl', text: plural(ig.length, 'entry', 'entries') })),
            k.table(['Nick or mask', 'Reason', 'Set by', 'Ends', ''], rows, 'Services answer everyone.')),
          h('section', { class: 'card' }, h('h2', { text: 'Add an ignore' }), mask.el, reason.el, life.el,
            h('p', { class: 'hint', text: 'Services stop answering them; they can still chat.' }), add)));
      }
      put(k.box, parts);
    };
    k.busyNow = () => !!k.box.querySelector('.rm, .confirm:not([hidden])') || !!mask.i.value;
    return k;
  }

  function servicesPage() {
    const tile = label => { const n = h('span', { class: 'num', text: '…' }); return { n, el: h('section', { class: 'card tight' }, h('span', { class: 'lbl', text: label }), n) }; };
    const tNicks = tile('Registered nicks'), tChans = tile('Registered channels'), tMemos = tile('Memos waiting'), tBots = tile('Bots');
    const bots = h('div');
    const canSet = has('a');
    const form = h('div', { class: 'svc-form' }, h('p', { class: 'empty', text: 'Loading…' }));
    const count = h('span');
    const undoBtn = h('button', { type: 'button', class: 'btn ghost', text: 'Undo' });
    const saveBtn = h('button', { type: 'button', class: 'btn main', text: 'Save' });
    const bar = h('div', { class: 'unsaved', hidden: true }, count, h('div', { class: 'row-btns' }, undoBtn, saveBtn));
    const off = h('section', { class: 'card', hidden: true }, h('h2', { text: 'Services run on the hub' }),
      h('p', { class: 'note', text: 'This server has its services switched off ([services] enabled = false): the hub of the network runs them. Use the hub\'s Hive Panel for these pages.' }));
    const botsCard = h('section', { class: 'card' }, h('h2', { text: 'Bots' }), bots);
    const main = canSet ? h('div', { class: 'band21' }, h('section', { class: 'card' }, h('h2', { text: 'Settings' }), form, bar), botsCard) : botsCard;
    const overview = h('div', { class: 'main-stack' }, h('div', { class: 'tiles4' }, tNicks.el, tChans.el, tMemos.el, tBots.el), main);

    // The tabs this oper may open; Overview is everyone's.
    const shown = SVC_TABS.filter(([, , , need]) => !need || [...need].some(has));
    const makers = { vhosts: vhostsTab, bots: botsTab, news: newsTab, sessions: sessionsTab, defcon: defconTab };
    const made = {};
    const asked = new URLSearchParams(location.hash.split('?')[1] || '').get('tab');
    let tab = shown.some(t => t[0] === asked) ? asked : 'overview';
    const tabs = h('div', { class: 'cm-tabs', role: 'tablist', 'aria-label': 'Services' });
    const body = h('div', { role: 'tabpanel' });
    function showTab() {
      put(tabs, shown.map(([id, ic, label]) => {
        const b = h('button', { type: 'button', role: 'tab', class: 'cm-tab' + (id === tab ? ' on' : ''), 'aria-selected': String(id === tab) }, icon(ic), label);
        b.addEventListener('click', () => { if (tab !== id) { tab = id; showTab(); } });
        return b;
      }));
      tabIntoView(tabs);
      if (tab === 'overview') put(body, overview);
      else {
        if (!made[tab]) made[tab] = makers[tab]();
        put(body, made[tab].box);
        made[tab].load();
      }
      history.replaceState(null, '', '#/services' + (tab === 'overview' ? '' : '?tab=' + tab));
    }
    const el = h('div', { class: 'main-stack' },
      h('header', { class: 'head' }, h('div', { class: 'head-text' }, h('h1', { text: 'Services' }),
        h('span', { class: 'sub', text: 'NickServ, ChanServ and the other bots, built in' }))),
      off, shown.length > 1 ? tabs : null, body);
    const fields = [];

    function check(f) {
      const s = f.s;
      if (s.kind === 'events') {
        const was = eventsOn(s);
        f.dirty = Object.entries(f.boxes).some(([n, b]) => b.checked !== was.includes(n));
        f.ok = true;
      } else if (s.kind === 'onoff') {
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
    // The log-events checkboxes: one field, saved as one list.
    function eventsBlock(s) {
      if (!s) return null;
      const f = { s, boxes: {}, ok: true, dirty: false, label: 'Log events' };
      const on = eventsOn(s);
      fields.push(f);
      return LOG_EVENTS.map(([title, list]) => [
        h('div', { class: 'grp', text: title }),
        h('div', { class: 'ev-grid' }, list.map(([n, label, note]) => {
          const box = h('input', { type: 'checkbox', id: 'svc-ev-' + n });
          box.checked = on.includes(n);
          box.addEventListener('change', () => check(f));
          f.boxes[n] = box;
          return h('label', { class: 'ck ev', for: 'svc-ev-' + n }, box, h('span', null, label, note ? h('small', { text: note }) : null));
        })),
      ]);
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
          }),
          title === 'Services log' ? eventsBlock(by['log-events']) : null));
        put(form, groups);
        fields.forEach(check);
      } catch (e) { put(form, h('p', { class: 'empty', text: e.message })); }
    }
    function resetEvents(f) { const on = eventsOn(f.s); for (const [n, b] of Object.entries(f.boxes)) b.checked = on.includes(n); }
    undoBtn.addEventListener('click', () => { fields.forEach(f => { if (f.s.kind === 'events') resetEvents(f); else if (f.s.kind === 'onoff') f.input.checked = f.s.value === 'on'; else f.input.value = f.s.value; check(f); }); });
    saveBtn.addEventListener('click', () => busy(saveBtn, async () => {
      const done = [];
      for (const f of fields.filter(x => x.dirty && x.ok)) {
        const picked = f.s.kind === 'events' ? Object.keys(f.boxes).filter(n => f.boxes[n].checked) : [];
        const value = f.s.kind === 'events' ? (picked.length ? picked.join(',') : 'off') : f.s.kind === 'onoff' ? (f.input.checked ? 'on' : 'off') : f.input.value.trim();
        try {
          const d = await api('POST', '/api/set', { name: f.s.name, value });
          done.push(d.changed);
          f.s.value = d.changed.new;
          if (f.s.kind === 'events') resetEvents(f);
          else if (f.s.kind !== 'onoff') f.input.value = d.changed.new;
          check(f);
          if (f.input) { f.input.classList.remove('saved'); void f.input.offsetWidth; f.input.classList.add('saved'); }
        } catch (e) {
          if (f.s.kind !== 'onoff' && f.s.kind !== 'events') { f.input.classList.add('wrong'); f.hint.classList.add('wrong'); f.hint.textContent = e.message; }
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
    if (canSet) load();
    showTab();
    // The open tab is read again every 30 s unless the oper is busy in it.
    const timer = setInterval(() => {
      const t = made[tab];
      if (t && !document.hidden && !t.busyNow()) bg(t.load);
    }, 30000);
    const onKey = e => { if (e.key === 'Escape' && made[tab] && made[tab].esc) made[tab].esc(); };
    document.addEventListener('keydown', onKey);
    let svcOn = true;
    return {
      el,
      leave() { clearInterval(timer); document.removeEventListener('keydown', onKey); },
      status(s) {
        if (svcOn !== s.services_on) {
          svcOn = s.services_on;
          off.hidden = svcOn;
          tabs.hidden = body.hidden = !svcOn;
        }
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
    // #/channels?q=#hive (the Spotlight's busiest room): that channel, opened.
    const asked = new URLSearchParams(location.hash.split('?')[1] || '').get('q');
    if (asked) { q = asked; search.value = asked; open = asked; }
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
    const timer = setInterval(() => { if (!open && !document.hidden) bg(load); }, 15000);
    load();
    return { el, leave() { clearInterval(timer); document.removeEventListener('keydown', onKey); } };
  }

  // ---------- boot ----------
  api('GET', '/api/me').then(d => { me = d.oper; start(); }).catch(e => { if (!me) showSignin(e.message); });
})();
