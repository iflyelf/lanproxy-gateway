'use strict';

/* ============ 工具函数 ============ */
const $ = (id) => document.getElementById(id);
const fmtBytes = (b) => {
  if (b < 1024) return b + ' B';
  if (b < 1048576) return (b / 1024).toFixed(1) + ' KB';
  if (b < 1073741824) return (b / 1048576).toFixed(1) + ' MB';
  return (b / 1073741824).toFixed(2) + ' GB';
};
const fmtRate = (b) => fmtBytes(b) + '/s';
const fmtTime = (ts) => {
  if (!ts) return '-';
  const d = new Date(ts * 1000);
  const diff = Math.floor((Date.now() - d.getTime()) / 1000);
  if (diff < 60) return diff + ' 秒前';
  if (diff < 3600) return Math.floor(diff / 60) + ' 分前';
  if (diff < 86400) return Math.floor(diff / 3600) + ' 时前';
  return d.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
};
const fmtUptime = (s) => {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  if (d > 0) return `运行 ${d}天${h}时`;
  if (h > 0) return `运行 ${h}时${m}分`;
  return `运行 ${m}分`;
};

/* ============ 主题 ============ */
function initTheme() {
  let theme = localStorage.getItem('lpg_theme');
  if (!theme) {
    // 首次:按系统偏好(暗→石墨深色 / 亮→暖沙米)
    theme = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'beige';
  }
  applyTheme(theme);
}
function applyTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme);
  localStorage.setItem('lpg_theme', theme);
  document.querySelectorAll('.theme-opt').forEach((el) => {
    el.classList.toggle('active', el.dataset.theme === theme);
  });
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) {
    const bg = getComputedStyle(document.body).backgroundColor;
    meta.setAttribute('content', bg);
  }
  // 重绘图表以适配主题色
  if (window._trafficChart) window._trafficChart.draw();
  if (window._topo) window._topo.draw();
}

/* ============ 页面路由 ============ */
function initTabs() {
  document.querySelectorAll('.tab').forEach((tab) => {
    tab.addEventListener('click', () => switchPage(tab.dataset.page));
  });
}
function switchPage(name) {
  document.querySelectorAll('.tab').forEach((t) => t.classList.toggle('active', t.dataset.page === name));
  document.querySelectorAll('.page').forEach((p) => p.classList.add('hidden'));
  $('page-' + name).classList.remove('hidden');
  if (name === 'settings') loadNetworkInfo();
  if (name === 'logs') loadLogs();
  // 切到总览时重算 canvas 尺寸
  if (name === 'overview') {
    setTimeout(() => {
      if (window._trafficChart) window._trafficChart.resize();
      if (window._topo) window._topo.resize();
    }, 0);
  }
}

/* ============ 认证 ============ */
async function checkAuth() {
  try {
    const r = await fetch('/api/status');
    if (r.ok) { showApp(); return; }
  } catch (e) {}
  showLogin();
}
function showLogin() {
  $('login').classList.remove('hidden');
  $('app').classList.add('hidden');
}
function showApp() {
  $('login').classList.add('hidden');
  $('app').classList.remove('hidden');
  // 页面显示后重算 canvas 尺寸(初始化时容器隐藏,尺寸为 0)
  requestAnimationFrame(() => {
    if (window._trafficChart) window._trafficChart.resize();
    if (window._topo) window._topo.resize();
  });
  startPolling();
}

$('loginForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const err = $('loginErr');
  err.textContent = '';
  try {
    const r = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: $('username').value.trim(), password: $('password').value }),
    });
    if (r.ok) { showApp(); }
    else { err.textContent = '登录失败:用户名或密码错误'; }
  } catch (e) { err.textContent = '网络错误'; }
});

$('logoutBtn').addEventListener('click', async () => {
  await fetch('/api/logout', { method: 'POST' });
  location.reload();
});

/* ============ 数据轮询 ============ */
let pollTimer = null;
let connFilter = { upstream: '', src: '' };

function startPolling() {
  if (pollTimer) return;
  loadAll();
  pollTimer = setInterval(loadAll, 3000);
}

