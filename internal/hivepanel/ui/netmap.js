// Hive Panel network map (step 3k): a flat world map that wraps round
// left and right like a globe, zooms to a city, and shows each server as a
// small icon with its links. Places come from beeircd.conf ([[map.place]],
// set here by the owner); nothing is looked up from an address and no
// person is ever placed.
//
// Drawing is split in two canvases: the base (land, lines, icons, names)
// is redrawn only when the view or the data changes; the top one only
// carries the moving signals on live links. Land outlines are one Path2D
// in degrees, drawn with a transform, so moving the map costs little. The
// finer outlines load only when zoomed in. Lite (asked for, or switched on
// by itself when drawing is slow, or by reduced motion for the signals)
// drops the glow and the moving signals.
'use strict';
window.HiveNetMap = function (env) {
  const { h, put, api, busy, toast, toastErr, plural, fmt } = env;
  const DPR = () => Math.min(window.devicePixelRatio || 1, 2);
  const reduced = () => window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  const css = n => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
  const MAXZ = 10, CLUSTER = 30;
  let world = { lo: null, hi: null, loading: null };

  function decode(pack) {
    const sc = pack.scale || 100, p = new Path2D();
    for (const r of pack.rings) {
      let x = 0, y = 0;
      for (let i = 0; i + 1 < r.length; i += 2) {
        x += r[i]; y += r[i + 1];
        i ? p.lineTo(x / sc, y / sc) : p.moveTo(x / sc, y / sc);
      }
      p.closePath();
    }
    return p;
  }
  async function loadWorld(which) {
    const r = await fetch('map/world-' + which + '.json', { credentials: 'same-origin' });
    if (!r.ok) throw new Error('map ' + which + ': ' + r.status);
    return decode(await r.json());
  }

  function create(opt) {
    const st = {
      data: null, can: !!opt.canPlace, view: { lon: 20, lat: 15, z: 1 }, fitted: false,
      filter: 'all', lite: false, autoLite: false, slow: 0, sel: null, placing: null, pin: null,
      nodes: [], hits: [], links: [], anim: 0, dragging: false, quick: false, cw: 0, ch: 0,
    };
    try { st.lite = localStorage.getItem('hive-map-lite') === '1'; } catch { /* private window */ }

    // ---------- DOM ----------
    const count = h('span', { class: 'lbl' });
    const search = h('input', { class: 'search nm-search', type: 'search', placeholder: 'Find a server', 'aria-label': 'Find a server', autocomplete: 'off', spellcheck: 'false' });
    const found = h('div', { class: 'nm-found', role: 'listbox', hidden: true });
    const chip = (k, label, dot) => { const b = h('button', { type: 'button', class: 'nm-chip', 'aria-pressed': k === st.filter ? 'true' : 'false' }, dot ? h('span', { class: 'dot bad' }) : null, label); b.addEventListener('click', () => { st.filter = k; chips.forEach(c => c.setAttribute('aria-pressed', c === b ? 'true' : 'false')); st.sel = null; drawCard(); redraw(); }); return b; };
    const chips = [chip('all', 'All'), chip('split', 'Split only', true), chip('hubs', 'Hubs only')];
    const liteBtn = h('button', { type: 'button', class: 'nm-chip', 'aria-pressed': st.lite ? 'true' : 'false', title: 'No glow and no moving signals: less work for slow machines' }, 'Lite');
    liteBtn.addEventListener('click', () => { st.lite = !st.lite; st.autoLite = false; liteBtn.setAttribute('aria-pressed', st.lite ? 'true' : 'false'); try { localStorage.setItem('hive-map-lite', st.lite ? '1' : '0'); } catch { /* ignore */ } redraw(); });
    const landC = h('canvas', { class: 'nm-land', 'aria-hidden': 'true' });
    const base = h('canvas', { class: 'nm-base', tabindex: '0', role: 'img', 'aria-label': 'Network map' });
    const top = h('canvas', { class: 'nm-top', 'aria-hidden': 'true' });
    const zin = h('button', { type: 'button', 'aria-label': 'Zoom in', title: 'Zoom in' }, '+');
    const zout = h('button', { type: 'button', 'aria-label': 'Zoom out', title: 'Zoom out' }, '−');
    const zfit = h('button', { type: 'button', 'aria-label': 'Show every server', title: 'Fit all' }, env.icon('fit'));
    const card = h('div', { class: 'nm-card', hidden: true, role: 'dialog', 'aria-label': 'Server' });
    const hint = h('span', { class: 'nm-hint', text: 'Drag to move round the world · wheel or pinch to zoom · click an icon' });
    const placing = h('form', { class: 'nm-placing', hidden: true });
    const legend = h('div', { class: 'nm-legend', 'aria-hidden': 'true' },
      h('span', {}, h('i', { class: 'nm-i hub' }), 'hub'), h('span', {}, h('i', { class: 'nm-i' }), 'server'),
      h('span', {}, h('i', { class: 'nm-i split' }), 'split'), h('span', {}, h('i', { class: 'nm-i group', text: '3' }), 'close together'));
    const stage = h('div', { class: 'nm-stage' }, landC, base, top, h('div', { class: 'nm-zoom' }, zin, zout, zfit), hint, card);
    const unplaced = h('div', { class: 'nm-unplaced' });
    const el = h('section', { class: 'card nm' },
      h('div', { class: 'nm-bar' },
        h('div', { class: 'nm-grp' }, h('h2', { text: 'Network map' }), count),
        h('div', { class: 'nm-grp' }, h('div', { class: 'nm-find' }, search, found), chips, liteBtn)),
      placing, stage, legend, unplaced);

    // ---------- geometry ----------
    const scale = () => st.cw / 360 * st.view.z; // px per degree
    function clampView() {
      const v = st.view, minZ = Math.max(1, st.ch / (st.cw / 2));
      v.z = Math.min(MAXZ, Math.max(minZ, v.z));
      v.lon = ((v.lon + 540) % 360) - 180;
      const half = st.ch / 2 / scale();
      v.lat = half >= 85 ? 0 : Math.max(-85 + half, Math.min(85 - half, v.lat));
    }
    // screen x for lon on the copy nearest to x0 (or the centre)
    function sx(lon, near) {
      const s = scale(), c = st.cw / 2;
      let x = c + (lon - st.view.lon) * s;
      const W = 360 * s, ref = near == null ? c : near;
      while (x - ref > W / 2) x -= W;
      while (ref - x > W / 2) x += W;
      return x;
    }
    const sy = lat => st.ch / 2 + (st.view.lat - lat) * scale();
    function toLonLat(x, y) {
      const s = scale();
      return { lon: ((st.view.lon + (x - st.cw / 2) / s + 540) % 360) - 180, lat: st.view.lat - (y - st.ch / 2) / s };
    }

    // ---------- data → nodes and links ----------
    function build() {
      const d = st.data; if (!d) return;
      const status = Object.fromEntries((d.status || []).map(s => [s.name.toLowerCase(), s]));
      const places = Object.fromEntries((d.places || []).map(p => [p.server.toLowerCase(), p]));
      const blocks = Object.fromEntries((d.blocks || []).map(b => [b.name.toLowerCase(), b]));
      const nodes = new Map(), kids = {};
      const mk = (name, extra) => {
        const k = name.toLowerCase(), p = places[k], s = status[k];
        const n = Object.assign({ name, key: k, place: p || null, users: 0, status: s || null, block: blocks[k] || null, software: s && s.software || '', gone: false }, extra);
        nodes.set(k, n); return n;
      };
      for (const m of d.map) { mk(m.name, { self: !!m.self, uplink: m.uplink || (m.self ? '' : d.self), users: m.users || 0, lag: m.lag_ms || 0, sid: m.sid || '' }); }
      for (const m of d.map) if (!m.self) (kids[(m.uplink || d.self).toLowerCase()] ||= []).push(m.name.toLowerCase());
      // link blocks not linked now: split off from us
      for (const b of d.blocks || []) if (!nodes.has(b.name.toLowerCase())) mk(b.name, { uplink: d.self, gone: true, split: true });
      // placed servers that aren't here and have no block: not linked now, no line
      for (const p of d.places || []) if (!nodes.has(p.server.toLowerCase())) mk(p.server, { gone: true, uplink: '' });
      for (const n of nodes.values()) {
        n.hub = (kids[n.key] || []).length > 0 || (n.self && d.role === 'hub');
        n.split = n.gone || (n.status && n.status.state !== 'up' && !n.self);
      }
      st.nodes = [...nodes.values()];
      st.links = [];
      for (const n of st.nodes) {
        if (!n.uplink) continue;
        const up = nodes.get(n.uplink.toLowerCase()); if (!up) continue;
        st.links.push({ a: up, b: n, split: n.split });
      }
      const placed = st.nodes.filter(n => n.place), splits = st.nodes.filter(n => n.split).length;
      count.textContent = plural(d.map.length, 'server', 'servers') + ' · ' + plural(st.links.filter(l => !l.split).length, 'link', 'links') + (splits ? ' · ' + splits + ' split' : '') + (placed.length < st.nodes.length ? ' · ' + (st.nodes.length - placed.length) + ' not on the map' : '');
      base.setAttribute('aria-label', 'Network map: ' + count.textContent + '. ' + placed.map(n => n.name + ' in ' + (n.place.name || 'a place') + (n.split ? ' (split)' : '')).join(', '));
      drawUnplaced();
    }
    const shown = n => n.place && (st.filter === 'all' || (st.filter === 'split' && n.split) || (st.filter === 'hubs' && (n.hub || n.self)));

    // ---------- drawing ----------
    function size() {
      const r = stage.getBoundingClientRect(), w = Math.max(280, Math.round(r.width)), hgt = Math.round(Math.min(560, Math.max(240, w * 0.5)));
      if (w === st.cw && hgt === st.ch) return false;
      st.cw = w; st.ch = hgt;
      for (const c of [landC, base, top]) { c.width = w * DPR(); c.height = hgt * DPR(); c.style.width = w + 'px'; c.style.height = hgt + 'px'; }
      stage.style.height = hgt + 'px';
      return true;
    }
    let raf = 0;
    function redraw(quick) { st.quick = !!quick; if (!raf) raf = requestAnimationFrame(() => { raf = 0; drawBase(); }); }

    function drawLand(x, dpr, s, lite) {
      x.setTransform(dpr, 0, 0, dpr, 0, 0);
      const g = x.createLinearGradient(0, 0, 0, st.ch); g.addColorStop(0, '#101010'); g.addColorStop(1, '#0b0b0b');
      x.fillStyle = g; x.fillRect(0, 0, st.cw, st.ch);
      // grid every 30 degrees
      x.strokeStyle = 'rgba(236,235,230,.035)'; x.lineWidth = 1; x.beginPath();
      for (let lo = -180; lo < 180; lo += 30) { const a = sx(lo); for (const X of [a - 360 * s, a, a + 360 * s]) if (X >= 0 && X <= st.cw) { x.moveTo(X, 0); x.lineTo(X, st.ch); } }
      for (let la = -60; la <= 60; la += 30) { const b = sy(la); x.moveTo(0, b); x.lineTo(st.cw, b); }
      x.stroke();
      // land: the finer outlines when zoomed in (loaded once, then kept)
      const fine = st.view.z >= 3 && world.hi, land = fine ? world.hi : world.lo;
      if (st.view.z >= 3 && !world.hi && !world.loading) {
        world.loading = loadWorld('50').then(p => { world.hi = p; redraw(); }).catch(() => { /* keep the light one */ }).finally(() => { world.loading = null; });
      }
      if (land) {
        for (const k of [-1, 0, 1]) {
          const ox = sx(0) + k * 360 * s;
          if (ox + 180 * s < 0 || ox - 180 * s > st.cw) continue;
          x.setTransform(dpr * s, 0, 0, -dpr * s, dpr * ox, dpr * sy(0));
          if (!lite && !fine) { x.save(); x.shadowColor = 'rgba(242,179,61,.10)'; x.shadowBlur = 14 * dpr; x.fillStyle = '#22211e'; x.fill(land); x.restore(); }
          x.fillStyle = '#242320'; x.fill(land);
          if (!st.quick || st.view.z > 2) { x.lineWidth = (st.view.z > 2 ? 0.8 : 0.6) / s; x.strokeStyle = st.view.z > 2 ? 'rgba(236,235,230,.13)' : 'rgba(236,235,230,.09)'; x.stroke(land); }
        }
        x.setTransform(dpr, 0, 0, dpr, 0, 0);
      }
      const v = x.createRadialGradient(st.cw / 2, st.ch / 2, Math.min(st.cw, st.ch) * 0.45, st.cw / 2, st.ch / 2, Math.max(st.cw, st.ch) * 0.75);
      v.addColorStop(0, 'rgba(0,0,0,0)'); v.addColorStop(1, 'rgba(0,0,0,.45)'); x.fillStyle = v; x.fillRect(0, 0, st.cw, st.ch);
    }
    function drawBase() {
      if (!st.cw) return;
      clampView();
      const t0 = performance.now(), x = base.getContext('2d'), dpr = DPR(), s = scale(), lite = st.lite || st.quick;
      base.dataset.view = st.view.lon.toFixed(4) + ',' + st.view.lat.toFixed(4) + ',' + st.view.z.toFixed(4); // for tests and bug reports
      // the land layer is redrawn only when the view (or its look) changes
      const fine = st.view.z >= 3 && world.hi, key = [st.view.lon, st.view.lat, st.view.z, st.cw, st.ch, dpr, lite, st.quick, !!fine, !!world.lo].join(',');
      if (key !== st.landKey) { st.landKey = key; drawLand(landC.getContext('2d'), dpr, s, lite); }
      x.setTransform(dpr, 0, 0, dpr, 0, 0); x.clearRect(0, 0, st.cw, st.ch);
      void fine;

      // servers on screen, grouped where icons would overlap (hubs never)
      const pts = [];
      for (const n of st.nodes) if (shown(n)) { const X = sx(n.place.lon), Y = sy(n.place.lat); pts.push({ n, a: X, b: Y }); }
      const cl = [];
      for (const p of pts.sort((m, q) => (q.n.hub - m.n.hub) || (q.n.self - m.n.self) || q.n.users - m.n.users)) {
        const c = p.n.hub || p.n.self || p.n === st.sel ? null : cl.find(c => !c.lead.hub && !c.lead.self && c.lead !== st.sel && Math.hypot(c.a - p.a, c.b - p.b) < CLUSTER);
        if (c) c.items.push(p.n); else cl.push({ a: p.a, b: p.b, items: [p.n], lead: p.n });
      }
      for (const c of cl) if (c.items.length > 1) for (const hb of cl.filter(o => o.lead.hub || o.lead.self)) {
        const d = Math.hypot(c.a - hb.a, c.b - hb.b);
        if (d < 34) { const ang = d ? Math.atan2(c.b - hb.b, c.a - hb.a) : Math.PI / 2; c.a = hb.a + Math.cos(ang) * 34; c.b = hb.b + Math.sin(ang) * 34; }
      }
      const where = new Map(); for (const c of cl) for (const n of c.items) where.set(n, c);
      // links, once per pair of icons/chips; hub-to-hub bright, the rest quiet when far out
      const many = st.nodes.length > 25 && st.view.z < 2, seen = new Map();
      for (const l of st.links) {
        const ca = where.get(l.a), cb = where.get(l.b);
        if (!ca || !cb || ca === cb) continue;
        const key = (ca.lead.key < cb.lead.key ? ca.lead.key + '|' + cb.lead.key : cb.lead.key + '|' + ca.lead.key) + (l.split ? '!' : '');
        const e = seen.get(key) || { ca, cb, split: l.split, n: 0, backbone: (l.a.hub || l.a.self) && (l.b.hub || l.b.self) };
        e.n++; seen.set(key, e);
      }
      st.curves = [];
      for (const e of [...seen.values()].sort((m, q) => m.split - q.split)) {
        const pts2 = curve(e.ca.a, e.ca.b, e.cb.a, e.cb.b), wgt = 1 + Math.log2(e.n) * 0.6;
        x.lineCap = 'round';
        if (e.split) { x.strokeStyle = '#f28a7d'; x.lineWidth = 1.6; x.setLineDash([6, 6]); poly(x, pts2); x.setLineDash([]); continue; }
        if (many && !e.backbone) { x.strokeStyle = `rgba(242,179,61,${Math.min(0.42, 0.22 + 0.05 * wgt).toFixed(2)})`; x.lineWidth = Math.min(1.6, 0.8 + 0.2 * wgt); poly(x, pts2); continue; }
        if (!lite) { x.strokeStyle = 'rgba(242,179,61,.18)'; x.lineWidth = 5 * Math.min(wgt, 2); poly(x, pts2); }
        const gr = x.createLinearGradient(pts2[0][0], pts2[0][1], pts2.at(-1)[0], pts2.at(-1)[1]); gr.addColorStop(0, '#f2b33d'); gr.addColorStop(1, '#ffd27f');
        x.strokeStyle = gr; x.lineWidth = 1.6 * Math.min(wgt, 2.4); poly(x, pts2);
        st.curves.push(pts2);
      }
      // icons (hubs on top), then names where they fit
      st.hits = [];
      for (const c of [...cl].sort((m, q) => (m.lead.hub || m.lead.self) - (q.lead.hub || q.lead.self))) {
        if (c.items.length > 1) { const w = groupIcon(x, c.a, c.b, c); st.hits.push({ x: c.a, y: c.b, r: Math.max(16, w / 2), group: c }); }
        else { serverIcon(x, c.a, c.b, c.lead, c.lead === st.sel); st.hits.push({ x: c.a, y: c.b, r: 14, node: c.lead }); }
      }
      // names never cover an icon or a chip, nor each other
      const boxes = st.hits.map(ht => [ht.x - ht.r, ht.y - 11, ht.r * 2, 22]), singles = cl.filter(c => c.items.length === 1);
      for (const c of singles.sort((m, q) => ((q.lead.hub || q.lead.self) - (m.lead.hub || m.lead.self)) || q.lead.users - m.lead.users)) {
        const n = c.lead, big = n.hub || n.self;
        if (!big && singles.length > 25 && st.view.z < 1.6) continue;
        x.font = '500 11px ' + css('--body'); const w = x.measureText(n.name).width + 10;
        const y0 = big && singles.length > 6 ? c.b - 32 : c.b + 14, bx = [c.a - w / 2, y0, w, 16];
        if (boxes.some(o => (o[0] !== c.a - 14 || o[1] !== c.b - 11) && bx[0] < o[0] + o[2] && bx[0] + bx[2] > o[0] && bx[1] < o[1] + o[3] && bx[1] + bx[3] > o[1])) continue;
        boxes.push(bx);
        x.fillStyle = 'rgba(13,13,13,.78)'; x.fillRect(bx[0], bx[1], w, 16);
        x.fillStyle = n.split ? '#f28a7d' : big && singles.length > 6 ? '#f2b33d' : '#ecebe6'; x.fillText(n.name, bx[0] + 5, bx[1] + 12);
      }
      if (st.pin) { // where a server is being placed
        const X = sx(st.pin.lon), Y = sy(st.pin.lat);
        x.strokeStyle = '#ffd27f'; x.lineWidth = 1.5; x.setLineDash([3, 3]); x.beginPath(); x.arc(X, Y, 16, 0, 7); x.stroke(); x.setLineDash([]);
        serverIcon(x, X, Y, { name: '', hub: false, split: false, self: false }, true);
      }
      positionCard();
      // too slow? Lite by itself (once), and say so
      const took = performance.now() - t0;
      if (!st.quick && !st.lite) { st.slow = took > 45 ? st.slow + 1 : 0; if (st.slow >= 3) { st.lite = st.autoLite = true; liteBtn.setAttribute('aria-pressed', 'true'); toast(['The map was slow to draw here, so Lite is on. Turn it off with Lite.']); redraw(); } }
      animate();
    }
    function curve(x1, y1, x2, y2) {
      const d = Math.hypot(x2 - x1, y2 - y1), mx = (x1 + x2) / 2, my = (y1 + y2) / 2 - Math.min(d * 0.22, 120), out = [];
      for (let i = 0; i <= 32; i++) { const t = i / 32; out.push([(1 - t) ** 2 * x1 + 2 * (1 - t) * t * mx + t * t * x2, (1 - t) ** 2 * y1 + 2 * (1 - t) * t * my + t * t * y2]); }
      return out;
    }
    function poly(x, pts) { x.beginPath(); pts.forEach(([a, b], i) => i ? x.lineTo(a, b) : x.moveTo(a, b)); x.stroke(); }
    function serverIcon(x, cx, cy, n, sel) {
      const S = 20, honey = '#f2b33d', red = '#f28a7d', hub = n.hub || n.self;
      x.save(); if (!st.lite) { x.shadowColor = 'rgba(0,0,0,.6)'; x.shadowBlur = 6; }
      x.beginPath(); x.roundRect(cx - S / 2, cy - S / 2, S, S, 4); x.fillStyle = hub && !n.split ? honey : '#181818'; x.fill(); x.shadowBlur = 0;
      x.lineWidth = sel ? 2 : 1.2; x.strokeStyle = sel ? '#fff3d1' : n.split ? red : n.software && !/bee/i.test(n.software) ? '#b98a3a' : honey; x.stroke();
      const ink = hub && !n.split ? '#151515' : n.split ? red : honey; x.strokeStyle = ink; x.fillStyle = ink; x.lineWidth = 1.3;
      if (hub) { x.beginPath(); for (let i = 0; i < 6; i++) { const a = Math.PI / 6 + i * Math.PI / 3, X = cx + 5.6 * Math.cos(a), Y = cy + 5.6 * Math.sin(a); i ? x.lineTo(X, Y) : x.moveTo(X, Y); } x.closePath(); x.stroke(); x.beginPath(); x.arc(cx, cy, 1.6, 0, 7); x.fill(); }
      else {
        x.beginPath(); x.roundRect(cx - 5.5, cy - 5.5, 11, 4.6, 1); x.roundRect(cx - 5.5, cy + 0.9, 11, 4.6, 1); x.stroke();
        x.beginPath(); x.arc(cx - 3, cy - 3.2, 0.8, 0, 7); x.arc(cx - 3, cy + 3.2, 0.8, 0, 7); x.fill();
        if (n.software && !/bee/i.test(n.software)) { x.fillStyle = '#181818'; x.fillRect(cx + 1, cy - 4.4, 4, 2.4); x.fillRect(cx + 1, cy + 2, 4, 2.4); }
      }
      if (n.split) { x.fillStyle = red; x.beginPath(); x.arc(cx + S / 2 - 1, cy - S / 2 + 1, 3.4, 0, 7); x.fill(); x.strokeStyle = '#121212'; x.lineWidth = 1.5; x.stroke(); }
      x.restore();
    }
    function groupIcon(x, cx, cy, c) {
      const t = String(c.items.length); x.font = '600 12px ' + css('--body');
      const w = Math.max(30, x.measureText(t).width + 26);
      x.save(); if (!st.lite) { x.shadowColor = 'rgba(0,0,0,.6)'; x.shadowBlur = 6; }
      x.beginPath(); x.roundRect(cx - w / 2, cy - 11, w, 22, 11); x.fillStyle = '#181818'; x.fill(); x.shadowBlur = 0; x.lineWidth = 1.2; x.strokeStyle = '#f2b33d'; x.stroke();
      const gx = cx - w / 2 + 11; x.lineWidth = 1.1; x.beginPath(); x.roundRect(gx - 4, cy - 5, 8, 4, 1); x.roundRect(gx - 4, cy + 1, 8, 4, 1); x.stroke();
      x.fillStyle = '#ecebe6'; x.fillText(t, gx + 8, cy + 4.5);
      if (c.items.some(n => n.split)) { x.fillStyle = '#f28a7d'; x.beginPath(); x.arc(cx + w / 2 - 2, cy - 9, 3.4, 0, 7); x.fill(); x.strokeStyle = '#121212'; x.lineWidth = 1.5; x.stroke(); }
      x.restore();
      return w;
    }

    // moving signals on live links: the only thing drawn every frame
    let last = 0;
    function animate() {
      cancelAnimationFrame(st.anim);
      const x = top.getContext('2d'), dpr = DPR();
      x.setTransform(dpr, 0, 0, dpr, 0, 0); x.clearRect(0, 0, st.cw, st.ch);
      if (st.lite || reduced() || st.dragging || !st.curves || !st.curves.length) return;
      const step = now => {
        if (document.hidden || !el.isConnected) { st.anim = 0; return; }
        if (now - last >= 33) { // about 30 frames a second is plenty
          last = now; x.clearRect(0, 0, st.cw, st.ch); x.fillStyle = '#fff3d1';
          for (const [i, pts] of st.curves.entries()) {
            const len = pts.length - 1;
            for (const off of [0, 0.5]) { const t = ((now / 2600 + i * 0.137 + off) % 1), f = t * len, j = Math.floor(f), k = f - j, p = pts[j], q = pts[Math.min(len, j + 1)]; x.beginPath(); x.arc(p[0] + (q[0] - p[0]) * k, p[1] + (q[1] - p[1]) * k, 2.2, 0, 7); x.fill(); }
          }
        }
        st.anim = requestAnimationFrame(step);
      };
      st.anim = requestAnimationFrame(step);
    }
    document.addEventListener('visibilitychange', () => { if (!document.hidden && el.isConnected) animate(); });

    // ---------- moving the map ----------
    const ptrs = new Map(); let downAt = null, pinch0 = null, quickTimer = 0;
    function settle() { clearTimeout(quickTimer); quickTimer = setTimeout(() => { st.dragging = false; redraw(); }, 140); }
    base.addEventListener('pointerdown', e => {
      try { base.setPointerCapture(e.pointerId); } catch { /* not a live pointer */ }
      ptrs.set(e.pointerId, { x: e.clientX, y: e.clientY });
      downAt = { x: e.clientX, y: e.clientY, moved: false };
      if (ptrs.size === 2) { const [a, b] = [...ptrs.values()]; pinch0 = { d: Math.hypot(a.x - b.x, a.y - b.y), z: st.view.z }; }
    });
    base.addEventListener('pointermove', e => {
      const p = ptrs.get(e.pointerId);
      if (!p) { hover(e); return; }
      const dx = e.clientX - p.x, dy = e.clientY - p.y; p.x = e.clientX; p.y = e.clientY;
      if (downAt && Math.hypot(e.clientX - downAt.x, e.clientY - downAt.y) > 4) downAt.moved = true;
      if (ptrs.size === 2 && pinch0) {
        const [a, b] = [...ptrs.values()], r = base.getBoundingClientRect();
        zoomAt(pinch0.z * Math.hypot(a.x - b.x, a.y - b.y) / pinch0.d, (a.x + b.x) / 2 - r.left, (a.y + b.y) / 2 - r.top, true);
        return;
      }
      if (!downAt || !downAt.moved) return;
      st.dragging = true; card.hidden = true;
      const s = scale(); st.view.lon -= dx / s; st.view.lat += dy / s; redraw(true); settle();
    });
    const up = e => {
      ptrs.delete(e.pointerId); if (ptrs.size < 2) pinch0 = null;
      if (downAt && !downAt.moved && e.type === 'pointerup') click(e);
      if (!ptrs.size) downAt = null;
    };
    base.addEventListener('pointerup', up); base.addEventListener('pointercancel', up);
    base.addEventListener('wheel', e => { e.preventDefault(); const r = base.getBoundingClientRect(); zoomAt(st.view.z * (e.deltaY < 0 ? 1.18 : 1 / 1.18), e.clientX - r.left, e.clientY - r.top, true); }, { passive: false });
    base.addEventListener('dblclick', e => { const r = base.getBoundingClientRect(); fly(null, st.view.z * 2, e.clientX - r.left, e.clientY - r.top); });
    base.addEventListener('keydown', e => {
      const s = scale(), k = e.key;
      if (k === 'ArrowLeft') st.view.lon -= 40 / s; else if (k === 'ArrowRight') st.view.lon += 40 / s;
      else if (k === 'ArrowUp') st.view.lat += 40 / s; else if (k === 'ArrowDown') st.view.lat -= 40 / s;
      else if (k === '+' || k === '=') zoomAt(st.view.z * 1.5, st.cw / 2, st.ch / 2);
      else if (k === '-') zoomAt(st.view.z / 1.5, st.cw / 2, st.ch / 2);
      else if (k === 'Escape') { st.sel = null; drawCard(); }
      else return;
      e.preventDefault(); redraw();
    });
    function zoomAt(z, px, py, quick) {
      const before = toLonLat(px, py); st.view.z = Math.min(MAXZ, Math.max(1, z)); clampView();
      const after = toLonLat(px, py); st.view.lon += before.lon - after.lon; st.view.lat += before.lat - after.lat;
      card.hidden = true; if (quick) { st.dragging = true; redraw(true); settle(); } else redraw();
    }
    zin.addEventListener('click', () => fly(null, st.view.z * 1.6)); zout.addEventListener('click', () => fly(null, st.view.z / 1.6));
    zfit.addEventListener('click', () => fit(st.nodes.filter(shown)));
    // a short flight (none with reduced motion)
    function fly(to, z, px, py) {
      const from = { ...st.view };
      let target;
      if (to) target = { lon: to.lon, lat: to.lat, z };
      else { const p = toLonLat(px ?? st.cw / 2, py ?? st.ch / 2); target = { lon: from.lon + (p.lon - from.lon) * (1 - from.z / z), lat: from.lat + (p.lat - from.lat) * (1 - from.z / z), z }; }
      let dl = target.lon - from.lon; if (dl > 180) dl -= 360; if (dl < -180) dl += 360;
      if (reduced()) { st.view = { lon: from.lon + dl, lat: target.lat, z: target.z }; redraw(); return; }
      const t0 = performance.now(), ms = 420;
      const step = now => {
        const t = Math.min(1, (now - t0) / ms), e = 1 - (1 - t) ** 3;
        st.view = { lon: from.lon + dl * e, lat: from.lat + (target.lat - from.lat) * e, z: from.z * Math.pow(target.z / from.z, e) };
        st.dragging = t < 1; drawBase(); if (t < 1) requestAnimationFrame(step); else { st.dragging = false; redraw(); }
      };
      card.hidden = true; requestAnimationFrame(step);
    }
    function fit(list) {
      const ps = list.filter(n => n.place).map(n => n.place);
      if (!ps.length) { fly({ lon: 20, lat: 15 }, 1); return; }
      // the smallest box round the world that holds them all (it may cross 180)
      const lons = ps.map(p => p.lon).sort((a, b) => a - b);
      let gap = 0, at = 0; for (let i = 0; i < lons.length; i++) { const g = (i ? lons[i] - lons[i - 1] : lons[0] + 360 - lons.at(-1)); if (g > gap) { gap = g; at = i; } }
      const west = lons[at], span = 360 - gap, lonC = west + span / 2;
      const lats = ps.map(p => p.lat), latC = (Math.max(...lats) + Math.min(...lats)) / 2, latSpan = Math.max(...lats) - Math.min(...lats);
      const z = ps.length === 1 ? 3 : Math.max(1, Math.min(6, Math.min(360 / Math.max(span * 1.5, 1), (st.ch / (st.cw / 360)) / Math.max(latSpan * 1.5 + 8, 1))));
      fly({ lon: ((lonC + 540) % 360) - 180, lat: latC }, z);
    }

    // ---------- clicks, hover, card ----------
    function hitAt(px, py) { let best = null, bd = 1e9; for (const ht of st.hits) { const d = Math.hypot(ht.x - px, ht.y - py); if (d <= ht.r && d < bd) { best = ht; bd = d; } } return best; }
    function hover(e) { if (st.placing) { base.style.cursor = 'crosshair'; return; } const r = base.getBoundingClientRect(), ht = hitAt(e.clientX - r.left, e.clientY - r.top); base.style.cursor = ht ? 'pointer' : 'grab'; base.title = ht ? (ht.node ? ht.node.name : ht.group.items.length + ' servers: ' + ht.group.items.slice(0, 6).map(n => n.name).join(', ') + (ht.group.items.length > 6 ? ' …' : '')) : ''; }
    function click(e) {
      const r = base.getBoundingClientRect(), px = e.clientX - r.left, py = e.clientY - r.top;
      if (st.placing) { st.pin = toLonLat(px, py); placingSave.disabled = false; redraw(); return; }
      const ht = hitAt(px, py);
      if (!ht) { if (st.sel) { st.sel = null; drawCard(); redraw(); } return; }
      if (ht.group) { fit(ht.group.items); return; }
      st.sel = ht.node; drawCard(); redraw();
    }
    function positionCard() {
      if (!st.sel || card.hidden || !st.sel.place) return;
      const X = sx(st.sel.place.lon), Y = sy(st.sel.place.lat), w = card.offsetWidth || 260, hh = card.offsetHeight || 150;
      if (X < -20 || X > st.cw + 20 || Y < -20 || Y > st.ch + 20) { card.hidden = true; return; }
      card.style.left = Math.max(8, Math.min(st.cw - w - 8, X + 18)) + 'px';
      card.style.top = Math.max(8, Math.min(st.ch - hh - 8, Y - hh / 2)) + 'px';
    }
    function drawCard() {
      const n = st.sel;
      if (!n) { card.hidden = true; return; }
      const s = n.status, where = n.place ? n.place.name || 'Placed' : 'Not on the map';
      const lines = [h('b', { text: n.name }),
        h('span', { class: 'lbl', text: [where, n.self ? 'this server' : (n.software || (n.gone ? '' : 'BeeIRCd')).replace(/^BeeIRCd-/, 'BeeIRCd '), n.hub && !n.self ? 'hub' : '', n.uplink && !n.self && st.data && n.uplink.toLowerCase() !== st.data.self.toLowerCase() ? 'via ' + n.uplink : ''].filter(Boolean).join(' · ') })];
      if (n.gone) lines.push(h('span', { class: 'bad-t', text: n.block ? (s && s.state !== 'down' ? s.state : 'not linked') + (s && s.retry ? ' · tries again ' + env.ends(s.retry) : '') : 'not linked now' }));
      else lines.push(h('span', { text: plural(n.users, 'person', 'people') + (n.self ? '' : n.lag >= 1 ? ' · lag ' + Math.round(n.lag) + ' ms' : n.lag > 0 ? ' · lag under 1 ms' : '') }));
      if (s && s.since && s.state === 'up') lines.push(h('span', { class: 'lbl', text: 'linked since ' + env.when(s.since) }));
      if (n.place && n.place.from) lines.push(h('span', { class: 'lbl', text: 'Placed by ' + n.place.from + (st.can ? '; Move puts it where you say' : '') }));
      const acts = h('div', { class: 'nm-acts' });
      if (st.can && n.block && !n.self) {
        if (s && s.state === 'up') {
          const b = h('button', { type: 'button', class: 'btn bad', text: 'Split' });
          b.addEventListener('click', () => busy(b, async () => { try { toast([(await api('POST', '/api/link-split', { name: n.block.name, reason: '' })).done.text]); opt.reload(); } catch (e) { toastErr(e.message); } }));
          acts.append(b);
        } else if (!n.block.host) {
          lines.push(h('span', { class: 'lbl', text: 'It connects to us by itself.' }));
        } else {
          const b = h('button', { type: 'button', class: 'btn main', text: 'Connect now' });
          b.addEventListener('click', () => busy(b, async () => { try { toast([(await api('POST', '/api/link-connect', { name: n.block.name })).done.text]); setTimeout(opt.reload, 2500); } catch (e) { toastErr(e.message); } }));
          acts.append(b);
        }
      }
      if (st.can) {
        const mv = h('button', { type: 'button', class: 'btn', text: n.place ? 'Move' : 'Place' });
        mv.addEventListener('click', () => startPlacing(n));
        acts.append(mv);
      }
      const close = h('button', { type: 'button', class: 'nm-x', 'aria-label': 'Close' }, '×');
      close.addEventListener('click', () => { st.sel = null; drawCard(); redraw(); base.focus(); });
      put(card, close, ...lines, acts.childNodes.length ? acts : null);
      card.hidden = !n.place; positionCard();
    }

    // ---------- placing ----------
    const placeName = h('input', { class: 'in', id: 'nm-place', type: 'text', maxlength: 60, autocomplete: 'off', placeholder: 'City, like Frankfurt' });
    const placingSave = h('button', { type: 'submit', class: 'btn main', text: 'Save place' });
    const placingCancel = h('button', { type: 'button', class: 'btn ghost', text: 'Cancel' });
    const placingRemove = h('button', { type: 'button', class: 'btn ghost', text: 'Take off the map' });
    const placingText = h('span');
    put(placing, placingText, h('label', { class: 'f', for: 'nm-place' }, 'Name of the place', placeName), h('div', { class: 'nm-acts' }, placingRemove, placingCancel, placingSave));
    function startPlacing(n) {
      st.placing = n; st.pin = n.place ? { lon: n.place.lon, lat: n.place.lat } : null; st.sel = null; card.hidden = true;
      put(placingText, h('b', { text: (n.place ? 'Moving ' : 'Placing ') + n.name }), ' · click the map where it is');
      placeName.value = n.place ? n.place.name : ''; placingSave.disabled = !st.pin; placingRemove.hidden = !n.place || !!n.place.from;
      placing.hidden = false; base.style.cursor = 'crosshair'; redraw(); placeName.focus();
    }
    function stopPlacing() { st.placing = null; st.pin = null; placing.hidden = true; base.style.cursor = ''; redraw(); }
    placingCancel.addEventListener('click', stopPlacing);
    placing.addEventListener('submit', e => {
      e.preventDefault(); if (!st.pin) return;
      busy(placingSave, async () => {
        try {
          const lat = Math.round(st.pin.lat * 1e4) / 1e4, lon = Math.round(st.pin.lon * 1e4) / 1e4;
          toast([(await api('POST', '/api/map-place', { name: st.placing.name, place: placeName.value.trim(), lat, lon })).done.text]);
          stopPlacing(); opt.reload();
        } catch (err) { toastErr(err.message); }
      });
    });
    placingRemove.addEventListener('click', () => busy(placingRemove, async () => {
      try { toast([(await api('POST', '/api/map-place', { name: st.placing.name })).done.text]); stopPlacing(); opt.reload(); } catch (err) { toastErr(err.message); }
    }));
    function drawUnplaced() {
      const left = st.nodes.filter(n => !n.place);
      if (!left.length) { unplaced.hidden = true; return; }
      unplaced.hidden = false;
      put(unplaced, h('span', { class: 'lbl', text: 'Not on the map yet:' }), ...left.slice(0, 40).map(n => {
        if (!st.can) return h('span', { class: 'mono nm-u', text: n.name });
        const b = h('button', { type: 'button', class: 'nm-chip', text: n.name, title: 'Place ' + n.name });
        b.addEventListener('click', () => startPlacing(n)); return b;
      }), left.length > 40 ? h('span', { class: 'lbl', text: '+' + (left.length - 40) }) : null,
      h('span', { class: 'lbl', text: st.can ? 'BeeIRCd servers place themselves from their time zone; a server set to UTC needs you to place it. Saved in beeircd.conf, nothing is looked up online.' : 'An oper with flag c places servers.' }));
    }

    // ---------- search ----------
    search.addEventListener('input', () => {
      const q = search.value.trim().toLowerCase();
      if (!q) { found.hidden = true; return; }
      const list = st.nodes.filter(n => n.name.toLowerCase().includes(q) || (n.place && n.place.name.toLowerCase().includes(q))).slice(0, 8);
      put(found, ...(list.length ? list.map(n => { const b = h('button', { type: 'button', role: 'option' }, h('span', { class: 'mono', text: n.name }), h('span', { class: 'lbl', text: n.place ? n.place.name : 'not on the map' })); b.addEventListener('click', () => pick(n)); return b; }) : [h('span', { class: 'lbl nm-none', text: 'No server called that' })]));
      found.hidden = false;
    });
    search.addEventListener('keydown', e => { if (e.key === 'Enter') { const b = found.querySelector('button'); if (b) b.click(); } if (e.key === 'Escape') { found.hidden = true; } });
    function pick(n) {
      found.hidden = true; search.value = '';
      if (st.filter !== 'all' && !shown(n)) { st.filter = 'all'; chips.forEach((c, i) => c.setAttribute('aria-pressed', i ? 'false' : 'true')); }
      st.sel = n;
      if (n.place) { fly({ lon: n.place.lon, lat: n.place.lat }, Math.max(st.view.z, 4)); setTimeout(drawCard, reduced() ? 0 : 460); }
      else if (st.can) startPlacing(n);
      else toast([n.name + " isn't on the map yet."]);
    }

    // ---------- life ----------
    const ro = new ResizeObserver(() => { if (size()) redraw(); });
    ro.observe(stage);
    if (!world.lo) loadWorld('110').then(p => { world.lo = p; redraw(); }).catch(() => toastErr("The map outlines didn't load."));
    return {
      el,
      update(data) {
        st.data = data; build(); size();
        if (st.sel) st.sel = st.nodes.find(n => n.key === st.sel.key) || null;
        if (!st.fitted && st.cw) { st.fitted = true; const ps = st.nodes.filter(n => n.place); if (ps.length) fit(ps); }
        drawCard(); redraw();
      },
      destroy() { ro.disconnect(); cancelAnimationFrame(st.anim); },
    };
  }
  return { create };
};
