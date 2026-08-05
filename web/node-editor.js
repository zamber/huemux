// HueMux Presets — node editor (tablet/desktop) + card browser (phone) + tweak panel.
// Litegraph.js integration. All Litegraph calls live here.
(function () {
  'use strict';

  const $ = function (s) { return document.querySelector(s); };
  const WS_URL = authWSURL('/ws');
  const isPhone = function () { return window.innerWidth < 768; };
  const isTablet = function () { return window.innerWidth >= 768 && window.innerWidth < 1024; };

  // ── State ────────────────────────────────────────────────────────────────
  var ws, catalog = [], currentSlug = '', activeSlug = '', graphDirty = false;
  var graph, canvas;
  var catColor = {
    source: '#4488cc', analysis: '#44aa66', routing: '#ccaa44',
    effect: '#cc4444', modulation: '#8844cc', output_effect: '#cc4488'
  };

  // ── WS connection ────────────────────────────────────────────────────────
  function connectWS() {
    if (ws) { try { ws.close(); } catch (e) { /* ok */ } }
    ws = new WebSocket(WS_URL);
    ws.binaryType = 'arraybuffer';
    ws.onopen = function () { $('#conn-dot').classList.add('connected'); updateStatus('Connected'); };
    ws.onclose = function () { $('#conn-dot').classList.remove('connected'); updateStatus('Disconnected — reload page'); };
    ws.onmessage = function (e) {
      if (typeof e.data === 'string') {
        try { handleMsg(JSON.parse(e.data)); } catch (_) {}
      }
    };
  }
  function send(msg) { if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg)); }
  function updateStatus(s) { $('#status-bar').textContent = s; }

  function handleMsg(msg) {
    if (msg.type === 'status' && msg.snapshot) {
      var slug = msg.snapshot.MusicPreset || '';
      if (slug !== activeSlug) {
        activeSlug = slug;
        updateButtons();
        if (isPhone()) renderCards();
      }
    }
    if (msg.type === 'debug' && msg.nodes && !isPhone()) {
      updateNodeBadges(msg.nodes);
    }
  }

  function updateButtons() {
    var btn = $('#btn-activate'), deact = $('#btn-deactivate'), badge = $('#live-badge');
    if (activeSlug) {
      btn.disabled = true; deact.disabled = false; badge.hidden = false;
    } else {
      btn.disabled = false; deact.disabled = true; badge.hidden = true;
    }
  }

  // ── Catalog → palette ────────────────────────────────────────────────────
  async function loadCatalog() {
    try {
      var resp = await fetch('/api/presets/catalog');
      catalog = await resp.json();
      buildPalette();
      registerNodeTypes();
    } catch (e) {
      $('#palette').innerHTML = '<p style="color:var(--ink-dim);padding:8px">Catalog unavailable.</p>';
    }
  }

  function buildPalette() {
    var palette = $('#palette');
    palette.innerHTML = '';
    var cats = {};
    catalog.forEach(function (m) {
      if (!cats[m.category]) cats[m.category] = [];
      cats[m.category].push(m);
    });
    var labels = { source: 'Sources', analysis: 'Analysis', routing: 'Routing', effect: 'Effects', modulation: 'Modulation', output_effect: 'Output Effects' };
    Object.keys(labels).forEach(function (cat) {
      if (!cats[cat] || !cats[cat].length) return;
      var h3 = document.createElement('h3'); h3.textContent = labels[cat]; palette.appendChild(h3);
      cats[cat].forEach(function (m) {
        var el = document.createElement('div');
        el.className = 'node-item cat-' + cat;
        el.textContent = m.label;
        el.title = m.description;
        el.setAttribute('data-type', m.type);

        // Pointer-based drag (works on touch)
        var dragging = false, startX, startY, clone;
        el.addEventListener('pointerdown', function (e) {
          if (e.pointerType === 'mouse' && e.button !== 0) return;
          dragging = true; startX = e.clientX; startY = e.clientY;
          el.setPointerCapture(e.pointerId);
          e.preventDefault();
        });
        el.addEventListener('pointermove', function (e) {
          if (!dragging) return;
          if (!clone && (Math.abs(e.clientX - startX) > 3 || Math.abs(e.clientY - startY) > 3)) {
            clone = el.cloneNode(true); clone.style.position = 'fixed'; clone.style.zIndex = '100'; clone.style.opacity = '0.8'; clone.style.pointerEvents = 'none'; document.body.appendChild(clone);
          }
          if (clone) { clone.style.left = (e.clientX - 20) + 'px'; clone.style.top = (e.clientY - 10) + 'px'; }
        });
        el.addEventListener('pointerup', function (e) {
          if (!dragging) return;
          dragging = false;
          if (clone) { clone.remove(); clone = null; }
          el.releasePointerCapture(e.pointerId);
          var rect = $('#graph-canvas').getBoundingClientRect();
          if (e.clientX >= rect.left && e.clientX <= rect.right && e.clientY >= rect.top && e.clientY <= rect.bottom) {
            addNodeAt(m, e.clientX - rect.left, e.clientY - rect.top);
          }
        });
        // Fallback: tap to add at canvas center
        el.addEventListener('click', function (e) {
          if (clone) return; // was a drag
          var rect = $('#graph-canvas').getBoundingClientRect();
          addNodeAt(m, rect.width / 2, rect.height / 2);
        });
        // Also keep HTML5 DnD for desktop
        el.draggable = true;
        el.addEventListener('dragstart', function (e) {
          e.dataTransfer.setData('text/plain', m.type);
          e.dataTransfer.effectAllowed = 'copy';
        });
        palette.appendChild(el);
      });
    });
    // Drop target for HTML5 DnD
    var cw = $('#canvas-wrap');
    cw.addEventListener('dragover', function (e) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; });
    cw.addEventListener('drop', function (e) {
      e.preventDefault();
      var type = e.dataTransfer.getData('text/plain');
      if (!type) return;
      var meta = catalog.find(function (m) { return m.type === type; });
      if (!meta) return;
      var rect = $('#graph-canvas').getBoundingClientRect();
      addNodeAt(meta, e.clientX - rect.left, e.clientY - rect.top);
    });
  }

  function addNodeAt(meta, x, y) {
    if (!graph) return;
    var node = LiteGraph.createNode('preset/' + meta.type);
    if (meta.params) {
      meta.params.forEach(function (p) {
        if (p.default !== undefined) node.properties[p.name] = p.default;
      });
    }
    node.pos = [x, y];
    graph.add(node);
    graphDirty = true;
  }

  function registerNodeTypes() {
    catalog.forEach(function (meta) {
      var ctor = function () {
        if (meta.params) {
          meta.params.forEach(function (p) {
            if (p.default !== undefined) this.properties[p.name] = p.default;
          }, this);
        }
        (meta.inputs || []).forEach(function (p) {
          this.addInput(p.name, 'number');
        }, this);
        (meta.outputs || []).forEach(function (p) {
          this.addOutput(p.name, 'number');
        }, this);
      };
      ctor.title = meta.label;
      ctor.desc = meta.description;
      ctor.prototype.onDrawBackground = function (ctx) {
        if (this.flags.collapsed) return;
        ctx.fillStyle = catColor[meta.category] || '#666';
        ctx.fillRect(0, 0, 4, this.size[1]);
        if (this._badge) {
          ctx.font = '9px Fira Code, monospace';
          ctx.fillStyle = '#aaa';
          ctx.fillText(this._badge, 8, this.size[1] - 4);
        }
      };
      ctor.prototype.onSelected = function () { showInspector(this, meta); };
      ctor.prototype.onDeselected = function () { clearInspector(); };
      ctor.prototype.onPropertyChanged = function () { graphDirty = true; };
      LiteGraph.registerNodeType('preset/' + meta.type, ctor);
    });
  }

  // ── Pinch zoom ──────────────────────────────────────────────────────────
  var pinchDist = 0, pinchMid = [0, 0], pinchScale0 = 1;
  function setupPinchZoom() {
    var cv = $('#graph-canvas');
    cv.addEventListener('pointerdown', function (e) {
      if (e.pointerType !== 'touch') return;
      var pts = getActivePointers(cv);
      if (pts.length === 2) {
        pinchDist = dist(pts[0], pts[1]);
        pinchMid = mid(pts[0], pts[1]);
        pinchScale0 = canvas.ds.scale;
        e.preventDefault();
      }
    });
    cv.addEventListener('pointermove', function (e) {
      if (e.pointerType !== 'touch') return;
      var pts = getActivePointers(cv);
      if (pts.length === 2 && pinchDist > 0) {
        var d = dist(pts[0], pts[1]);
        var s = pinchScale0 * d / pinchDist;
        if (s < 0.1) s = 0.1; if (s > 5) s = 5;
        canvas.ds.changeScale(s, [pinchMid[0], pinchMid[1]]);
        canvas.setDirty(true, true);
        e.preventDefault();
      }
    });
    cv.addEventListener('pointerup', function () { pinchDist = 0; });
    cv.addEventListener('pointercancel', function () { pinchDist = 0; });
  }
  function getActivePointers(el) {
    var pts = [];
    if (el._pointer_ids) {
      el._pointer_ids.forEach(function (id) { pts.push(el._pointer_positions && el._pointer_positions[id]); });
    }
    return pts.filter(Boolean);
  }
  function dist(a, b) { var dx = a[0] - b[0], dy = a[1] - b[1]; return Math.sqrt(dx * dx + dy * dy); }
  function mid(a, b) { return [(a[0] + b[0]) / 2, (a[1] + b[1]) / 2]; }

  // ── Inspector ────────────────────────────────────────────────────────────
  var inspectedNode = null, inspectedMeta = null;

  function showInspector(node, meta) {
    inspectedNode = node; inspectedMeta = meta;
    var el = $('#inspector');
    var html = '<h3>' + escHtml(meta.label) + '</h3>';
    html += '<p style="font-size:11px;color:var(--ink-dim);margin:0 0 8px">' + escHtml(meta.description || '') + '</p>';
    if (meta.params && meta.params.length) {
      meta.params.forEach(function (p) {
        html += '<label>' + escHtml(p.label) + '</label>';
        var val = node.properties[p.name];
        if (val === undefined) val = p.default;
        if (p.type === 'bool') {
          html += '<input type="checkbox" data-param="' + p.name + '"' + (val ? ' checked' : '') + '>';
        } else if (p.choices && p.choices.length) {
          html += '<select data-param="' + p.name + '">';
          p.choices.forEach(function (c) {
            html += '<option value="' + c + '"' + (val === c ? ' selected' : '') + '>' + c + '</option>';
          });
          html += '</select>';
        } else if (p.type === 'color') {
          html += '<input type="color" data-param="' + p.name + '" value="' + (val || '#FFFFFF') + '">';
        } else if (p.type === 'light_ids') {
          html += '<input type="text" data-param="' + p.name + '" value="' + escHtml(JSON.stringify(val || [])) + '" placeholder="[]">';
        } else {
          var v = Number(val) || 0;
          html += '<input type="range" data-param="' + p.name + '" min="' + (p.min !== undefined ? p.min : 0) +
            '" max="' + (p.max !== undefined ? p.max : 1) + '" step="' + (p.step || 0.01) + '" value="' + v + '">';
          html += ' <span class="param-readout" data-readout="' + p.name + '">' + v.toFixed(2) + '</span>';
        }
        if (p.description) html += '<p style="font-size:10px;color:var(--ink-dim);margin:0">' + escHtml(p.description) + '</p>';
      });
    }
    if (node._badge) {
      html += '<div style="margin-top:8px;font-size:10px;color:var(--accent)">Live: ' + escHtml(node._badge) + '</div>';
    }
    el.innerHTML = html;
    el.querySelectorAll('[data-param]').forEach(function (ctrl) {
      ctrl.addEventListener('input', function () {
        var name = ctrl.dataset.param;
        var p = meta.params.find(function (pp) { return pp.name === name; });
        if (!p) return;
        var val;
        if (ctrl.type === 'checkbox') val = ctrl.checked;
        else if (ctrl.type === 'color') val = ctrl.value;
        else if (p.type === 'light_ids') { try { val = JSON.parse(ctrl.value); } catch (_) { val = []; } }
        else if (p.choices) val = ctrl.value;
        else val = parseFloat(ctrl.value) || 0;
        node.properties[name] = val;
        graphDirty = true;
        var ro = document.querySelector('[data-readout="' + name + '"]');
        if (ro) ro.textContent = typeof val === 'number' ? val.toFixed(2) : val;
      });
    });
  }

  function clearInspector() {
    inspectedNode = null; inspectedMeta = null;
    $('#inspector').innerHTML = '<p style="color:var(--ink-dim);font-size:12px">Select a node to inspect.</p>';
  }

  function updateNodeBadges(snaps) {
    if (!graph) return;
    snaps.forEach(function (s) {
      var node = graph._nodes.find(function (n) { return n.id === s.id; });
      if (!node) return;
      var parts = [];
      if (s.out && Object.keys(s.out).length) {
        parts.push(Object.entries(s.out).map(function (kv) { return kv[0] + '=' + kv[1].toFixed(2); }).join(' '));
      }
      if (s.lights && Object.keys(s.lights).length) {
        parts.push(Object.keys(s.lights).length + ' lights');
      }
      node._badge = parts.join(' · ') || 'active';
      if (inspectedNode === node) {
        var el = $('#inspector');
        var badgeEl = el.querySelector('div:last-child');
        if (badgeEl && badgeEl.textContent.indexOf('Live:') === 0) {
          badgeEl.textContent = 'Live: ' + node._badge;
        }
      }
    });
    if (canvas) canvas.setDirty(true, true);
  }

  // ── DAG adapter ──────────────────────────────────────────────────────────
  function graphToPreset(name, desc) {
    var nodes = []; var edges = [];
    graph._nodes.forEach(function (n) {
      nodes.push({ id: String(n.id), type: n.type.replace('preset/', ''), params: stripParams(n.properties) });
      (n.outputs || []).forEach(function (out, outIdx) {
        if (!out || !out.links) return;
        out.links.forEach(function (linkId) {
          var link = graph.links[linkId];
          if (!link) return;
          var target = graph.getNodeById(link.target_id);
          if (!target) return;
          var inPort = (target.inputs && target.inputs[link.target_slot]) ? target.inputs[link.target_slot].name : 'in';
          var outPort = out.name || 'out';
          edges.push({ from: String(n.id), out_port: outPort, to: String(target.id), in_port: inPort });
        });
      });
    });
    return { version: 1, name: name, description: desc, nodes: nodes, edges: edges };
  }

  function stripParams(props) {
    var out = {};
    Object.keys(props || {}).forEach(function (k) {
      if (props[k] !== undefined) out[k] = props[k];
    });
    return out;
  }

  function presetToGraph(doc) {
    graph.clear();
    var nodeMap = {};
    doc.nodes.forEach(function (rn) {
      var node = LiteGraph.createNode('preset/' + rn.type);
      node.id = rn.id;
      if (rn.params) Object.assign(node.properties, rn.params);
      graph.add(node);
      nodeMap[rn.id] = node;
    });
    doc.edges.forEach(function (re) {
      var from = nodeMap[re.from], to = nodeMap[re.to];
      if (!from || !to) return;
      var fromSlot = findOutputSlot(from, re.out_port);
      var toSlot = findInputSlot(to, re.in_port);
      from.connect(fromSlot, to, toSlot);
    });
    graphDirty = false;
    currentSlug = doc.name.toLowerCase().replace(/[^a-z0-9_-]/g, '_').slice(0, 64) || 'untitled';
  }

  function findOutputSlot(node, name) { for (var i = 0; i < (node.outputs || []).length; i++) { if (node.outputs[i] && node.outputs[i].name === name) return i; } return 0; }
  function findInputSlot(node, name) { for (var i = 0; i < (node.inputs || []).length; i++) { if (node.inputs[i] && node.inputs[i].name === name) return i; } return 0; }

  // ── Save / Load / Activate ───────────────────────────────────────────────
  async function savePreset() {
    var name = prompt('Preset name:', currentSlug || '');
    if (!name) return;
    var slug = name.toLowerCase().replace(/[^a-z0-9_-]/g, '_').slice(0, 64);
    var doc = graphToPreset(name, '');
    try {
      var resp = await fetch('/api/presets/' + slug, { method: 'PUT', body: JSON.stringify(doc) });
      if (!resp.ok) { var err = await resp.text(); alert('Save failed: ' + err); return; }
      currentSlug = slug; graphDirty = false;
      updateStatus('Saved: ' + slug);
      if (isPhone()) renderCards();
      else loadPresetList();
      if (slug === activeSlug) activatePreset(slug);
    } catch (e) { alert('Save error: ' + e.message); }
  }

  async function loadPreset(slug) {
    try {
      var resp = await fetch('/api/presets/' + slug);
      if (!resp.ok) { alert('Not found'); return; }
      var doc = await resp.json();
      if (!isPhone()) { presetToGraph(doc); }
      updateStatus('Loaded: ' + slug);
    } catch (e) { alert('Load error: ' + e.message); }
  }

  function activatePreset(slug) { send({ type: 'music_preset', preset: slug }); updateStatus('Activated: ' + slug); }
  function deactivatePreset() { send({ type: 'music_preset', preset: '' }); updateStatus('Deactivated'); }

  async function loadPresetList() {
    try {
      var resp = await fetch('/api/presets');
      var list = await resp.json();
      var sel = $('#preset-select');
      sel.innerHTML = '<option value="">-- Load --</option>';
      list.forEach(function (p) {
        sel.innerHTML += '<option value="' + p.slug + '">' + escHtml(p.name) + (p.builtin ? ' (built-in)' : '') + '</option>';
      });
    } catch (e) { /* ok */ }
  }

  function newGraph() {
    graph.clear(); currentSlug = ''; graphDirty = false; updateStatus('New preset');
  }

  // ── Card browser (phone) ─────────────────────────────────────────────────
  async function renderCards() {
    try {
      var resp = await fetch('/api/presets');
      var list = await resp.json();
      var html = '';
      list.forEach(function (p) {
        var isActive = p.slug === activeSlug;
        html += '<div class="preset-card' + (isActive ? ' active' : '') + '">';
        html += '<div class="card-info">';
        html += '<div class="card-name">' + escHtml(p.name) + '</div>';
        if (p.description) html += '<div class="card-desc">' + escHtml(p.description) + '</div>';
        html += '<span class="card-badge">' + (p.builtin ? 'built-in' : 'user') + '</span>';
        if (isActive) html += ' <span class="card-live">● LIVE</span>';
        html += '</div>';
        html += '<button class="tweak-btn" data-tweak="' + p.slug + '" title="Tweak">&#x2699;</button>';
        html += '<button class="activate-btn" data-slug="' + p.slug + '">' + (isActive ? 'Stop' : 'Activate') + '</button>';
        html += '</div>';
      });
      $('#preset-cards').innerHTML = html || '<p style="color:var(--ink-dim)">No presets yet. Create one on desktop.</p>';

      // Bind activate buttons
      document.querySelectorAll('.activate-btn').forEach(function (btn) {
        btn.addEventListener('click', function () {
          var slug = btn.dataset.slug;
          if (slug === activeSlug) { deactivatePreset(); }
          else { activatePreset(slug); }
        });
      });
      // Bind tweak buttons
      document.querySelectorAll('.tweak-btn').forEach(function (btn) {
        btn.addEventListener('click', function () { openTweakPanel(btn.dataset.tweak); });
      });
    } catch (e) { $('#preset-cards').innerHTML = '<p style="color:var(--ink-dim)">Failed to load presets.</p>'; }
  }

  // ── Tweak panel ──────────────────────────────────────────────────────────
  var tweakSlug = '', tweakDoc = null;

  async function openTweakPanel(slug) {
    try {
      var resp = await fetch('/api/presets/' + slug);
      if (!resp.ok) return;
      tweakDoc = await resp.json();
      tweakSlug = slug;
      $('#tweak-title').textContent = tweakDoc.name || slug;

      // Collect tweakable params: number/bool types, skip light_ids
      var params = [];
      (tweakDoc.nodes || []).forEach(function (n) {
        var meta = catalog.find(function (m) { return m.type === n.type; });
        if (!meta || !meta.params) return;
        meta.params.forEach(function (p) {
          if (p.type !== 'number' && p.type !== 'bool') return;
          params.push({
            nodeId: n.id, nodeLabel: meta.label, name: p.name, label: p.label,
            type: p.type, value: n.params && n.params[p.name] !== undefined ? n.params[p.name] : p.default,
            min: p.min, max: p.max, step: p.step
          });
        });
      });
      // Sort by step (finest first), take top 5
      params.sort(function (a, b) { return (a.step || 0.01) - (b.step || 0.01); });
      params = params.slice(0, 5);

      var html = '';
      params.forEach(function (p) {
        html += '<label>' + escHtml(p.nodeLabel + ' · ' + p.label) + '</label>';
        if (p.type === 'bool') {
          html += '<input type="checkbox" data-tweak-node="' + p.nodeId + '" data-tweak-param="' + p.name + '"' + (p.value ? ' checked' : '') + '>';
        } else {
          var v = Number(p.value) || 0;
          html += '<input type="range" data-tweak-node="' + p.nodeId + '" data-tweak-param="' + p.name +
            '" min="' + (p.min !== undefined ? p.min : 0) + '" max="' + (p.max !== undefined ? p.max : 1) +
            '" step="' + (p.step || 0.01) + '" value="' + v + '">';
          html += ' <div class="tweak-readout" data-tweak-readout="' + p.nodeId + ':' + p.name + '">' + v.toFixed(2) + '</div>';
        }
      });
      if (!params.length) html = '<p style="color:var(--ink-dim)">No tweakable parameters in this preset.</p>';
      $('#tweak-controls').innerHTML = html;

      // Bind controls
      document.querySelectorAll('[data-tweak-param]').forEach(function (ctrl) {
        ctrl.addEventListener('input', function () {
          var nodeId = ctrl.dataset.tweakNode, name = ctrl.dataset.tweakParam;
          var node = (tweakDoc.nodes || []).find(function (n) { return n.id === nodeId; });
          if (!node) return;
          if (!node.params) node.params = {};
          if (ctrl.type === 'checkbox') node.params[name] = ctrl.checked;
          else node.params[name] = parseFloat(ctrl.value) || 0;
          var ro = document.querySelector('[data-tweak-readout="' + nodeId + ':' + name + '"]');
          if (ro) ro.textContent = Number(node.params[name]).toFixed(2);
          // Debounced save
          clearTimeout(tweakDoc._saveTimer);
          tweakDoc._saveTimer = setTimeout(function () { saveTweak(); }, 400);
        });
      });

      $('#tweak-panel').hidden = false;
      $('#tweak-overlay').classList.add('open');
      $('#tweak-panel').classList.add('open');
    } catch (e) { console.error('Tweak error:', e); }
  }

  async function saveTweak() {
    if (!tweakSlug || !tweakDoc) return;
    try {
      await fetch('/api/presets/' + tweakSlug, { method: 'PUT', body: JSON.stringify(tweakDoc) });
      if (tweakSlug === activeSlug) activatePreset(tweakSlug);
    } catch (e) { /* ok */ }
  }

  function closeTweak() {
    $('#tweak-overlay').classList.remove('open');
    $('#tweak-panel').classList.remove('open');
    setTimeout(function () { $('#tweak-panel').hidden = true; }, 250);
  }

  // ── Init ─────────────────────────────────────────────────────────────────
  function escHtml(s) {
    if (!s) return '';
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  async function init() {
    // Litegraph pointer events for touch
    if (typeof LiteGraph !== 'undefined') LiteGraph.pointerevents_method = 'pointer';

    graph = new LGraph();
    canvas = new LGraphCanvas('#graph-canvas', graph);
    canvas.render_canvas_border = false;
    setupPinchZoom();

    connectWS();
    await loadCatalog();
    await loadPresetList();

    // Toolbar
    $('#btn-new').addEventListener('click', newGraph);
    $('#btn-save').addEventListener('click', savePreset);
    $('#btn-activate').addEventListener('click', function () {
      if (!currentSlug) { savePreset().then(function () { if (currentSlug) activatePreset(currentSlug); }); return; }
      if (graphDirty) { alert('Save first before activating.'); return; }
      activatePreset(currentSlug);
    });
    $('#btn-deactivate').addEventListener('click', deactivatePreset);
    $('#preset-select').addEventListener('change', function () {
      if (this.value) loadPreset(this.value);
    });

    // Palette toggle buttons
    $('#btn-palette').addEventListener('click', function () { $('#palette').classList.toggle('open'); });
    $('#palette-bookmark').addEventListener('click', function () { $('#palette').classList.toggle('open'); });
    $('#btn-inspector').addEventListener('click', function () { $('#inspector').classList.toggle('open'); });

    // Close overlays on outside click
    $('#canvas-wrap').addEventListener('click', function () {
      if (isTablet()) { $('#palette').classList.remove('open'); $('#inspector').classList.remove('open'); }
    });

    // Card browser
    $('#btn-new-preset').addEventListener('click', function () {
      if (isPhone()) { alert('The node editor needs a larger screen. Open this page on a tablet or desktop to build presets.'); return; }
      newGraph();
    });

    // Tweak panel
    $('#tweak-overlay').addEventListener('click', closeTweak);
    $('#tweak-close').addEventListener('click', closeTweak);
    $('#tweak-handle').addEventListener('pointerdown', function (e) {
      var startY = e.clientY, panel = $('#tweak-panel'), orig = panel.style.transform;
      function onMove(ev) { var dy = ev.clientY - startY; if (dy > 0) panel.style.transform = 'translateY(' + dy + 'px)'; }
      function onUp(ev) { document.removeEventListener('pointermove', onMove); document.removeEventListener('pointerup', onUp);
        if (ev.clientY - startY > 60) closeTweak(); else panel.style.transform = ''; }
      document.addEventListener('pointermove', onMove); document.addEventListener('pointerup', onUp);
    });

    // Render card browser on phone
    if (isPhone()) renderCards();

    // Resize handler for phone ↔ tablet switching
    var wasPhone = isPhone();
    window.addEventListener('resize', function () {
      var now = isPhone();
      if (now !== wasPhone) {
        wasPhone = now;
        if (now) renderCards();
        else loadPresetList();
      }
    });

    window.addEventListener('beforeunload', function (e) {
      if (!isPhone() && graphDirty) { e.preventDefault(); e.returnValue = ''; }
    });

    updateStatus('Ready');
  }

  init().catch(function (e) { updateStatus('Init error: ' + e.message); console.error(e); });
})();