async function loadAll() {
  try {
    const [status, devices, conns, traffic] = await Promise.all([
      fetch('/api/status').then((r) => { if (r.status === 401) throw new Error('UNAUTHORIZED'); return r.json(); }),
      fetch('/api/devices').then((r) => { if (r.status === 401) throw new Error('UNAUTHORIZED'); return r.json(); }),
      fetch('/api/connections').then((r) => { if (r.status === 401) throw new Error('UNAUTHORIZED'); return r.json(); }),
      fetch('/api/traffic').then((r) => { if (r.status === 401) throw new Error('UNAUTHORIZED'); return r.json(); }),
    ]);
    renderStatus(status);
    renderDevices(devices);
    renderConns(conns);
    if (window._trafficChart) window._trafficChart.update(traffic || []);
    if (window._topo) window._topo.update(devices || []);
  } catch (e) {
    // 仅 401 未授权时退出登录,其他错误(网络抖动/超时/5xx)静默重试,避免误退出
    if (e.message === 'UNAUTHORIZED') {
      showLogin();
      if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
    }
  }
}

function renderStatus(s) {
  $('uptime').textContent = fmtUptime(s.uptime_seconds);
  const t = s.totals || {};
  $('mTx').textContent = fmtBytes(t.tx_bytes || 0);
  $('mRx').textContent = fmtBytes(t.rx_bytes || 0);
  $('mActive').textContent = t.active_conns || 0;
  $('mDevices').textContent = t.device_count || 0;
  // 设置页
  $('cfgUpstream').textContent = s.upstream || '-';
  $('cfgUpstreamType').textContent = (s.upstream_type || '-').toUpperCase();
  $('cfgLAN').textContent = s.lan_interface || '(自动)';
  setBadge($('cfgFallback'), s.fallback_direct);
  setBadge($('cfgQUIC'), s.block_quic);
  setBadge($('cfgIPv6'), s.enable_ipv6);
}
function setBadge(el, on) {
  el.textContent = on ? '开启' : '关闭';
  el.className = 'badge ' + (on ? 'on' : 'off');
}

function renderDevices(devs) {
  devs = devs || [];
  const tbody = $('deviceRows');
  $('deviceEmpty').classList.toggle('hidden', devs.length > 0);
  tbody.innerHTML = devs.map((d) => `
    <tr>
      <td>${d.ip}</td>
      <td>${d.hostname || '-'}</td>
      <td>${fmtBytes(d.tx_bytes)}</td>
      <td>${fmtBytes(d.rx_bytes)}</td>
      <td>${d.active_conns}</td>
      <td>${d.total_conns}</td>
      <td>${fmtTime(d.last_seen)}</td>
    </tr>`).join('');
}

function renderConns(conns) {
  conns = (conns || []).filter((c) => {
    if (connFilter.upstream && c.upstream !== connFilter.upstream) return false;
    if (connFilter.src && !c.src_ip.includes(connFilter.src)) return false;
    return true;
  });
  const tbody = $('connRows');
  $('connEmpty').classList.toggle('hidden', conns.length > 0);
  tbody.innerHTML = conns.map((c) => `
    <tr>
      <td>${c.src_ip}</td>
      <td>${c.dst_ip}:${c.dst_port}</td>
      <td><span class="badge ${c.upstream}">${upstreamLabel(c.upstream)}</span></td>
      <td>${fmtBytes(c.tx_bytes)}</td>
      <td>${fmtBytes(c.rx_bytes)}</td>
      <td>${fmtTime(c.closed_at)}</td>
    </tr>`).join('');
}
function upstreamLabel(u) {
  return u === 'proxy' ? '代理' : u === 'direct' ? '直连' : u === 'failed' ? '失败' : u;
}

$('filterUpstream').addEventListener('change', (e) => { connFilter.upstream = e.target.value; });
$('filterSrc').addEventListener('input', (e) => { connFilter.src = e.target.value.trim(); });

document.querySelectorAll('.theme-opt').forEach((el) => {
  el.addEventListener('click', () => applyTheme(el.dataset.theme));
});

/* ============ 网络信息(仅 CloudFlare + 墙外) ============ */
async function loadNetworkInfo() {
  fetchCF();
  fetchOuter();
}
async function fetchCF() {
  try {
    const r = await fetch('https://help.x.com/cdn-cgi/trace?t=' + Date.now());
    const text = await r.text();
    const kv = {};
    text.split('\n').forEach((line) => {
      const i = line.indexOf('=');
      if (i > 0) kv[line.slice(0, i)] = line.slice(i + 1);
    });
    $('cfDot').className = 'dot ok';
    $('cfIP').textContent = kv.ip || '-';
    $('cfLoc').textContent = [kv.loc, kv.colo].filter(Boolean).join(' · ');
  } catch (e) {
    $('cfDot').className = 'dot err';
    $('cfIP').textContent = '获取失败';
    $('cfLoc').textContent = '';
  }
}
async function fetchOuter() {
  try {
    const r = await fetch('https://api.cmliussss.net/api/ipinfo?t=' + Date.now());
    const d = await r.json();
    $('outerDot').className = 'dot ok';
    $('outerIP').textContent = d.ip || d.query || '-';
    $('outerLoc').textContent = [d.country, d.regionName || d.region, d.city].filter(Boolean).join(' · ');
  } catch (e) {
    $('outerDot').className = 'dot err';
    $('outerIP').textContent = '获取失败';
    $('outerLoc').textContent = '';
  }
}

