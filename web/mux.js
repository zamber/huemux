// Mux page: a lane mixer over /api/mux. The page never computes an effect;
// it edits a document and renders the server's preview snapshot. That keeps
// the Go primitives the single source of truth and makes the UI testable
// against a fake document.
(function () {
  'use strict';

  var state = {
    catalog: { sources: [], effects: [], blends: [] },
    doc: null,
    lights: [],
    presets: [],
    snapshot: null,
    ready: false,
    error: '',
    polling: false,
    pushing: false,
    pushAgain: false,
    saveTimer: null,
    saveState: '',
    pollTimer: null,
    sheetTimer: null
  };

  var els = {};

  function $(id) { return document.getElementById(id); }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function clone(v) { return JSON.parse(JSON.stringify(v)); }

  function authHeaders() {
    var h = { 'Content-Type': 'application/json' };
    try {
      var t = localStorage.getItem('huemux-auth-token');
      if (t) h['Authorization'] = 'Bearer ' + t;
    } catch (_) {}
    return h;
  }

  function api(path, opts) {
    opts = opts || {};
    var headers = authHeaders();
    if (opts.headers) {
      Object.keys(opts.headers).forEach(function (k) { headers[k] = opts.headers[k]; });
    }
    opts.headers = headers;
    return fetch(path, opts).then(function (r) {
      if (r.status === 204) return null;
      if (!r.ok) {
        return r.text().then(function (t) {
          throw new Error((t || 'HTTP ' + r.status).trim());
        });
      }
      return r.json();
    });
  }

  function toast(msg, isErr) {
    var t = els.toast;
    t.textContent = msg;
    t.className = 'show' + (isErr ? ' err' : '');
    clearTimeout(t._timer);
    t._timer = setTimeout(function () { t.className = ''; }, 2800);
  }

  function fmt(v) {
    v = Number(v);
    if (!isFinite(v)) v = 0;
    if (Math.abs(v) >= 10) return String(Math.round(v * 10) / 10);
    return (Math.round(v * 100) / 100).toFixed(2);
  }

  function hex6(v) {
    v = String(v || '#000000').trim();
    if (v[0] !== '#') v = '#' + v;
    if (v.length === 4) v = '#' + v[1] + v[1] + v[2] + v[2] + v[3] + v[3];
    if (v.length !== 7) v = '#000000';
    return v.toLowerCase();
  }

  function uid() {
    return 'lane-' + Math.random().toString(36).slice(2, 9);
  }

  function slugify(name) {
    return String(name || '')
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '')
      .slice(0, 48);
  }

  function meta(kind, type) {
    var list = state.catalog[kind] || [];
    for (var i = 0; i < list.length; i++) {
      if (list[i].type === type) return list[i];
    }
    return null;
  }

  function makeNode(kind, type) {
    var m = meta(kind, type);
    var params = {};
    if (m && m.params) {
      m.params.forEach(function (p) {
        if (p.default !== undefined) params[p.name] = clone(p.default);
      });
    }
    return { type: type, params: params };
  }

  function fillNode(node, kind) {
    if (!node || typeof node !== 'object') node = {};
    var list = state.catalog[kind] || [];
    if (!node.type && list.length) node.type = list[0].type;
    if (!node.params || typeof node.params !== 'object') node.params = {};
    var m = meta(kind, node.type);
    if (!m && list.length) {
      node.type = list[0].type;
      m = list[0];
    }
    if (m && m.params) {
      m.params.forEach(function (p) {
        if (node.params[p.name] === undefined && p.default !== undefined) {
          node.params[p.name] = clone(p.default);
        }
      });
    }
    return node;
  }

  function normalizeLane(lane, i) {
    if (!lane || typeof lane !== 'object') lane = {};
    if (!lane.id) lane.id = uid();
    if (typeof lane.enabled !== 'boolean') lane.enabled = true;
    if (!lane.name) lane.name = 'Layer ' + (i + 1);
    if (typeof lane.level !== 'number') lane.level = 1;
    lane.level = Math.max(0, Math.min(1, lane.level));
    if (!lane.blend) lane.blend = i === 0 ? 'replace' : 'add';
    if (!Array.isArray(lane.targets)) lane.targets = [];
    var known = {};
    state.lights.forEach(function (l) { known[l.id] = true; });
    lane.targets = lane.targets.filter(function (id) { return known[id]; });
    lane.source = fillNode(lane.source, 'sources');
    lane.effect = fillNode(lane.effect, 'effects');
    return lane;
  }

  function normalizeDoc(doc) {
    if (!doc || typeof doc !== 'object') doc = {};
    doc.version = 1;
    doc.name = doc.name || 'Salon mux';
    doc.room = doc.room || 'Salon';
    if (!doc.master || typeof doc.master !== 'object') doc.master = {};
    if (typeof doc.master.brightness !== 'number') doc.master.brightness = 0.9;
    doc.master.brightness = Math.max(0, Math.min(1, doc.master.brightness));
    if (!doc.output || typeof doc.output !== 'object') doc.output = {};
    if (!doc.output.hz) doc.output.hz = 4;
    if (!doc.output.transition_ms) doc.output.transition_ms = 240;
    if (!Array.isArray(doc.lanes)) doc.lanes = [];
    if (!Array.isArray(doc.lights) || !doc.lights.length) {
      doc.lights = state.lights.map(function (l) { return l.id; });
    }
    doc.lanes.forEach(normalizeLane);
    return doc;
  }

  // ── preview ───────────────────────────────────────────────────

  function buildLamps() {
    var stage = els.stage;
    clear(stage);
    stage._lamps = {};
    stage.classList.toggle('is-empty', state.lights.length === 0);
    state.lights.forEach(function (l) {
      var d = el('div', 'lamp');
      d.style.left = (Math.max(0.06, Math.min(0.94, l.x)) * 100) + '%';
      d.style.top = (Math.max(0.06, Math.min(0.94, l.y)) * 100) + '%';
      d.textContent = l.name || '';
      d.title = l.name || l.id;
      stage.appendChild(d);
      stage._lamps[l.id] = d;
    });
  }

  function updateLamps() {
    var stage = els.stage;
    var byId = {};
    if (state.snapshot && Array.isArray(state.snapshot.lights)) {
      state.snapshot.lights.forEach(function (l) { byId[l.id] = l; });
    }
    Object.keys(stage._lamps || {}).forEach(function (id) {
      var lamp = stage._lamps[id];
      var s = byId[id];
      var hex = s && s.on ? s.hex : '#000000';
      lamp.style.background = hex;
      lamp.style.boxShadow = s && s.on ? '0 0 15px 2px ' + hex : 'none';
      lamp.style.opacity = s && s.on ? String(0.32 + 0.68 * (s.brightness || 0)) : '0.18';
      lamp.classList.toggle('off', !(s && s.on));
    });
  }

  // ── status ────────────────────────────────────────────────────

  function renderStatus() {
    var running = !!(state.snapshot && state.snapshot.running);
    var dot = els.dot;
    dot.className = 'dot' + (!state.ready ? ' err' : (running ? ' on' : ' ready'));
    els.statusText.textContent = !state.ready ? 'Not paired' : (running ? 'Live to Salon' : 'Preview only');
    els.run.textContent = running ? 'Stop' : 'Start';
    els.run.disabled = !state.ready;
    els.off.disabled = !state.ready;
    els.saveState.textContent = state.saveState || '';
    var err = state.error || (state.snapshot && state.snapshot.error) || '';
    if (err) {
      els.error.textContent = err;
      els.error.hidden = false;
    } else {
      els.error.hidden = true;
    }
  }

  function renderMaster() {
    if (!state.doc) return;
    var v = Math.round(state.doc.master.brightness * 100);
    els.master.value = String(v);
    els.masterOut.textContent = v + '%';
  }

  // ── lanes ─────────────────────────────────────────────────────

  function renderLanes() {
    clear(els.lanes);
    var lanes = state.doc ? state.doc.lanes : [];
    els.empty.hidden = lanes.length !== 0;
    lanes.forEach(function (lane, i) {
      els.lanes.appendChild(laneCard(lane, i));
    });
  }

  function selectField(label, options, value, onChange) {
    var wrap = el('label', 'field');
    wrap.appendChild(el('span', null, label));
    var sel = el('select');
    options.forEach(function (o) {
      var opt = el('option', null, o.label);
      opt.value = o.value;
      sel.appendChild(opt);
    });
    sel.value = value;
    sel.onchange = function () { onChange(sel.value); };
    wrap.appendChild(sel);
    return wrap;
  }

  function catalogueOptions(kind) {
    return (state.catalog[kind] || []).map(function (m) {
      return { value: m.type, label: m.label || m.type };
    });
  }

  function sourceValueOptions(current) {
    var known = ['', 'value', 'phase', 'beat', 'bass', 'mid', 'treble', 'energy', 'r', 'g', 'b'];
    if (current && known.indexOf(current) === -1) known.push(current);
    return known.map(function (v) {
      return { value: v, label: v === '' ? 'Steady / clock' : v };
    });
  }

  function paramField(node, spec) {
    var wrap = el('div', 'param');
    wrap.appendChild(el('span', 'param-label', spec.label || spec.name));
    var input;
    var out;

    if (spec.type === 'number') {
      input = el('input');
      input.type = 'range';
      input.min = String(spec.min !== undefined ? spec.min : 0);
      input.max = String(spec.max !== undefined ? spec.max : 1);
      input.step = String(spec.step || 0.01);
      var v = Number(node.params[spec.name]);
      if (!isFinite(v)) v = Number(spec.default || 0);
      input.value = String(v);
      out = el('output', 'param-value', fmt(v));
      input.oninput = function () {
        node.params[spec.name] = parseFloat(input.value);
        out.textContent = fmt(input.value);
        schedulePush();
      };
      wrap.appendChild(input);
      wrap.appendChild(out);
    } else if (spec.type === 'color') {
      input = el('input');
      input.type = 'color';
      input.value = hex6(node.params[spec.name] || spec.default || '#ffffff');
      input.oninput = function () {
        node.params[spec.name] = input.value.toUpperCase();
        schedulePush();
      };
      wrap.appendChild(input);
    } else if (spec.type === 'bool') {
      input = el('input');
      input.type = 'checkbox';
      input.checked = node.params[spec.name] !== false;
      input.onchange = function () {
        node.params[spec.name] = input.checked;
        schedulePush();
      };
      wrap.appendChild(input);
    } else if (spec.type === 'enum') {
      input = el('select');
      (spec.choices || []).forEach(function (c) {
        var opt = el('option', null, c.label || c.value);
        opt.value = c.value;
        input.appendChild(opt);
      });
      input.value = String(node.params[spec.name]);
      input.onchange = function () {
        node.params[spec.name] = input.value;
        schedulePush();
      };
      wrap.appendChild(input);
    } else if (spec.name === 'source') {
      input = el('select');
      sourceValueOptions(String(node.params[spec.name] || '')).forEach(function (o) {
        var opt = el('option', null, o.label);
        opt.value = o.value;
        input.appendChild(opt);
      });
      input.value = String(node.params[spec.name] || '');
      input.onchange = function () {
        node.params[spec.name] = input.value;
        schedulePush();
      };
      wrap.appendChild(input);
    } else {
      input = el('input');
      input.type = 'text';
      input.value = String(node.params[spec.name] !== undefined ? node.params[spec.name] : '');
      input.oninput = function () {
        node.params[spec.name] = input.value;
        schedulePush();
      };
      wrap.appendChild(input);
    }
    return wrap;
  }

  function levelField(lane) {
    var wrap = el('div', 'param');
    wrap.appendChild(el('span', 'param-label', 'Layer level'));
    var input = el('input');
    input.type = 'range';
    input.min = '0';
    input.max = '1';
    input.step = '0.01';
    input.value = String(lane.level);
    var out = el('output', 'param-value', fmt(lane.level));
    input.oninput = function () {
      lane.level = parseFloat(input.value);
      out.textContent = fmt(lane.level);
      schedulePush();
    };
    wrap.appendChild(input);
    wrap.appendChild(out);
    return wrap;
  }

  function laneCard(lane, index) {
    var card = el('article', 'lane' + (lane.enabled ? '' : ' disabled'));
    card.dataset.id = lane.id;

    var head = el('div', 'lane-head');

    var order = el('div', 'lane-order');
    var up = el('button', 'icon-btn', '▲');
    up.type = 'button';
    up.title = 'Move up';
    up.disabled = index === 0;
    up.onclick = function () { moveLane(index, -1); };
    var down = el('button', 'icon-btn', '▼');
    down.type = 'button';
    down.title = 'Move down';
    down.disabled = index === state.doc.lanes.length - 1;
    down.onclick = function () { moveLane(index, 1); };
    order.appendChild(up);
    order.appendChild(down);
    head.appendChild(order);

    var name = el('input', 'lane-name');
    name.type = 'text';
    name.value = lane.name;
    name.setAttribute('aria-label', 'Layer name');
    name.oninput = function () {
      lane.name = name.value;
      schedulePush();
    };
    head.appendChild(name);

    var sw = el('label', 'switch');
    var cb = el('input');
    cb.type = 'checkbox';
    cb.checked = lane.enabled;
    cb.onchange = function () {
      lane.enabled = cb.checked;
      card.classList.toggle('disabled', !lane.enabled);
      schedulePush();
    };
    sw.appendChild(cb);
    sw.appendChild(el('span', 'switch-track'));
    sw.appendChild(el('span', 'switch-label', 'On'));
    head.appendChild(sw);

    var del = el('button', 'icon-btn danger', '✕');
    del.type = 'button';
    del.title = 'Delete layer';
    del.onclick = function () { deleteLane(index); };
    head.appendChild(del);
    card.appendChild(head);

    var selects = el('div', 'lane-selects');
    selects.appendChild(selectField('Source', catalogueOptions('sources'), lane.source.type, function (v) {
      lane.source = makeNode('sources', v);
      renderLanes();
      schedulePush();
    }));
    selects.appendChild(selectField('Effect', catalogueOptions('effects'), lane.effect.type, function (v) {
      lane.effect = makeNode('effects', v);
      renderLanes();
      schedulePush();
    }));
    card.appendChild(selects);

    var em = meta('effects', lane.effect.type);
    if (em && em.params && em.params.length) {
      var params = el('div', 'lane-params');
      em.params.forEach(function (spec) {
        params.appendChild(paramField(lane.effect, spec));
      });
      card.appendChild(params);
    }

    var sm = meta('sources', lane.source.type);
    if (sm && sm.params && sm.params.length) {
      var more = el('details', 'more');
      more.appendChild(el('summary', null, 'Source settings'));
      var sp = el('div', 'lane-params');
      sm.params.forEach(function (spec) {
        sp.appendChild(paramField(lane.source, spec));
      });
      more.appendChild(sp);
      card.appendChild(more);
    }

    var mix = el('div', 'lane-mix');
    mix.appendChild(selectField('Blend', (state.catalog.blends || []).map(function (b) {
      return { value: b, label: b.charAt(0).toUpperCase() + b.slice(1) };
    }), lane.blend, function (v) {
      lane.blend = v;
      schedulePush();
    }));
    mix.appendChild(levelField(lane));
    card.appendChild(mix);

    card.appendChild(targetsBlock(lane));
    return card;
  }

  function targetsBlock(lane) {
    var det = el('details', 'targets');
    var summary = el('summary', null, lane.targets.length ? 'Lights: ' + lane.targets.length : 'Lights: all');
    det.appendChild(summary);
    if (!state.lights.length) {
      det.appendChild(el('p', 'empty', 'No lights resolved.'));
      return det;
    }
    var grid = el('div', 'target-grid');
    var boxes = [];
    state.lights.forEach(function (l) {
      var label = el('label', 'target');
      var cb = el('input');
      cb.type = 'checkbox';
      cb.checked = lane.targets.length === 0 || lane.targets.indexOf(l.id) !== -1;
      boxes.push({ id: l.id, cb: cb });
      cb.onchange = function () {
        var checked = boxes.filter(function (b) { return b.cb.checked; }).map(function (b) { return b.id; });
        lane.targets = checked.length === state.lights.length ? [] : checked;
        summary.textContent = lane.targets.length ? 'Lights: ' + lane.targets.length : 'Lights: all';
        schedulePush();
      };
      label.appendChild(cb);
      label.appendChild(el('span', null, l.name || l.id));
      grid.appendChild(label);
    });
    det.appendChild(grid);
    return det;
  }

  function moveLane(index, delta) {
    var lanes = state.doc.lanes;
    var to = index + delta;
    if (to < 0 || to >= lanes.length) return;
    var tmp = lanes[index];
    lanes[index] = lanes[to];
    lanes[to] = tmp;
    renderLanes();
    pushDoc();
  }

  function deleteLane(index) {
    state.doc.lanes.splice(index, 1);
    renderLanes();
    pushDoc();
  }

  // ── persistence ───────────────────────────────────────────────

  function schedulePush() {
    if (!state.doc) return;
    state.saveState = 'Unsaved';
    renderStatus();
    clearTimeout(state.saveTimer);
    state.saveTimer = setTimeout(pushDoc, 320);
  }

  function pushDoc() {
    if (!state.doc || !state.ready) return;
    if (state.pushing) {
      state.pushAgain = true;
      return;
    }
    state.pushing = true;
    api('/api/mux', { method: 'PUT', body: JSON.stringify({ doc: state.doc }) })
      .then(function () {
        state.saveState = 'Saved';
        renderStatus();
      })
      .catch(function (e) {
        state.saveState = 'Save failed';
        state.error = e.message;
        renderStatus();
        toast('Save failed: ' + e.message, true);
      })
      .then(function () {
        state.pushing = false;
        if (state.pushAgain) {
          state.pushAgain = false;
          pushDoc();
        }
      });
  }

  function refreshPresets() {
    return api('/api/mux/presets')
      .then(function (list) { state.presets = list || []; })
      .catch(function () {});
  }

  // ── polling / run ─────────────────────────────────────────────

  function pollOnce() {
    if (state.polling || !state.ready) return;
    state.polling = true;
    api('/api/mux/state')
      .then(function (s) {
        if (s) {
          state.snapshot = s;
          state.error = '';
          updateLamps();
          renderStatus();
        }
      })
      .catch(function () {})
      .then(function () { state.polling = false; });
  }

  function startPolling() {
    clearInterval(state.pollTimer);
    state.pollTimer = setInterval(pollOnce, document.hidden ? 700 : 200);
    pollOnce();
  }

  function runToggle() {
    var running = !!(state.snapshot && state.snapshot.running);
    api(running ? '/api/mux/stop' : '/api/mux/start', { method: 'POST' })
      .then(function () { pollOnce(); })
      .catch(function (e) { toast(e.message, true); });
  }

  function allOff() {
    if (!window.confirm('Turn all Salon lamps off?')) return;
    api('/api/mux/off', { method: 'POST' })
      .then(function () { pollOnce(); toast('Salon off'); })
      .catch(function (e) { toast(e.message, true); });
  }

  // ── sheet ─────────────────────────────────────────────────────

  function openSheet(title, content) {
    clearTimeout(state.sheetTimer);
    els.sheetTitle.textContent = title;
    clear(els.sheetBody);
    els.sheetBody.appendChild(content);
    els.sheet.hidden = false;
    els.backdrop.hidden = false;
    requestAnimationFrame(function () {
      els.sheet.classList.add('open');
      els.backdrop.classList.add('open');
    });
  }

  function closeSheet() {
    els.sheet.classList.remove('open');
    els.backdrop.classList.remove('open');
    clearTimeout(state.sheetTimer);
    state.sheetTimer = setTimeout(function () {
      els.sheet.hidden = true;
      els.backdrop.hidden = true;
    }, 230);
  }

  function addLaneSheet() {
    var list = el('div', 'option-list');
    var options = [
      { name: 'Warm pulse', desc: 'A slow breathing colour.', src: 'timer', eff: 'pulse' },
      { name: 'Gradient', desc: 'Two colours across the room.', src: 'timer', eff: 'gradient' },
      { name: 'Chase', desc: 'A head travelling through the lights.', src: 'timer', eff: 'chase' },
      { name: 'Sparkle', desc: 'Deterministic twinkles.', src: 'timer', eff: 'sparkle' },
      { name: 'Rainbow', desc: 'A rotating hue ramp.', src: 'timer', eff: 'rainbow' },
      { name: 'Sound pulse', desc: 'Drive brightness from bass.', src: 'sound', eff: 'pulse' },
      { name: 'Video map', desc: 'Pass screen colours through.', src: 'video', eff: 'video_map' },
      { name: 'Warm solid', desc: 'A steady wash.', src: 'constant', eff: 'solid' }
    ];
    options.forEach(function (o) {
      var b = el('button', 'option');
      b.type = 'button';
      var left = el('span');
      left.appendChild(el('strong', null, o.name));
      left.appendChild(el('small', null, o.desc));
      b.appendChild(left);
      b.appendChild(el('span', 'go', '＋'));
      b.onclick = function () {
        var lane = normalizeLane({
          id: uid(), name: o.name, enabled: true,
          source: makeNode('sources', o.src),
          effect: makeNode('effects', o.eff),
          blend: state.doc.lanes.length ? 'add' : 'replace',
          level: 1, targets: []
        }, state.doc.lanes.length);
        state.doc.lanes.push(lane);
        closeSheet();
        renderLanes();
        pushDoc();
      };
      list.appendChild(b);
    });
    openSheet('Add a layer', list);
  }

  function presetsSheet() {
    var body = el('div');
    var list = el('div', 'option-list');
    refreshPresets().then(function () {
      if (!state.presets.length) {
        list.appendChild(el('p', 'empty', 'No presets yet.'));
      }
      state.presets.forEach(function (p) {
        var row = el('div', 'preset-row');
        var b = el('button', 'option');
        b.type = 'button';
        var left = el('span');
        left.appendChild(el('strong', null, p.name || p.slug));
        left.appendChild(el('small', null, p.slug));
        b.appendChild(left);
        if (p.builtin) {
          b.appendChild(el('span', 'badge', 'built-in'));
        } else {
          b.appendChild(el('span', 'go', '→'));
        }
        b.onclick = function () { loadPreset(p.slug); };
        row.appendChild(b);
        if (!p.builtin) {
          var del = el('button', 'icon-btn danger', '✕');
          del.type = 'button';
          del.title = 'Delete preset';
          del.onclick = function () { deletePreset(p.slug); };
          row.appendChild(del);
        }
        list.appendChild(row);
      });
      body.appendChild(list);
      var actions = el('div', 'sheet-actions');
      var save = el('button', 'btn primary', 'Save current as…');
      save.type = 'button';
      save.onclick = saveAs;
      actions.appendChild(save);
      body.appendChild(actions);
      openSheet('Presets', body);
    });
  }

  function loadPreset(slug) {
    api('/api/mux/presets/' + encodeURIComponent(slug))
      .then(function (doc) {
        state.doc = normalizeDoc(doc);
        state.saveState = '';
        render();
        pushDoc();
        closeSheet();
        toast('Loaded ' + slug);
      })
      .catch(function (e) { toast(e.message, true); });
  }

  function deletePreset(slug) {
    if (!window.confirm('Delete preset "' + slug + '"?')) return;
    api('/api/mux/presets/' + encodeURIComponent(slug), { method: 'DELETE' })
      .then(function () { refreshPresets(); closeSheet(); toast('Deleted ' + slug); })
      .catch(function (e) { toast(e.message, true); });
  }

  function saveAs() {
    var name = window.prompt('Preset name', state.doc.name || 'Salon mux');
    if (!name) return;
    var slug = slugify(name) || ('mux-' + Date.now());
    state.doc.name = name;
    api('/api/mux', { method: 'PUT', body: JSON.stringify({ doc: state.doc, slug: slug }) })
      .then(function () {
        state.saveState = 'Saved';
        render();
        refreshPresets();
        closeSheet();
        toast('Saved ' + name);
      })
      .catch(function (e) { toast(e.message, true); });
  }

  // ── boot ──────────────────────────────────────────────────────

  function render() {
    if (!state.doc) return;
    els.docName.textContent = state.doc.name || 'Mux';
    renderMaster();
    updateLamps();
    renderLanes();
    renderStatus();
  }

  function bind() {
    els.run.onclick = runToggle;
    els.add.onclick = addLaneSheet;
    els.presets.onclick = presetsSheet;
    els.save.onclick = saveAs;
    els.off.onclick = allOff;
    els.sheetClose.onclick = closeSheet;
    els.backdrop.onclick = closeSheet;
    els.master.oninput = function () {
      if (!state.doc) return;
      var v = parseFloat(els.master.value) / 100;
      state.doc.master.brightness = v;
      els.masterOut.textContent = Math.round(v * 100) + '%';
      schedulePush();
    };
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') closeSheet();
    });
    document.addEventListener('visibilitychange', function () {
      clearInterval(state.pollTimer);
      state.pollTimer = setInterval(pollOnce, document.hidden ? 700 : 200);
      pollOnce();
    });
  }

  function boot() {
    els = {
      docName: $('doc-name'),
      stage: $('lamp-stage'),
      dot: $('status-dot'),
      statusText: $('status-text'),
      saveState: $('save-state'),
      error: $('error-line'),
      master: $('master'),
      masterOut: $('master-out'),
      run: $('btn-run'),
      off: $('btn-off'),
      add: $('add-lane'),
      presets: $('btn-presets'),
      save: $('btn-save'),
      lanes: $('lanes'),
      empty: $('empty-state'),
      sheet: $('sheet'),
      sheetTitle: $('sheet-title'),
      sheetBody: $('sheet-body'),
      sheetClose: $('sheet-close'),
      backdrop: $('sheet-backdrop'),
      toast: $('toast')
    };
    bind();
    api('/api/mux')
      .then(function (data) {
        state.catalog = data.catalog || state.catalog;
        state.lights = data.lights || [];
        state.presets = data.presets || [];
        state.ready = !!data.ready;
        state.error = data.error || '';
        state.snapshot = data.status || null;
        state.doc = normalizeDoc(data.doc || {});
        buildLamps();
        render();
        startPolling();
      })
      .catch(function (e) {
        state.error = e.message;
        renderStatus();
        toast('Could not load mux: ' + e.message, true);
      });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
