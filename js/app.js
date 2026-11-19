/* ============================================================
   InfraGym / UI controller
   ------------------------------------------------------------
   只负责把引擎产出的「剧本」按时间轴播放出来，
   不包含任何故障判定逻辑。
   ============================================================ */
(function () {
  'use strict';

  var E = window.InfraGymEngine;
  var MONO = "'SFMono-Regular',Consolas,Menlo,monospace";
  var SPEED_BASE = 2.0; // 1x = 2 simulated seconds per real second

  var C = {
    cyan: '#39d6c0', amber: '#f2ad4f', red: '#ff5f6d',
    purple: '#a68cff', green: '#57d98b', dim: '#7d8fa3'
  };
  var STATUS = {
    healthy: { color: C.cyan, label: '健康' },
    degraded: { color: C.amber, label: '降级' },
    failed: { color: C.red, label: '故障' },
    recovering: { color: C.purple, label: '恢复中' }
  };

  // 每个场景注入的东西不一样，按钮文案要如实说明，不能都叫"注入故障"
  var SCENARIO_ACTION = {
    'node-loss': { inject: '注入节点失联', running: '正在断开节点', desc: '正在断开节点心跳' },
    'rolling-upgrade': { inject: '注入坏版本', running: '正在推送坏版本', desc: '正在推送 v2.4.1 坏版本' },
    'scheduler': { inject: '注入热点过载', running: '正在制造热点', desc: '正在制造热点节点' }
  };

  function $(id) { return document.getElementById(id); }
  function esc(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;')
      .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }
  function formatMsg(msg) {
    var parts = String(msg).split(/(<[^<>]+>)/g);
    return parts.map(function (p) {
      if (/^<[^<>]+>$/.test(p)) {
        return '<span class="hl">' + esc(p.slice(1, -1)) + '</span>';
      }
      return esc(p);
    }).join('');
  }
  function roundRect(c, x, y, w, h, r) {
    c.beginPath();
    c.moveTo(x + r, y);
    c.arcTo(x + w, y, x + w, y + h, r);
    c.arcTo(x + w, y + h, x, y + h, r);
    c.arcTo(x, y + h, x, y, r);
    c.arcTo(x, y, x + w, y, r);
    c.closePath();
  }

  var state = {
    scenario: 'node-loss',
    policy: { health: 3, concurrency: 2, retry: 2, nodes: 3 },
    seed: 42,
    locked: false,
    speed: 1,
    running: false,
    script: null,
    baseNodes: [],
    nodeMeta: [],
    nodeMap: {},
    curFrame: null,
    lastFrameIdx: -1,
    firedEvents: 0,
    particles: [],
    playT: 0,
    compare: [],
    hovered: null,
    mode: 'sim',          // sim | live
    liveUrl: 'http://127.0.0.1:8088',
    liveConnected: false,
    liveSource: '',       // 'real' | 'mock'
    liveT0: 0,
    liveResult: null
  };

  var canvas, g, cw = 0, ch = 0, dpr = 1, lastTs = 0;

  /* ---------------- layout ---------------- */
  function layoutCanvas() {
    if (!canvas) return;
    var rect = canvas.getBoundingClientRect();
    cw = Math.max(320, rect.width);
    ch = Math.max(260, rect.height);
    dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(cw * dpr);
    canvas.height = Math.round(ch * dpr);
    g.setTransform(dpr, 0, 0, dpr, 0, 0);

    if (!state.baseNodes.length) return;
    var cols = 1, rows = 1;
    state.baseNodes.forEach(function (n) {
      cols = Math.max(cols, n.col + 1);
      rows = Math.max(rows, n.row + 1);
    });
    var padX = 46, padTop = 66, padBottom = 26;
    var gapX = (cw - padX * 2) / cols;
    var gapY = (ch - padTop - padBottom) / rows;
    var bw = Math.max(40, Math.min(64, gapX * 0.62));
    var bh = Math.max(34, Math.min(48, gapY * 0.58));
    state.nodeMeta = state.baseNodes.map(function (n) {
      return {
        id: n.id, zone: n.zone,
        x: padX + n.col * gapX + gapX / 2,
        y: padTop + n.row * gapY + gapY / 2,
        w: bw, h: bh
      };
    });
    state.nodeMap = {};
    state.nodeMeta.forEach(function (m) { state.nodeMap[m.id] = m; });
  }

  /* ---------------- drawing ---------------- */
  function drawBackdrop(ts) {
    g.fillStyle = 'rgba(57,214,192,0.075)';
    for (var x = 14; x < cw; x += 24) {
      for (var y = 14; y < ch; y += 24) g.fillRect(x, y, 1, 1);
    }
    // links: control plane -> nodes
    g.strokeStyle = 'rgba(57,214,192,0.085)';
    g.lineWidth = 1;
    state.nodeMeta.forEach(function (m) {
      g.beginPath();
      g.moveTo(cw / 2, 40);
      g.lineTo(m.x, m.y);
      g.stroke();
    });
    // links: adjacent nodes (mesh)
    g.strokeStyle = 'rgba(36,50,68,0.55)';
    state.nodeMeta.forEach(function (m, i) {
      var n = state.nodeMeta[i + 1];
      if (n && n.row === m.row) {
        g.beginPath(); g.moveTo(m.x + m.w / 2, m.y); g.lineTo(n.x - n.w / 2, n.y); g.stroke();
      }
    });
    // control plane
    var pulse = 0.5 + 0.5 * Math.sin(ts / 600);
    g.strokeStyle = 'rgba(57,214,192,' + (0.4 + pulse * 0.35) + ')';
    g.lineWidth = 1;
    g.fillStyle = 'rgba(10,18,24,0.95)';
    roundRect(g, cw / 2 - 58, 16, 116, 24, 3);
    g.fill(); g.stroke();
    g.font = '9px ' + MONO;
    g.fillStyle = C.cyan;
    g.textAlign = 'center';
    g.fillText('CONTROL PLANE', cw / 2, 31);
  }

  function drawNode(m, status, workloads, ts) {
    var color = STATUS[status] ? STATUS[status].color : C.dim;
    var x = m.x - m.w / 2, y = m.y - m.h / 2;
    g.save();

    if (status === 'failed') {
      var pr = (ts / 900) % 1;
      g.strokeStyle = 'rgba(255,95,109,' + (0.55 * (1 - pr)) + ')';
      g.lineWidth = 2;
      g.beginPath();
      g.arc(m.x, m.y, m.w * 0.62 + pr * 16, 0, Math.PI * 2);
      g.stroke();
    }
    if (status === 'recovering') {
      g.strokeStyle = 'rgba(166,140,255,0.5)';
      g.lineWidth = 1;
      g.setLineDash([3, 3]);
      g.lineDashOffset = -(ts / 60) % 12;
      g.beginPath();
      g.arc(m.x, m.y, m.w * 0.7, 0, Math.PI * 2);
      g.stroke();
      g.setLineDash([]);
    }

    g.shadowColor = color;
    g.shadowBlur = status === 'healthy' ? 5 : 13;
    g.strokeStyle = color;
    g.lineWidth = status === 'failed' ? 2 : 1.2;
    g.fillStyle = 'rgba(9,14,20,0.94)';
    roundRect(g, x, y, m.w, m.h, 3);
    g.fill(); g.stroke();
    g.shadowBlur = 0;

    g.globalAlpha = 0.13;
    g.fillStyle = color;
    g.fillRect(x + 1, y + 1, m.w - 2, 12);
    g.globalAlpha = 1;

    g.font = '9px ' + MONO;
    g.fillStyle = color;
    g.textAlign = 'left';
    g.fillText(m.id, x + 5, y + 10);
    g.font = '8px ' + MONO;
    g.fillStyle = 'rgba(125,143,163,0.85)';
    g.textAlign = 'right';
    g.fillText(m.zone.replace('az-', ''), x + m.w - 5, y + 10);

    var n = workloads.length, perRow = 6, dx = 6, dy = 5;
    var startX = x + (m.w - Math.min(n, perRow) * dx) / 2;
    for (var i = 0; i < n; i++) {
      var px = startX + (i % perRow) * dx + 1.5;
      var py = y + 19 + Math.floor(i / perRow) * dy;
      var st = workloads[i].status;
      g.fillStyle = st === 'running' ? color
        : st === 'migrating' ? C.purple
          : st === 'stopped' ? C.red : C.amber;
      g.fillRect(px, py, 3, 3);
    }

    if (status === 'failed') {
      g.strokeStyle = C.red;
      g.lineWidth = 1.4;
      g.beginPath();
      g.moveTo(x + 6, y + 19); g.lineTo(x + m.w - 6, y + m.h - 5);
      g.moveTo(x + m.w - 6, y + 19); g.lineTo(x + 6, y + m.h - 5);
      g.stroke();
    }
    if (state.hovered === m.id) {
      g.strokeStyle = '#ffffff';
      g.globalAlpha = 0.5;
      g.lineWidth = 1;
      roundRect(g, x - 3, y - 3, m.w + 6, m.h + 6, 4);
      g.stroke();
      g.globalAlpha = 1;
    }
    g.restore();
  }

  function drawParticles(dt) {
    state.particles = state.particles.filter(function (p) {
      p.p += dt * 1.5;
      if (p.p >= 1) return false;
      var x = p.fx + (p.tx - p.fx) * p.p;
      var y = p.fy + (p.ty - p.fy) * p.p;
      var color = p.kind === 'fail' ? C.red : C.cyan;
      g.fillStyle = color;
      g.shadowColor = color;
      g.shadowBlur = 9;
      g.beginPath(); g.arc(x, y, 2.4, 0, Math.PI * 2); g.fill();
      g.shadowBlur = 0;
      return true;
    });
  }

  function drawFrame(dt, ts) {
    g.clearRect(0, 0, cw, ch);
    drawBackdrop(ts);

    var frame = state.curFrame;
    if (!frame) return;
    var nodeStatus = {};
    frame.nodes.forEach(function (n) { nodeStatus[n.id] = n.status; });
    var wlByNode = {};
    frame.workloads.forEach(function (w) {
      (wlByNode[w.nodeId] = wlByNode[w.nodeId] || []).push(w);
    });
    state.nodeMeta.forEach(function (m) {
      drawNode(m, nodeStatus[m.id] || 'healthy', wlByNode[m.id] || [], ts);
    });
    drawParticles(dt);
  }

  /* ---------------- playback ---------------- */
  function spawnFrameFlows(frame) {
    frame.flows.forEach(function (f) {
      var from = f.from ? state.nodeMap[f.from] : { x: cw / 2, y: 40 };
      var to = state.nodeMap[f.to];
      if (!to) return;
      state.particles.push({ fx: from.x, fy: from.y, tx: to.x, ty: to.y, p: 0, kind: f.kind });
    });
  }

  function updateStats(frame) {
    var counts = { healthy: 0, degraded: 0, failed: 0, recovering: 0 };
    frame.nodes.forEach(function (n) { counts[n.status] = (counts[n.status] || 0) + 1; });
    var running = 0, total = frame.workloads.length;
    frame.workloads.forEach(function (w) { if (w.status === 'running') running++; });
    $('statHealthy').textContent = counts.healthy;
    $('statDegraded').textContent = counts.degraded;
    $('statFailed').textContent = counts.failed;
    $('statWorkloads').textContent = running + '/' + total;
    $('statAvail').textContent = (total ? (running / total * 100).toFixed(1) : '100.0') + '%';
  }

  function pushTimeline(ev) {
    var box = $('timeline');
    if (box.querySelector('.tl-empty')) box.innerHTML = '';
    var el = document.createElement('div');
    el.className = 'tl-item ' + ev.type;
    el.innerHTML = '<span class="tl-time">' + ev.t.toFixed(1) + 's</span>' +
      '<span class="tl-mark"></span>' +
      '<span class="tl-msg">' + formatMsg(ev.msg) + '</span>';
    box.appendChild(el);
    box.scrollTop = box.scrollHeight;
  }

  function setEngine(kind) {
    var chip = $('engineDot').parentNode;
    chip.className = 'sys-chip' + (kind === 'running' ? ' running' : kind === 'done' ? ' done' : '');
    $('engineState').textContent = kind === 'running' ? 'ENGINE RUNNING'
      : kind === 'done' ? 'ENGINE COMPLETE' : 'ENGINE IDLE';
  }

  function advancePlay(dt) {
    var s = state.script;
    if (!s) return;
    state.playT += dt * SPEED_BASE * state.speed;

    // events up to current time
    while (state.firedEvents < s.events.length && s.events[state.firedEvents].t <= state.playT) {
      pushTimeline(s.events[state.firedEvents]);
      state.firedEvents++;
    }
    // latest frame
    var idx = state.lastFrameIdx;
    while (idx + 1 < s.frames.length && s.frames[idx + 1].t <= state.playT) {
      idx++;
    }
    if (idx !== state.lastFrameIdx) {
      state.lastFrameIdx = idx;
      state.curFrame = s.frames[idx];
      updateStats(state.curFrame);
      spawnFrameFlows(state.curFrame);
      if (state.curFrame.notice) {
        $('stageDesc').textContent = '18 节点 · 3 可用区 · ' + state.curFrame.notice;
      }
    }
    $('simClock').textContent = 'T+' + Math.min(state.playT, s.duration).toFixed(1).padStart(5, '0') + 's';

    if (state.playT >= s.duration) finishPlay();
  }

  function finishPlay() {
    state.running = false;
    state.playT = state.script.duration;
    state.firedEvents = state.script.events.length;
    state.curFrame = state.script.frames[state.script.frames.length - 1];
    updateStats(state.curFrame);
    setEngine('done');
    var r = state.script.result;
    $('rMttr').textContent = r.mttr + 's';
    $('rImpact').textContent = r.impact;
    $('rDetect').textContent = r.detect + 's';
    $('rRecovery').textContent = r.recovery + '%';
    $('recordBtn').disabled = false;
    $('injectBtn').innerHTML = '<span class="btn-glyph">⚡</span> 再次演练 <span class="btn-key">RUN</span>';
  }

  function frameLoop(ts) {
    var dt = lastTs ? Math.min((ts - lastTs) / 1000, 0.1) : 0;
    lastTs = ts;
    if (state.running) advancePlay(dt);
    drawFrame(dt, ts);
    requestAnimationFrame(frameLoop);
  }

  /* ---------------- live mode ---------------- */
  function apiBase() { return state.liveUrl.replace(/\/$/, ''); }

  function checkLiveConnection(silent) {
    var status = $('liveStatus');
    if (!silent) {
      status.className = 'live-status';
      status.textContent = '正在连接 ' + apiBase() + ' ...';
    }
    fetch(apiBase() + '/api/health', { method: 'GET' })
      .then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      })
      .then(function (j) {
        state.liveConnected = true;
        state.liveSource = j.mock ? 'mock' : 'real';
        status.className = 'live-status ok';
        var label = j.mock
          ? '已连接 · MOCK 数据源（control plane 自测模式，非真实集群）'
          : '已连接 · 真实集群 ' + (j.clusterVersion || '') + ' · ns=' + (j.namespace || '');
        status.textContent = label;
        setEngine('idle');
      })
      .catch(function (e) {
        state.liveConnected = false;
        status.className = 'live-status err';
        status.textContent = '连接失败：' + e.message + '。请先运行 deploy/bootstrap.sh 启动 control plane。';
      });
  }

  // 把 control plane 的聚合指标映射成 Canvas 可渲染的 frame。
  // 每个节点代表一个真实 pod：前 ready 个健康，其余故障。
  function buildLiveFrame(ready, total, phase) {
    total = Math.max(1, total || 6);
    ready = Math.max(0, Math.min(ready, total));
    var nodes = [], workloads = [];
    var cols = Math.ceil(Math.sqrt(total));
    for (var i = 0; i < total; i++) {
      var id = 'pod-' + String(i).padStart(2, '0');
      var st = i < ready ? 'healthy' : (phase === 'recover' ? 'recovering' : 'failed');
      nodes.push({ id: id, status: st, zone: 'az-' + String.fromCharCode(97 + (i % 3)), col: i % cols, row: Math.floor(i / cols) });
      workloads.push({ id: 'w' + i, nodeId: id, status: i < ready ? 'running' : 'stopped' });
    }
    return { nodes: nodes, workloads: workloads, flows: [], notice: '' };
  }

  function wireLiveEvent(ev) {
    var type = ev.type || 'info';
    var msg = ev.message || '';
    // Go 的 phase：inject / detect / mitigate / recover
    pushTimeline({ t: 0, type: type, msg: msg });
    var frame = buildLiveFrame(ev.ready, ev.total, ev.phase);
    state.curFrame = frame;
    state.baseNodes = frame.nodes;
    layoutCanvas();
    updateStats(frame);
    var avail = frame.workloads.length
      ? (frame.workloads.filter(function (w) { return w.status === 'running'; }).length / frame.workloads.length * 100).toFixed(1)
      : '100.0';
    $('stageDesc').textContent = '真实 pod ' + (ev.total || '?') + ' 个 · 就绪 ' + (ev.ready || 0) + ' · 可用性 ' + avail + '%';
    // 相对时间用事件时间差
    if (!state.liveT0) state.liveT0 = new Date(ev.at).getTime();
    var dt = (new Date(ev.at).getTime() - state.liveT0) / 1000;
    $('simClock').textContent = 'T+' + dt.toFixed(1).padStart(5, '0') + 's';
  }

  function finishLive(result) {
    state.running = false;
    state.liveResult = result || null;
    setEngine('done');
    $('injectBtn').innerHTML = '<span class="btn-glyph">⚡</span> 再次演练 <span class="btn-key">RUN</span>';
    if (result) {
      var mttrS = result.mttr ? result.mttr / 1e9 : 0;
      var detectS = result.detectLatency ? result.detectLatency / 1e9 : 0;
      $('rMttr').textContent = mttrS ? mttrS.toFixed(2) + 's' : '—';
      $('rImpact').textContent = result.impactPods != null ? result.impactPods : '—';
      $('rDetect').textContent = detectS ? detectS.toFixed(2) + 's' : '—';
      $('rRecovery').textContent = result.recovered ? '100%' : '0%';
      state.liveResult.normMttr = mttrS;
      state.liveResult.normDetect = detectS;
    }
    $('recordBtn').disabled = false;
  }

  function injectLive() {
    if (!state.liveConnected) {
      $('liveStatus').className = 'live-status err';
      $('liveStatus').textContent = '请先测试连接成功，再注入故障。';
      return;
    }
    // 真实模式只支持 S1
    if (state.scenario !== 'node-loss') {
      $('liveStatus').className = 'live-status err';
      $('liveStatus').textContent = '真实模式目前只支持 S1 节点故障自愈，S2/S3 请在 SIM 模式运行。';
      return;
    }
    state.running = true;
    state.liveT0 = 0;
    $('timeline').innerHTML = '';
    $('rMttr').textContent = '—';
    $('rImpact').textContent = '—';
    $('rDetect').textContent = '—';
    $('rRecovery').textContent = '—';
    $('recordBtn').disabled = true;
    $('injectBtn').innerHTML = '<span class="btn-glyph">⚡</span> 正在注入真实故障 <span class="btn-key">···</span>';
    setEngine('running');

    fetch(apiBase() + '/api/experiments', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ scenario: 'node-loss', duration: 20 })
    })
      .then(function (r) {
        if (!r.ok) throw new Error('创建实验失败 HTTP ' + r.status);
        return r.json();
      })
      .then(function (j) {
        var es = new EventSource(apiBase() + j.stream);
        es.addEventListener('tick', function (e) {
          var ev = JSON.parse(e.data);
          wireLiveEvent(ev);
        });
        es.onerror = function () {
          es.close();
          fetch(apiBase() + '/api/experiments/' + j.id).then(function (r) { return r.json(); })
            .then(function (full) {
              finishLive(full.result);
            })
            .catch(function () { finishLive(null); });
        };
      })
      .catch(function (e) {
        state.running = false;
        setEngine('idle');
        $('liveStatus').className = 'live-status err';
        $('liveStatus').textContent = '注入失败：' + e.message;
        $('injectBtn').innerHTML = '<span class="btn-glyph">⚡</span> 注入节点失联 <span class="btn-key">RUN</span>';
      });
  }

  function inject() {
    if (state.mode === 'live') { injectLive(); return; }
    injectSim();
  }

  function injectSim() {
    var seed = parseInt($('seedInput').value, 10);
    if (isNaN(seed)) seed = 0;
    state.seed = seed >>> 0;

    state.script = E.simulate(state.scenario, state.policy, state.seed);
    state.baseNodes = state.script.nodes;
    state.curFrame = state.script.frames[0];
    state.playT = 0;
    state.lastFrameIdx = -1;
    state.firedEvents = 0;
    state.particles = [];
    state.running = true;

    $('timeline').innerHTML = '';
    $('rMttr').textContent = '—';
    $('rImpact').textContent = '—';
    $('rDetect').textContent = '—';
    $('rRecovery').textContent = '—';
    $('recordBtn').disabled = true;
    $('injectBtn').innerHTML = '<span class="btn-glyph">⚡</span> ' + SCENARIO_ACTION[state.scenario].running + ' <span class="btn-key">···</span>';
    $('stageDesc').textContent = '18 节点 · 3 可用区 · ' + SCENARIO_ACTION[state.scenario].desc;
    setEngine('running');
    layoutCanvas();
    updateStats(state.curFrame);
  }

  function reset() {
    state.running = false;
    state.playT = 0;
    state.liveT0 = 0;
    if (state.mode === 'live') { resetLive(); return; }
    var s = E.simulate(state.scenario, state.policy, state.seed);
    state.script = s;
    state.baseNodes = s.nodes;
    state.curFrame = s.frames[0];
    state.lastFrameIdx = 0;
    state.firedEvents = 0;
    state.particles = [];
    $('timeline').innerHTML = '<div class="tl-empty">// 等待注入事件<br><span>系统就绪，事件将在此实时流式输出</span></div>';
    $('rMttr').textContent = '—';
    $('rImpact').textContent = '—';
    $('rDetect').textContent = '—';
    $('rRecovery').textContent = '—';
    $('recordBtn').disabled = true;
    $('simClock').textContent = 'T+000.0s';
    $('stageDesc').textContent = '18 节点 · 3 可用区 · 控制面健康';
    $('injectBtn').innerHTML = '<span class="btn-glyph">⚡</span> ' + SCENARIO_ACTION[state.scenario].inject + ' <span class="btn-key">RUN</span>';
    setEngine('idle');
    layoutCanvas();
    updateStats(state.curFrame);
  }

  function resetLive() {
    var frame = buildLiveFrame(6, 6, 'healthy');
    state.script = null;
    state.baseNodes = frame.nodes;
    state.curFrame = frame;
    $('timeline').innerHTML = '<div class="tl-empty">// 等待真实故障注入<br><span>事件将由 control plane 通过 SSE 实时推送</span></div>';
    $('rMttr').textContent = '—';
    $('rImpact').textContent = '—';
    $('rDetect').textContent = '—';
    $('rRecovery').textContent = '—';
    $('recordBtn').disabled = true;
    $('simClock').textContent = 'T+000.0s';
    $('stageDesc').textContent = '真实 pod 6 个 · 就绪 6 · 等待注入';
    $('injectBtn').innerHTML = '<span class="btn-glyph">⚡</span> 注入节点失联 <span class="btn-key">RUN</span>';
    setEngine(state.liveConnected ? 'idle' : 'idle');
    layoutCanvas();
    updateStats(frame);
  }

  function switchMode(mode) {
    if (mode === state.mode) return;
    state.mode = mode;
    document.body.classList.toggle('live-mode', mode === 'live');
    document.querySelectorAll('.mode-btn').forEach(function (b) {
      b.classList.toggle('active', b.dataset.mode === mode);
    });
    $('sbMode').textContent = mode === 'live' ? 'LIVE CLUSTER' : 'SIMULATED';
    $('sbEngine').textContent = mode === 'live' ? 'chaos-mesh-v1' : 'deterministic-v1';
    $('sbNote').textContent = mode === 'live'
      ? 'REAL CHAOS INJECTION · PROMETHEUS METRICS'
      : 'NO REAL CLOUD CREDENTIALS REQUIRED';
    if (mode === 'live') {
      state.liveUrl = $('liveUrl').value.trim() || state.liveUrl;
      checkLiveConnection(false);
      resetLive();
    } else {
      reset();
    }
  }

  function policySummary(p) {
    return 'H' + p.health + ' · C' + p.concurrency + ' · R' + p.retry + ' · N' + p.nodes;
  }

  function recordResult() {
    if (state.running) return;
    if (state.mode === 'live') {
      if (!state.liveResult) return;
      var r = state.liveResult;
      state.compare.push({
        scenario: 'LIVE:节点自愈', seed: null,
        policy: state.liveSource === 'mock' ? 'MOCK 数据源' : '真实集群',
        mttr: Math.round(r.normMttr * 100) / 100, impact: r.impactPods,
        recovery: r.recovered ? 100 : 0, detect: Math.round(r.normDetect * 100) / 100
      });
      renderCompare();
      return;
    }
    if (!state.script) return;
    var s = state.script;
    state.compare.push({
      scenario: s.scenario, seed: s.seed, policy: policySummary(s.policy),
      mttr: s.result.mttr, impact: s.result.impact,
      recovery: s.result.recovery, detect: s.result.detect
    });
    renderCompare();
  }

  function renderCompare() {
    var box = $('compareList');
    if (!state.compare.length) {
      box.innerHTML = '<div class="cmp-empty">尚无记录 · 跑完一次演练后点击「记录本轮」</div>';
      return;
    }
    var seed0 = state.compare[0].seed;
    var best = Math.min.apply(null, state.compare.map(function (r) { return r.mttr; }));
    box.innerHTML = state.compare.map(function (r, i) {
      var isLive = r.seed === null;
      var mismatch = !isLive && r.seed !== seed0;
      var seedLabel = isLive ? 'LIVE' : ('seed ' + r.seed);
      return '<div class="cmp-row' + (r.mttr === best && !mismatch ? ' best' : '') + '">' +
        '<div class="cmp-top"><b>#' + (i + 1) + ' ' + esc(r.scenario) + '</b>' +
        '<span' + (mismatch ? ' style="color:#ff5f6d"' : (isLive ? ' style="color:#f2ad4f"' : '')) + '>' + seedLabel + (mismatch ? ' ⚠' : '') + '</span></div>' +
        '<div class="cmp-metrics"><span>' + esc(r.policy) + '</span></div>' +
        '<div class="cmp-metrics" style="margin-top:4px">' +
        '<span>MTTR <i>' + r.mttr + 's</i></span>' +
        '<span>IMPACT <i>' + r.impact + '</i></span>' +
        '<span>REC <i>' + r.recovery + '%</i></span></div></div>';
    }).join('');
  }

  /* ---------------- binding ---------------- */
  var PARAM_DEFS = [
    ['pHealth', 'vHealth', 'health', function (v) { return v.toFixed(1) + 's'; }],
    ['pConcurrency', 'vConcurrency', 'concurrency', function (v) { return String(v); }],
    ['pRetry', 'vRetry', 'retry', function (v) { return String(v); }],
    ['pNodes', 'vNodes', 'nodes', function (v) { return String(v); }]
  ];

  function setParam(key, v) {
    state.policy[key] = v;
    PARAM_DEFS.forEach(function (d) {
      if (d[2] === key) {
        $(d[0]).value = v;
        $(d[1]).textContent = d[3](v);
      }
    });
  }

  function bindParams() {
    PARAM_DEFS.forEach(function (d) {
      var el = $(d[0]);
      el.addEventListener('input', function () {
        setParam(d[2], parseFloat(el.value));
      });
    });
  }

  /* shareable experiment links:
     ?scenario=node-loss&seed=42&health=1&concurrency=6&retry=5&nodes=3&auto=1 */
  function applyUrlParams() {
    var q = new URLSearchParams(location.search);
    var sc = q.get('scenario');
    if (sc && ['node-loss', 'rolling-upgrade', 'scheduler'].indexOf(sc) !== -1) {
      state.scenario = sc;
      document.querySelectorAll('.scenario').forEach(function (b) {
        b.classList.toggle('active', b.dataset.scenario === sc);
      });
    }
    PARAM_DEFS.forEach(function (d) {
      var v = parseFloat(q.get(d[2]));
      if (!isNaN(v)) setParam(d[2], v);
    });
    var sd = parseInt(q.get('seed'), 10);
    if (!isNaN(sd)) $('seedInput').value = sd >>> 0;
    var sp = parseFloat(q.get('speed'));
    if (!isNaN(sp) && [0.5, 1, 2, 4].indexOf(sp) !== -1) {
      state.speed = sp;
      document.querySelectorAll('#speedGroup button').forEach(function (b) {
        b.classList.toggle('active', parseFloat(b.dataset.speed) === sp);
      });
    }
    var lu = q.get('liveUrl');
    if (lu) {
      state.liveUrl = lu;
      $('liveUrl').value = lu;
    }
    var wantLive = q.get('mode') === 'live';
    return { auto: q.get('auto') === '1', live: wantLive };
  }

  function bind() {
    canvas = $('topoCanvas');
    g = canvas.getContext('2d');

    document.querySelectorAll('.scenario').forEach(function (btn) {
      btn.addEventListener('click', function () {
        document.querySelectorAll('.scenario').forEach(function (b) { b.classList.remove('active'); });
        btn.classList.add('active');
        state.scenario = btn.dataset.scenario;
        reset();
      });
    });

    $('injectBtn').addEventListener('click', inject);
    $('resetBtn').addEventListener('click', reset);
    $('recordBtn').addEventListener('click', recordResult);
    $('clearCompare').addEventListener('click', function () {
      state.compare = [];
      renderCompare();
    });

    document.querySelectorAll('.mode-btn').forEach(function (b) {
      b.addEventListener('click', function () { switchMode(b.dataset.mode); });
    });
    $('liveConnectBtn').addEventListener('click', function () {
      state.liveUrl = $('liveUrl').value.trim() || state.liveUrl;
      checkLiveConnection(false);
    });

    $('seedLockBtn').addEventListener('click', function () {
      state.locked = !state.locked;
      this.textContent = state.locked ? 'LOCKED' : 'UNLOCKED';
      this.classList.toggle('locked', state.locked);
      $('seedInput').style.color = state.locked ? C.green : '';
    });

    document.querySelectorAll('#speedGroup button').forEach(function (b) {
      b.addEventListener('click', function () {
        document.querySelectorAll('#speedGroup button').forEach(function (x) { x.classList.remove('active'); });
        b.classList.add('active');
        state.speed = parseFloat(b.dataset.speed);
      });
    });

    canvas.addEventListener('mousemove', function (e) {
      var rect = canvas.getBoundingClientRect();
      var mx = e.clientX - rect.left, my = e.clientY - rect.top;
      var hit = null;
      state.nodeMeta.forEach(function (m) {
        if (Math.abs(mx - m.x) < m.w / 2 + 4 && Math.abs(my - m.y) < m.h / 2 + 4) hit = m;
      });
      state.hovered = hit ? hit.id : null;
      var card = $('hoverCard');
      if (!hit || !state.curFrame) { card.style.opacity = 0; return; }
      var st = state.curFrame.nodes.filter(function (n) { return n.id === hit.id; })[0];
      var wls = state.curFrame.workloads.filter(function (w) { return w.nodeId === hit.id; });
      var running = wls.filter(function (w) { return w.status === 'running'; }).length;
      card.innerHTML = '<div class="hc-t">' + esc(hit.id) + ' <span style="color:' + C.dim + '">/ ' + esc(hit.zone) + '</span></div>' +
        '<div class="hc-r"><span>状态</span><b style="color:' + (STATUS[st.status] || STATUS.healthy).color + '">' +
        (STATUS[st.status] || STATUS.healthy).label + '</b></div>' +
        '<div class="hc-r"><span>负载</span><b>' + running + '/' + wls.length + ' running</b></div>' +
        '<div class="hc-r"><span>容量</span><b>8 slots</b></div>';
      card.style.opacity = 1;
      card.style.left = Math.min(mx + 14, cw - 150) + 'px';
      card.style.top = Math.max(4, my - 10) + 'px';
    });
    canvas.addEventListener('mouseleave', function () {
      state.hovered = null;
      $('hoverCard').style.opacity = 0;
    });

    window.addEventListener('resize', layoutCanvas);
    bindParams();
  }

  /* ---------------- boot ---------------- */
  function init() {
    bind();
    var opts = applyUrlParams();
    reset();
    if (opts.live) switchMode('live');
    if (opts.auto && !opts.live) setTimeout(inject, 120);
    requestAnimationFrame(frameLoop);
  }
  init();
})();