/* ============ 初始化 ============ */
initTheme();
initTabs();
setupLogControls();
startLogAuto();
checkAuth();

/* ============ 流量曲线图 ============ */
class TrafficChart {
  constructor(canvasId) {
    this.canvas = $(canvasId);
    this.ctx = this.canvas.getContext('2d');
    this.samples = [];
    this.resize();
    window.addEventListener('resize', () => this.resize());
  }
  resize() {
    const dpr = window.devicePixelRatio || 1;
    const rect = this.canvas.getBoundingClientRect();
    this.canvas.width = rect.width * dpr;
    this.canvas.height = rect.height * dpr;
    this.ctx.scale(dpr, dpr);
    this.w = rect.width;
    this.h = rect.height;
    this.draw();
  }
  update(samples) {
    this.samples = samples || [];
    // 若 canvas 尺寸为 0(初始化时容器隐藏),先重算尺寸再绘制
    if (!this.w) this.resize();
    this.draw();
  }
  draw() {
    if (!this.w) return;
    const { ctx, w, h, samples } = this;
    const style = getComputedStyle(document.body);
    const bgColor = style.getPropertyValue('--panel').trim();
    const txColor = style.getPropertyValue('--chart-tx').trim();
    const rxColor = style.getPropertyValue('--chart-rx').trim();
    const textColor = style.getPropertyValue('--muted').trim();
    const gridColor = style.getPropertyValue('--border').trim();

    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = bgColor;
    ctx.fillRect(0, 0, w, h);

    if (samples.length < 2) {
      ctx.fillStyle = textColor;
      ctx.font = '13px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('等待数据...', w / 2, h / 2);
      return;
    }

    const pad = { t: 18, r: 12, b: 22, l: 50 };
    const cw = w - pad.l - pad.r, ch = h - pad.t - pad.b;
    const maxRate = Math.max(...samples.map(s => Math.max(s.tx_rate, s.rx_rate)), 1024);
    const yScale = ch / maxRate;
    const xStep = cw / (samples.length - 1);

    // 网格
    ctx.strokeStyle = gridColor;
    ctx.lineWidth = 0.5;
    for (let i = 0; i <= 4; i++) {
      const y = pad.t + (ch * i / 4);
      ctx.beginPath(); ctx.moveTo(pad.l, y); ctx.lineTo(w - pad.r, y); ctx.stroke();
    }

    // Y轴标签
    ctx.fillStyle = textColor;
    ctx.font = '11px sans-serif';
    ctx.textAlign = 'right';
    for (let i = 0; i <= 4; i++) {
      const rate = maxRate * (4 - i) / 4;
      ctx.fillText(fmtRate(rate), pad.l - 6, pad.t + (ch * i / 4) + 4);
    }

    // 曲线
    const drawLine = (color, key) => {
      ctx.strokeStyle = color;
      ctx.lineWidth = 2;
      ctx.beginPath();
      samples.forEach((s, i) => {
        const x = pad.l + i * xStep;
        const y = pad.t + ch - s[key] * yScale;
        i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y);
      });
      ctx.stroke();
    };
    drawLine(txColor, 'tx_rate');
    drawLine(rxColor, 'rx_rate');
  }
}

