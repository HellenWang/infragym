/* ============================================================
   InfraGym / simulation engine
   ------------------------------------------------------------
   纯逻辑，无 DOM 依赖。相同的 (scenario, policy, seed) 必然
   产出完全相同的事件流、帧序列和结果指标。

   simulate() 一次性同步计算整场演练，返回一个可回放的「剧本」；
   UI 层只负责按时间轴播放，因此策略对比无需等待动画即可取结果。
   ============================================================ */
(function (global) {
  'use strict';

  /* ---------- deterministic PRNG (mulberry32) ---------- */
  function mulberry32(a) {
    return function () {
      a |= 0;
      a = (a + 0x6D2B79F5) | 0;
      var t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  function r1(v) { return Math.round(v * 10) / 10; }
  function fmt(n) { return String(n).padStart(2, '0'); }
  function fmt3(n) { return String(n).padStart(3, '0'); }

  /* ---------- cluster model ---------- */
  function buildCluster(rng, nodeCount) {
    var nodes = [], workloads = [];
    var cols = Math.ceil(Math.sqrt(nodeCount));
    var zones = ['az-a', 'az-b', 'az-c'];
    for (var i = 0; i < nodeCount; i++) {
      nodes.push({
        id: 'n-' + fmt(i),
        zone: zones[i % 3],
        col: i % cols,
        row: Math.floor(i / cols),
        capacity: 8
      });
    }
    var wid = 0;
    nodes.forEach(function (n) {
      var count = 3 + Math.floor(rng() * 4); // 3..6 workloads per node
      for (var j = 0; j < count; j++) {
        workloads.push({ id: 'w-' + fmt3(wid++), nodeId: n.id, status: 'running' });
      }
    });
    return { nodes: nodes, workloads: workloads };
  }

  /* ---------- simulation context ---------- */
  function SimContext(seed, cluster) {
    this.rng = mulberry32(seed);
    this.nodes = cluster.nodes.map(function (n) { return Object.assign({}, n, { status: 'healthy' }); });
    this.workloads = cluster.workloads.map(function (w) { return Object.assign({}, w); });
    this.events = [];
    this.frames = [];
    this.t = 0;
  }

  SimContext.prototype.emit = function (type, msg) {
    this.events.push({ t: r1(this.t), type: type, msg: msg });
  };

  SimContext.prototype.snapshot = function (flows, notice) {
    this.frames.push({
      t: r1(this.t),
      nodes: this.nodes.map(function (n) { return { id: n.id, status: n.status }; }),
      workloads: this.workloads.map(function (w) {
        return { id: w.id, nodeId: w.nodeId, status: w.status };
      }),
      flows: (flows || []).map(function (f) { return Object.assign({}, f); }),
      notice: notice || ''
    });
  };

  SimContext.prototype.advance = function (dt) {
    this.t = r1(this.t + dt);
  };

  function pickDistinct(rng, arr, count) {
    var pool = arr.slice(), out = [];
    for (var i = pool.length - 1; i > 0; i--) {
      var j = Math.floor(rng() * (i + 1));
      var tmp = pool[i]; pool[i] = pool[j]; pool[j] = tmp;
    }
    for (var k = 0; k < Math.min(count, pool.length); k++) out.push(pool[k]);
    return out;
  }

  function workOnNodes(workloads, nodes) {
    var ids = nodes.map(function (n) { return n.id; });
    return workloads.filter(function (w) { return ids.indexOf(w.nodeId) !== -1; });
  }

  /* ---------- scenario S1: node loss + self-healing ---------- */
  function scenarioNodeLoss(ctx, policy) {
    var rng = ctx.rng, nodes = ctx.nodes, workloads = ctx.workloads;

    ctx.emit('info', '演练开始 · 场景 <S1 节点故障自愈>');
    ctx.emit('info', '集群规模 ' + nodes.length + ' 节点 / ' + workloads.length + ' 负载 · 策略已锁定');
    ctx.snapshot([], '集群初始化完成');

    // 1) inject failures
    var victims = pickDistinct(rng, nodes, policy.nodes);
    var failT = ctx.t;
    victims.forEach(function (n) {
      n.status = 'failed';
      ctx.emit('error', '节点 <' + n.id + '> (' + n.zone + ') 心跳丢失，控制面标记 NotReady');
    });
    var affected = workOnNodes(workloads, victims);
    affected.forEach(function (w) { w.status = 'stopped'; });
    ctx.emit('warn', '影响评估：' + affected.length + ' 个负载失去承载节点');
    ctx.snapshot(victims.map(function (n) { return { to: n.id, kind: 'fail' }; }),
      '检测到 ' + victims.length + ' 个节点故障');

    // 2) detection after health-check interval (+ jitter)
    var detectLag = r1(policy.health + rng() * 1.2);
    ctx.advance(detectLag);
    ctx.emit('warn', '健康检查确认 ' + victims.length + ' 节点不可达（探测间隔 ' + policy.health + 's，延迟 ' + detectLag + 's）');
    ctx.snapshot([], '故障确认');

    // 3) migrate affected workloads in batches
    var targets = nodes.filter(function (n) { return n.status === 'healthy'; });
    var queue = affected.slice();
    var migrated = 0, unrecovered = 0;
    while (queue.length) {
      var batch = queue.splice(0, policy.concurrency);
      var flows = [];
      batch.forEach(function (w, idx) {
        var target = targets[idx % targets.length];
        w.status = 'migrating';
        w.targetNodeId = target.id;
        flows.push({ from: w.nodeId, to: target.id, kind: 'migrate' });
      });
      ctx.snapshot(flows, '迁移 ' + batch.length + ' 个负载');
      ctx.advance(r1(0.8 + rng() * 0.9));

      batch.forEach(function (w) {
        var failProb = 0.14 / (1 + policy.retry * 0.9);
        var fail = rng() < failProb;
        if (fail && policy.retry > 0) {
          ctx.emit('warn', '负载 <' + w.id + '> 迁移超时，触发重试');
          ctx.advance(0.5);
          fail = rng() < 0.07 / (1 + policy.retry * 0.5);
        }
        if (fail) {
          w.status = 'stopped';
          unrecovered++;
          ctx.emit('error', '负载 <' + w.id + '> 迁移失败，已用尽重试');
        } else {
          w.status = 'running';
          w.nodeId = w.targetNodeId;
          migrated++;
          ctx.emit('ok', '负载 <' + w.id + '> 已在 <' + w.targetNodeId + '> 恢复运行');
        }
      });
      ctx.snapshot([], '批次完成');
      ctx.advance(0.3);
    }

    // 4) replacement nodes join back
    victims.forEach(function (n) {
      n.status = 'recovering';
      ctx.emit('step', '节点 <' + n.id + '> 进入替换流程');
    });
    ctx.snapshot([], '故障节点替换中');
    ctx.advance(1.2);
    victims.forEach(function (n) {
      n.status = 'healthy';
      ctx.emit('ok', '节点 <' + n.id + '> 替换完成，重新加入集群');
    });

    var mttr = r1(ctx.t - failT);
    ctx.emit('ok', '演练结束 · 恢复 ' + migrated + '/' + affected.length + ' 负载 · MTTR ' + mttr + 's');
    ctx.snapshot([], '演练完成');

    return {
      mttr: mttr,
      detect: detectLag,
      impact: affected.length,
      recovery: affected.length ? Math.round(migrated / affected.length * 100) : 100,
      detail: migrated + ' recovered / ' + unrecovered + ' lost'
    };
  }

  /* ---------- scenario S2: rolling upgrade with health gate ---------- */
  function scenarioRollingUpgrade(ctx, policy) {
    var rng = ctx.rng, nodes = ctx.nodes, workloads = ctx.workloads;

    ctx.emit('info', '演练开始 · 场景 <S2 滚动升级保护>');
    ctx.emit('info', '分批策略：maxSurge=1 · 健康门禁开启');
    ctx.snapshot([], '集群就绪');

    var batchSize = Math.max(2, Math.ceil(nodes.length / 3));
    var batches = [];
    for (var i = 0; i < nodes.length; i += batchSize) batches.push(nodes.slice(i, i + batchSize));

    var badBatch = 1; // 第二个批次注入坏版本
    var detectLag = r1(policy.health * 0.7 + rng() * 0.8);
    var failT = null, detectT = 0, totalAffected = 0, recovered = 0;

    for (var bi = 0; bi < batches.length; bi++) {
      if (bi > badBatch) {
        ctx.emit('warn', '批次 ' + (bi + 1) + ' 阻塞：等待人工确认后继续');
        continue;
      }
      var batch = batches[bi];
      if (bi === badBatch) failT = ctx.t;
      batch.forEach(function (n) { n.status = 'recovering'; });
      ctx.emit('step', '批次 ' + (bi + 1) + '/' + batches.length + ' 开始升级（' + batch.length + ' 节点）');
      ctx.snapshot([], '批次 ' + (bi + 1) + ' 升级中');
      ctx.advance(r1(1.0 + rng() * 0.7));

      if (bi === badBatch) {
        batch.forEach(function (n) { n.status = 'degraded'; });
        var wl = workOnNodes(workloads, batch);
        wl.forEach(function (w) { w.status = 'stopped'; });
        totalAffected = wl.length;
        ctx.emit('error', '批次 ' + (bi + 1) + ' 健康检查失败：新版本持续返回 5xx');
        ctx.snapshot(wl.map(function (w) { return { to: w.nodeId, kind: 'fail' }; }), '坏版本被识别');
        ctx.advance(detectLag);
        detectT = detectLag;
        ctx.emit('warn', '健康门禁触发：升级已暂停（发现延迟 ' + detectLag + 's）');
        ctx.snapshot([], '升级暂停');

        ctx.emit('warn', '执行自动回滚 · 批次 ' + (bi + 1));
        var attempts = 0, ok = false;
        while (!ok && attempts <= policy.retry) {
          attempts++;
          ctx.advance(r1(0.7 + rng() * 0.5));
          ok = rng() > 0.3 || attempts > policy.retry;
          if (!ok) ctx.emit('warn', '回滚未收敛，重试 ' + attempts + '/' + policy.retry);
        }
        batch.forEach(function (n) { n.status = 'healthy'; });
        wl.forEach(function (w) { w.status = 'running'; recovered++; });
        ctx.emit(ok ? 'ok' : 'error', ok
          ? '批次 ' + (bi + 1) + ' 回滚完成，服务已恢复'
          : '批次 ' + (bi + 1) + ' 回滚后仍有异常，需人工介入');
        ctx.snapshot([], '回滚完成');
      } else {
        batch.forEach(function (n) { n.status = 'healthy'; });
        ctx.emit('ok', '批次 ' + (bi + 1) + ' 升级完成');
        ctx.snapshot([], '批次 ' + (bi + 1) + ' 完成');
      }
      ctx.advance(0.4);
    }

    var mttr = failT === null ? 0 : r1(ctx.t - failT);
    ctx.emit('ok', '演练结束 · 回滚保护生效 · MTTR ' + mttr + 's');
    ctx.snapshot([], '演练完成');

    return {
      mttr: mttr,
      detect: detectT,
      impact: totalAffected,
      recovery: totalAffected ? Math.round(recovered / totalAffected * 100) : 100,
      detail: 'health gate blocked bad rollout'
    };
  }

  /* ---------- scenario S3: scheduler rebalance ---------- */
  function scenarioReschedule(ctx, policy) {
    var rng = ctx.rng, nodes = ctx.nodes, workloads = ctx.workloads;

    ctx.emit('info', '演练开始 · 场景 <S3 调度重排>');
    ctx.emit('info', '当前放置策略：spread · 约束 zone 反亲和');
    ctx.snapshot([], '集群就绪');

    // hotspot detection
    var hotspot = nodes[Math.floor(rng() * nodes.length)];
    var failT = ctx.t;
    hotspot.status = 'failed';
    var affected = workOnNodes(workloads, [hotspot]);
    affected.forEach(function (w) { w.status = 'stopped'; });
    ctx.emit('error', '热点节点 <' + hotspot.id + '> (' + hotspot.zone + ') 过载并失联');
    ctx.emit('warn', '受影响负载 ' + affected.length + ' 个，重新进入调度队列');
    ctx.snapshot([{ to: hotspot.id, kind: 'fail' }], '热点节点失联');
    ctx.advance(r1(policy.health * 0.8 + rng()));

    // rebalance: choose least-loaded healthy nodes
    var placed = 0;
    var healthy = nodes.filter(function (n) { return n.status === 'healthy'; });
    var load = {};
    nodes.forEach(function (n) {
      load[n.id] = workloads.filter(function (w) { return w.nodeId === n.id && w.status === 'running'; }).length;
    });
    var queue = affected.slice();
    while (queue.length) {
      var batch = queue.splice(0, policy.concurrency);
      var flows = [];
      batch.forEach(function (w) {
        var target = healthy.slice().sort(function (a, b) {
          return (load[a.id] - load[b.id]) || (rng() - 0.5);
        })[0];
        load[target.id] = (load[target.id] || 0) + 1;
        w.status = 'migrating';
        w.targetNodeId = target.id;
        flows.push({ from: w.nodeId, to: target.id, kind: 'migrate' });
      });
      ctx.emit('step', '调度决策：重排 ' + batch.length + ' 个负载至低负载节点');
      ctx.snapshot(flows, '重调度中');
      ctx.advance(r1(0.7 + rng() * 0.8));
      batch.forEach(function (w) {
        var fail = rng() < 0.08 / (1 + policy.retry);
        if (fail && policy.retry > 0) {
          ctx.advance(0.4);
          fail = rng() < 0.04;
        }
        if (fail) {
          w.status = 'stopped';
          ctx.emit('error', '负载 <' + w.id + '> 调度失败：反亲和约束冲突');
        } else {
          w.status = 'running';
          w.nodeId = w.targetNodeId;
          placed++;
          ctx.emit('ok', '负载 <' + w.id + '> → <' + w.targetNodeId + '> 调度完成');
        }
      });
      ctx.snapshot([], '批次落位');
    }
    hotspot.status = 'recovering';
    ctx.advance(1.0);
    hotspot.status = 'healthy';
    ctx.emit('ok', '节点 <' + hotspot.id + '> 重新加入调度池');

    var mttr = r1(ctx.t - failT);
    ctx.emit('ok', '演练结束 · 重排 ' + placed + '/' + affected.length + ' · MTTR ' + mttr + 's');
    ctx.snapshot([], '演练完成');

    return {
      mttr: mttr,
      detect: r1(policy.health * 0.8),
      impact: affected.length,
      recovery: affected.length ? Math.round(placed / affected.length * 100) : 100,
      detail: 'spread + zone anti-affinity'
    };
  }

  /* ---------- public API ---------- */
  function simulate(scenario, policy, seed) {
    var clusterRng = mulberry32((seed ^ 0x9E3779B9) >>> 0);
    var cluster = buildCluster(clusterRng, 18);
    var ctx = new SimContext(seed >>> 0, cluster);
    var result;

    if (scenario === 'rolling-upgrade') result = scenarioRollingUpgrade(ctx, policy);
    else if (scenario === 'scheduler') result = scenarioReschedule(ctx, policy);
    else result = scenarioNodeLoss(ctx, policy);

    return {
      scenario: scenario,
      seed: seed >>> 0,
      policy: Object.assign({}, policy),
      nodes: cluster.nodes,
      workloads: cluster.workloads,
      events: ctx.events,
      frames: ctx.frames,
      result: result,
      duration: r1(ctx.t)
    };
  }

  global.InfraGymEngine = {
    simulate: simulate,
    mulberry32: mulberry32,
    VERSION: 'deterministic-v1'
  };
})(window);
