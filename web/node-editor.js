// HueMux Node Editor — Litegraph.js integration for Phase 3 preset builder.
// All Litegraph calls live here; the DAG adapter is framework-agnostic.
(function () {
  'use strict';

  const $ = (s) => document.querySelector(s);
  const WS_URL = authWSURL('/ws');

  // ── State ────────────────────────────────────────────────────────────────
  let ws, catalog = [], currentSlug = '', activeSlug = '', graphDirty = false;
  let graph, canvas;
  const catColor = {
    source: '#4488cc', analysis: '#44aa66', routing: '#ccaa44',
    effect: '#cc4444', modulation: '#8844cc', output_effect: '#cc4488'
  };

  // ── WS connection ────────────────────────────────────────────────────────
  function connectWS() {
    if (ws) { try { ws.close(); } catch (e) { /* ok */ } }
    ws = new WebSocket(WS_URL);
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => { $('#conn-dot').classList.add('connected'); updateStatus('Connected'); };
    ws.onclose = () => { $('#conn-dot').classList.remove('connected'); updateStatus('Disconnected — reload page'); };
    ws.onmessage = (e) => {
      if (typeof e.data === 'string') {
        try { handleMsg(JSON.parse(e.data)); } catch (_) {}
      }
    };
  }
  function send(msg) { if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg)); }
  function updateStatus(s) { $('#status-bar').textContent = s; }

  function handleMsg(msg) {
    if (msg.type === 'status' && msg.snapshot) {
      var slug = (msg.snapshot.MusicPreset || '');
      if (slug !== activeSlug) {
        activeSlug = slug;
        updateActivateButton();
      }
    }
    if (msg.type === 'debug' && msg.nodes) {
      updateNodeBadges(msg.nodes);
    }
  }

  function updateActivateButton() {
    var btn = $('#btn-activate'), deact = $('#btn-deactivate'), badge = $('#live-badge');
    if (activeSlug) {
      btn.disabled = true; deact.disabled = false;
      badge.hidden = false;
    } else {
      btn.disabled = false; deact.disabled = true;
      badge.hidden = true;
    }
  }

  // ── Catalog → palette + node types ───────────────────────────────────────
  async function loadCatalog() {
    try {
      var resp = await fetch('/api/presets/catalog');
      catalog = await resp.json();
      buildPalette();
      registerNodeTypes();
    } catch (e) {
      $('#palette').innerHTML = '<p style="color:var(--ink-dim);padding:8px">Catalog unavailable — is the server running?</p>';
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
        el.draggable = true;
        el.addEventListener('dragstart', function (e) {
          e.dataTransfer.setData('text/plain', m.type);
          e.dataTransfer.effectAllowed = 'copy';
        });
        palette.appendChild(el);
      });
    });
    // Drop target: canvas accepts dragged nodes
    var cw = $('#canvas-wrap');
    cw.addEventListener('dragover', function (e) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; });
    cw.addEventListener('drop', function (e) {
      e.preventDefault();
      var type = e.dataTransfer.getData('text/plain');
      if (!type) return;
      var meta = catalog.find(function (m) { return m.type === type; });
      if (!meta) return;
      // Convert drop position to canvas coordinates
      var rect = $('#graph-canvas').getBoundingClientRect();
      var x = e.clientX - rect.left, y = e.clientY - rect.top;
      var node = LiteGraph.createNode('preset/' + type);
      if (meta.params) {
        meta.params.forEach(function (p) {
          if (p.default !== undefined) node.properties[p.name] = p.default;
        });
      }
      node.pos = [x, y];
      graph.add(node);
      graphDirty = true;
    });
  }

  function registerNodeTypes() {
    catalog.forEach(function (meta) {
      var ctor = function () {
        // Seed properties from param defaults
        if (meta.params) {
          meta.params.forEach(function (p) {
            if (p.default !== undefined) this.properties[p.name] = p.default;
          }, this);
        }
        // Add input/output slots
        (meta.inputs || []).forEach(function (p) {
          this.addInput(p.name, p.kind === 'trigger' ? 'number' : 'number');
        }, this);
        (meta.outputs || []).forEach(function (p) {
          this.addOutput(p.name, p.kind === 'trigger' ? 'number' : 'number');
        }, this);
      };
      ctor.title = meta.label;
      ctor.desc = meta.description;
      ctor.prototype.onDrawBackground = function (ctx) {
        if (this.flags.collapsed) return;
        // Color stripe by category
        ctx.fillStyle = catColor[meta.category] || '#666';
        ctx.fillRect(0, 0, 4, this.size[1]);
        // Live badge
        if (this._badge) {
          ctx.font = '9px Fira Code, monospace';
          ctx.fillStyle = '#aaa';
          ctx.fillText(this._badge, 8, this.size[1] - 4);
        }
      };
      // Click → inspector
      ctor.prototype.onSelected = function () { showInspector(this, meta); };
      ctor.prototype.onDeselected = function () { clearInspector(); };
      // Property changes mark dirty
      ctor.prototype.onPropertyChanged = function () { graphDirty = true; };
      LiteGraph.registerNodeType('preset/' + meta.type, ctor);
    });
  }

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
          html += '<input type="text" data-param="' + p.name + '" value="' + escHtml(JSON.stringify(val || [])) + '" placeholder=\'[]\'>';
        } else {
          // number slider
          var v = Number(val) || 0;
          html += '<input type="range" data-param="' + p.name + '" min="' + (p.min !== undefined ? p.min : 0) +
            '" max="' + (p.max !== undefined ? p.max : 1) + '" step="' + (p.step || 0.01) + '" value="' + v + '">';
          html += ' <span class="param-readout" data-readout="' + p.name + '">' + v.toFixed(2) + '</span>';
        }
        if (p.description) html += '<p style="font-size:10px;color:var(--ink-dim);margin:0">' + escHtml(p.description) + '</p>';
      });
    }
    // Live data strip
    if (node._badge) {
      html += '<div style="margin-top:8px;font-size:10px;color:var(--accent)">Live: ' + escHtml(node._badge) + '</div>';
    }
    el.innerHTML = html;

    // Bind controls
    el.querySelectorAll('[data-param]').forEach(function (ctrl) {
      ctrl.addEventListener('input', function () {
        var name = ctrl.dataset.param;
        var p = meta.params.find(function (pp) { return pp.name === name; });
        if (!p) return;
        var val;
        if (ctrl.type === 'checkbox') val = ctrl.checked;
        else if (ctrl.type === 'color') val = ctrl.value;
        else if (p.type === 'light_ids') {
          try { val = JSON.parse(ctrl.value); } catch (_) { val = []; }
        } else if (p.choices) val = ctrl.value;
        else val = parseFloat(ctrl.value) || 0;
        node.properties[name] = val;
        graphDirty = true;
        // Update readout
        var ro = el.querySelector('[data-readout="' + name + '"]');
        if (ro) ro.textContent = typeof val === 'number' ? val.toFixed(2) : val;
      });
    });
  }

  function clearInspector() { inspectedNode = null; inspectedMeta = null; $('#inspector').innerHTML = '<p style="color:var(--ink-dim);font-size:12px">Select a node to inspect its parameters.</p>'; }

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
        // Update inspector live data
        var el = $('#inspector');
        var badgeEl = el.querySelector('div:last-child');
        if (badgeEl && badgeEl.textContent.startsWith('Live:')) {
          badgeEl.textContent = 'Live: ' + node._badge;
        }
      }
    });
    if (canvas) canvas.setDirty(true, true);
  }

  // ── DAG adapter ──────────────────────────────────────────────────────────
  function graphToPreset(name, desc) {
    var nodes = [];
    var edges = [];
    graph._nodes.forEach(function (n) {
      nodes.push({ id: String(n.id), type: n.type.replace('preset/', ''), params: stripParams(n.properties, n.type) });
      // Outgoing links
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

  function stripParams(props, type) {
    // Only include params that differ from defaults, for cleaner JSON.
    // For MVP, include all non-undefined properties.
    var out = {};
    Object.keys(props || {}).forEach(function (k) {
      if (props[k] !== undefined) out[k] = props[k];
    });
    return out;
  }

  function presetToGraph(doc) {
    graph.clear();
    // Build nodes
    var nodeMap = {};
    doc.nodes.forEach(function (rn) {
      var meta = catalog.find(function (m) { return m.type === rn.type; });
      var node = LiteGraph.createNode('preset/' + rn.type);
      node.id = rn.id;
      if (rn.params) Object.assign(node.properties, rn.params);
      graph.add(node);
      nodeMap[rn.id] = node;
    });
    // Build edges
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

  function findOutputSlot(node, portName) {
    for (var i = 0; i < (node.outputs || []).length; i++) {
      if (node.outputs[i] && node.outputs[i].name === portName) return i;
    }
    return 0;
  }
  function findInputSlot(node, portName) {
    for (var i = 0; i < (node.inputs || []).length; i++) {
      if (node.inputs[i] && node.inputs[i].name === portName) return i;
    }
    return 0;
  }

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
      loadPresetList();
      // If this preset is currently active, re-activate to pick up changes
      if (slug === activeSlug) activatePreset(slug);
    } catch (e) { alert('Save error: ' + e.message); }
  }

  async function loadPreset(slug) {
    try {
      var resp = await fetch('/api/presets/' + slug);
      if (!resp.ok) { alert('Not found'); return; }
      var doc = await resp.json();
      presetToGraph(doc);
      updateStatus('Loaded: ' + slug);
    } catch (e) { alert('Load error: ' + e.message); }
  }

  async function activatePreset(slug) {
    send({ type: 'music_preset', preset: slug });
    updateStatus('Activated: ' + slug);
  }

  function deactivatePreset() {
    send({ type: 'music_preset', preset: '' });
    updateStatus('Deactivated');
  }

  async function loadPresetList() {
    try {
      var resp = await fetch('/api/presets');
      var list = await resp.json();
      var sel = $('#preset-select');
      sel.innerHTML = '<option value="">-- Load preset --</option>';
      list.forEach(function (p) {
        sel.innerHTML += '<option value="' + p.slug + '">' + escHtml(p.name) + (p.builtin ? ' (built-in)' : '') + '</option>';
      });
    } catch (e) { /* ok */ }
  }

  // ── Init ─────────────────────────────────────────────────────────────────
  function newGraph() {
    graph.clear();
    currentSlug = ''; graphDirty = false;
    updateStatus('New preset');
  }

  function escHtml(s) {
    if (!s) return '';
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  async function init() {
    graph = new LGraph();
    canvas = new LGraphCanvas('#graph-canvas', graph);
    canvas.render_canvas_border = false;

    connectWS();
    await loadCatalog();
    await loadPresetList();

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

    // Warn on close with unsaved changes
    window.addEventListener('beforeunload', function (e) {
      if (graphDirty) { e.preventDefault(); e.returnValue = ''; }
    });

    updateStatus('Ready');
  }

  init().catch(function (e) { updateStatus('Init error: ' + e.message); console.error(e); });
})();