/* ============ 力导向流量拓扑 ============ */
class ForceTopology {
  constructor(canvasId) {
    this.canvas = $(canvasId);
    this.ctx = this.canvas.getContext('2d');
    this.nodes = [];
    this.edges = [];
    this.dragging = null;
    this.animating = false;
    this.resize();
    this.setupEvents();
    window.addEventListener('resize', () => this.resize());
  }
  resize() {
    const dpr = window.devicePixelRatio || 1;
    const rect = this.canvas.getBoundingClientRect();
    this.canvas.width = rect.width * dpr;
    this.canvas.height = rect.height * dpr;
    this.ctx.scale(dpr, dpr);
    this.w = rect.width;
    this.h = rect.height;
    this.draw();
  }
  update(devices) {
    devices = devices || [];
    // 若 canvas 尺寸为 0(初始化时容器隐藏),先重算尺寸再更新
    if (!this.w) this.resize();
    // 限制渲染节点数(防 O(n²) 卡顿)
    const top = devices.slice(0, 200);
    const gateway = { id: 'gateway', label: '网关', x: this.w / 2, y: this.h / 2, vx: 0, vy: 0, r: 16, fixed: true };
    this.nodes = [gateway, ...top.map((d, i) => ({
      id: d.ip,
      label: d.hostname || d.ip,
      traffic: d.tx_bytes + d.rx_bytes,
      conns: d.active_conns,
      x: this.w / 2 + Math.cos(i / top.length * Math.PI * 2) * 120,
      y: this.h / 2 + Math.sin(i / top.length * Math.PI * 2) * 120,
      vx: 0, vy: 0,
      r: 6 + Math.min(d.active_conns * 1.5, 16),
    }))];
    this.edges = top.map(d => ({ source: 'gateway', target: d.ip, width: Math.max(1, Math.log(d.tx_bytes + d.rx_bytes + 1) / 4) }));
    if (!this.animating) this.simulate();
  }
  simulate() {
    this.animating = true;
    let ticks = 0;
    const tick = () => {
      this.applyForces();
      this.draw();
      ticks++;
      if (ticks < 180 && !this.stable()) requestAnimationFrame(tick);
      else this.animating = false;
    };
    requestAnimationFrame(tick);
  }
  applyForces() {
    const { nodes, edges } = this;
    const k = 800, damping = 0.85;
    // 斥力
    for (let i = 0; i < nodes.length; i++) {
      for (let j = i + 1; j < nodes.length; j++) {
        const a = nodes[i], b = nodes[j];
        if (a.fixed && b.fixed) continue;
        const dx = b.x - a.x, dy = b.y - a.y;
        const d = Math.sqrt(dx * dx + dy * dy) || 1;
        const f = k / (d * d);
        const fx = (dx / d) * f, fy = (dy / d) * f;
        if (!a.fixed) { a.vx -= fx; a.vy -= fy; }
        if (!b.fixed) { b.vx += fx; b.vy += fy; }
      }
    }
    // 边引力
    edges.forEach(e => {
      const a = nodes.find(n => n.id === e.source);
      const b = nodes.find(n => n.id === e.target);
      if (!a || !b || (a.fixed && b.fixed)) return;
      const dx = b.x - a.x, dy = b.y - a.y;
      const d = Math.sqrt(dx * dx + dy * dy) || 1;
      const f = d * 0.002;
      const fx = (dx / d) * f, fy = (dy / d) * f;
      if (!a.fixed) { a.vx += fx; a.vy += fy; }
      if (!b.fixed) { b.vx -= fx; b.vy -= fy; }
    });
    // 应用速度 + 阻尼
    nodes.forEach(n => {
      if (n.fixed) return;
      n.x += n.vx; n.y += n.vy;
      n.vx *= damping; n.vy *= damping;
      // 边界限制
      n.x = Math.max(n.r + 10, Math.min(this.w - n.r - 10, n.x));
      n.y = Math.max(n.r + 10, Math.min(this.h - n.r - 10, n.y));
    });
  }
  stable() {
    return this.nodes.every(n => n.fixed || (Math.abs(n.vx) < 0.1 && Math.abs(n.vy) < 0.1));
  }
  draw() {
    if (!this.w) return;
    const { ctx, w, h, nodes, edges } = this;
    const style = getComputedStyle(document.body);
    const bgColor = style.getPropertyValue('--panel').trim();
    const accentColor = style.getPropertyValue('--accent').trim();
    const textColor = style.getPropertyValue('--text').trim();
    const mutedColor = style.getPropertyValue('--muted').trim();

    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = bgColor;
    ctx.fillRect(0, 0, w, h);

    if (nodes.length < 2) {
      ctx.fillStyle = mutedColor;
      ctx.font = '13px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText('等待设备数据...', w / 2, h / 2);
      return;
    }

    // 边
    ctx.strokeStyle = mutedColor;
    ctx.globalAlpha = 0.35;
    edges.forEach(e => {
      const a = nodes.find(n => n.id === e.source);
      const b = nodes.find(n => n.id === e.target);
      if (!a || !b) return;
      ctx.lineWidth = e.width;
      ctx.beginPath();
      ctx.moveTo(a.x, a.y);
      ctx.lineTo(b.x, b.y);
      ctx.stroke();
    });
    ctx.globalAlpha = 1;

    // 节点
    nodes.forEach(n => {
      ctx.fillStyle = n.id === 'gateway' ? accentColor : textColor;
      ctx.beginPath();
      ctx.arc(n.x, n.y, n.r, 0, Math.PI * 2);
      ctx.fill();
      if (n.id === 'gateway') {
        ctx.strokeStyle = '#fff';
        ctx.lineWidth = 2;
        ctx.stroke();
      }
    });

    // 标签(仅网关)
    ctx.fillStyle = textColor;
    ctx.font = '13px sans-serif';
    ctx.textAlign = 'center';
    const gw = nodes[0];
    ctx.fillText(gw.label, gw.x, gw.y - gw.r - 6);
  }
  setupEvents() {
    const getPos = (e) => {
      const rect = this.canvas.getBoundingClientRect();
      const x = (e.clientX || e.touches[0].clientX) - rect.left;
      const y = (e.clientY || e.touches[0].clientY) - rect.top;
      return { x, y };
    };
    const findNode = (x, y) => this.nodes.find(n => Math.hypot(n.x - x, n.y - y) < n.r + 5);
    this.canvas.addEventListener('mousedown', (e) => {
      const { x, y } = getPos(e);
      this.dragging = findNode(x, y);
      if (this.dragging && !this.dragging.fixed) { this.dragging.vx = 0; this.dragging.vy = 0; }
    });
    this.canvas.addEventListener('touchstart', (e) => {
      const { x, y } = getPos(e);
      this.dragging = findNode(x, y);
      if (this.dragging && !this.dragging.fixed) { this.dragging.vx = 0; this.dragging.vy = 0; }
    }, { passive: true });
    this.canvas.addEventListener('mousemove', (e) => {
      if (!this.dragging || this.dragging.fixed) return;
      const { x, y } = getPos(e);
      this.dragging.x = x;
      this.dragging.y = y;
      this.draw();
    });
    this.canvas.addEventListener('touchmove', (e) => {
      if (!this.dragging || this.dragging.fixed) return;
      const { x, y } = getPos(e);
      this.dragging.x = x;
      this.dragging.y = y;
      this.draw();
    }, { passive: true });
    const endDrag = () => { this.dragging = null; };
    this.canvas.addEventListener('mouseup', endDrag);
    this.canvas.addEventListener('mouseleave', endDrag);
    this.canvas.addEventListener('touchend', endDrag);
  }
}

