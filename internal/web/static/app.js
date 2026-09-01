const $$ = (sel) => document.querySelector(sel);
const show = (el) => el.classList.remove('hidden');
const hide = (el) => el.classList.add('hidden');

const loginWrap = $$('#login');
const app = $$('#app');

// 检查会话并决定显示登录页还是主界面。
async function checkAuth() {
  const r = await fetch('/api/status');
  if (r.ok) {
    hide(loginWrap);
    show(app);
    loadData();
    setInterval(loadData, 3000);
  } else {
    show(loginWrap);
    hide(app);
  }
}

// 登录。
$$('#loginForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const username = $$('#username').value.trim();
  const password = $$('#password').value;
  const errEl = $$('#loginErr');
  errEl.textContent = '';
  const r = await fetch('/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password })
  });
  if (r.ok) {
    checkAuth();
  } else {
    errEl.textContent = '登录失败:用户名或密码错误';
  }
});

// 登出。
$$('#logout').addEventListener('click', async () => {
  await fetch('/api/logout', { method: 'POST' });
  location.reload();
});

// 轮询加载数据。
async function loadData() {
  const [status, devices, conns] = await Promise.all([
    fetch('/api/status').then(r => r.json()),
    fetch('/api/devices').then(r => r.json()),
    fetch('/api/connections').then(r => r.json()),
  ]);
  updateStatus(status);
  updateDevices(devices);
  updateConnections(conns);
}

function updateStatus(s) {
  $$('#uptime').textContent = '运行 ' + formatSeconds(s.uptime_seconds);
  $$('#mDevices').textContent = s.totals.device_count;
  $$('#mActive').textContent = s.totals.active_conns;
  $$('#mTx').textContent = formatBytes(s.totals.tx_bytes);
  $$('#mRx').textContent = formatBytes(s.totals.rx_bytes);
  $$('#mUpstream').textContent = s.upstream_type.toUpperCase() + ' ' + s.upstream;
}

function updateDevices(devs) {
  const tbody = $$('#deviceRows');
  tbody.innerHTML = devs.map(d => `
    <tr>
      <td>${d.ip}</td>
      <td>${d.hostname || '-'}</td>
      <td>${formatBytes(d.tx_bytes)}</td>
      <td>${formatBytes(d.rx_bytes)}</td>
      <td>${d.active_conns}</td>
      <td>${d.total_conns}</td>
      <td>${formatTime(d.last_seen)}</td>
    </tr>
  `).join('');
}

function updateConnections(conns) {
  const tbody = $$('#connRows');
  tbody.innerHTML = conns.map(c => `
    <tr>
      <td>${c.src_ip}</td>
      <td>${c.dst_ip}:${c.dst_port}</td>
      <td><span class="badge ${c.upstream}">${c.upstream}</span></td>
      <td>${formatBytes(c.tx_bytes)}</td>
      <td>${formatBytes(c.rx_bytes)}</td>
      <td>${formatTime(c.closed_at)}</td>
    </tr>
  `).join('');
}

function formatBytes(b) {
  if (b < 1024) return b + ' B';
  if (b < 1048576) return (b / 1024).toFixed(1) + ' KB';
  if (b < 1073741824) return (b / 1048576).toFixed(1) + ' MB';
  return (b / 1073741824).toFixed(2) + ' GB';
}

function formatSeconds(s) {
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return h + 'h' + m + 'm';
  if (m > 0) return m + 'm';
  return s + 's';
}

function formatTime(ts) {
  if (!ts) return '-';
  const d = new Date(ts * 1000);
  const now = new Date();
  const diff = Math.floor((now - d) / 1000);
  if (diff < 60) return diff + '秒前';
  if (diff < 3600) return Math.floor(diff / 60) + '分钟前';
  if (diff < 86400) return Math.floor(diff / 3600) + '小时前';
  return d.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
}

checkAuth();