window._trafficChart = new TrafficChart('trafficChart');
window._topo = new ForceTopology('topoCanvas');

/* ============ 日志查看 ============ */
let logAutoTimer = null;

async function loadLogs() {
  const level = $('logLevel').value;
  const lines = $('logLines').value;
  const q = $('logKeyword').value.trim();
  const params = new URLSearchParams({ lines });
  if (level) params.set('level', level);
  if (q) params.set('q', q);
  try {
    const resp = await fetch('/api/logs?' + params.toString());
    if (!resp.ok) { $('logMeta').textContent = '读取日志失败'; return; }
    const data = await resp.json();
    renderLogs(data);
  } catch (e) {
    $('logMeta').textContent = '网络错误';
  }
}

function renderLogs(data) {
  const meta = $('logMeta');
  if (!data.enabled) {
    meta.textContent = '未启用文件日志(log.path 为空),仅控制台输出';
    $('logView').innerHTML = '';
    return;
  }
  meta.textContent = `文件: ${data.file}  ·  级别: ${(data.level||'info').toUpperCase()}  ·  共 ${data.lines.length} 行`;
  const view = $('logView');
  view.innerHTML = data.lines.map(escapeAndColor).join('\n');
  // 滚动到底部
  view.scrollTop = view.scrollHeight;
}

function escapeAndColor(line) {
  const esc = line.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  let cls = '';
  if (line.includes('[ERROR]')) cls = 'lv-error';
  else if (line.includes('[WARN]')) cls = 'lv-warn';
  else if (line.includes('[DEBUG]')) cls = 'lv-debug';
  else if (line.includes('[INFO]')) cls = 'lv-info';
  return cls ? `<span class="${cls}">${esc}</span>` : esc;
}

function setupLogControls() {
  $('logRefresh').addEventListener('click', loadLogs);
  $('logLevel').addEventListener('change', loadLogs);
  $('logLines').addEventListener('change', loadLogs);
  $('logKeyword').addEventListener('input', debounce(loadLogs, 400));
  $('logAuto').addEventListener('change', (e) => {
    if (e.target.checked) startLogAuto();
    else stopLogAuto();
  });
}

function startLogAuto() {
  stopLogAuto();
  logAutoTimer = setInterval(() => {
    if (!$('page-logs').classList.contains('hidden')) loadLogs();
  }, 5000);
}
function stopLogAuto() {
  if (logAutoTimer) { clearInterval(logAutoTimer); logAutoTimer = null; }
}

function debounce(fn, ms) {
  let t = null;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}
