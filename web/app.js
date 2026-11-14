'use strict';
// Modbus proxy web UI – plain JavaScript, no framework, no build step.

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) =>
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const state = {
  tab: 'devices',
  devices: [],      // working copy: {name, host, port, timeout, listenPort, _orig}
  original: [],     // as read from the config
  hash: '',
  readOnly: false,
  reserved: {},
  uiPort: 80,
  conns: null,
  editIndex: null,
  timers: {},
};

// ---------- API ----------

async function api(method, path, body) {
  const opt = { method, headers: { 'X-Modbusui': '1' }, credentials: 'same-origin' };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  let r;
  try {
    r = await fetch(path, opt);
  } catch (e) {
    throw new Error('No connection to the web UI.');
  }
  let data = null;
  try { data = await r.json(); } catch (_) { /* empty */ }
  if (r.status === 401 && path !== '/api/login') {
    showLogin();
  }
  if (!r.ok) {
    const err = new Error((data && data.error) || `Error ${r.status}`);
    err.status = r.status;
    err.data = data;
    throw err;
  }
  return data;
}

// ---------- helpers ----------

function toast(msg, ms = 3500) {
  const t = $('#toast');
  t.textContent = msg;
  t.classList.remove('hidden');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => t.classList.add('hidden'), ms);
}

function fmtBytes(n) {
  if (n == null) return '–';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return (i === 0 ? String(v) : v.toFixed(v < 10 ? 2 : 1)) + ' ' + u[i];
}

function fmtDuration(sec) {
  if (!sec || sec < 0) return '–';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d} d ${h} h ${m} min`;
  if (h > 0) return `${h} h ${m} min`;
  if (m > 0) return `${m} min ${sec % 60} s`;
  return `${sec} s`;
}

function fmtTime(unix) {
  if (!unix) return '–';
  return new Date(unix * 1000).toLocaleString();
}

function fmtNum(n) {
  return Number(n).toLocaleString();
}

function suggestPort(ip) {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(ip.trim());
  if (!m) return null;
  const last = Number(m[4]);
  if (last > 255) return null;
  return 50200 + (last % 100);
}

function confirmDlg(title, text, okLabel = 'OK', danger = false) {
  return new Promise((resolve) => {
    const dlg = $('#confirmDlg');
    $('#confirmTitle').textContent = title;
    $('#confirmText').textContent = text;
    const yes = $('#confirmYes');
    yes.textContent = okLabel;
    yes.className = danger ? 'btn btn-danger' : 'btn btn-primary';
    const done = (v) => {
      yes.onclick = null; $('#confirmNo').onclick = null; dlg.onclose = null;
      if (dlg.open) dlg.close();
      resolve(v);
    };
    yes.onclick = () => done(true);
    $('#confirmNo').onclick = () => done(false);
    dlg.onclose = () => done(false);
    dlg.showModal();
  });
}

// ---------- login ----------

function showLogin(msg) {
  stopTimers();
  $('#app').classList.add('hidden');
  $('#login').classList.remove('hidden');
  const m = $('#loginMsg');
  if (msg) { m.textContent = msg; m.classList.remove('hidden'); } else { m.classList.add('hidden'); }
  $('#pw').value = '';
  $('#pw').focus();
}

function showApp() {
  $('#login').classList.add('hidden');
  $('#app').classList.remove('hidden');
  refreshPill();
  state.timers.pill = setInterval(refreshPill, 10000);
  switchTab(state.tab);
}

function stopTimers() {
  Object.values(state.timers).forEach((t) => clearInterval(t));
  state.timers = {};
}

$('#loginForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const btn = $('#loginForm button');
  btn.disabled = true;
  try {
    await api('POST', '/api/login', { password: $('#pw').value });
    showApp();
  } catch (err) {
    showLogin(err.message);
  } finally {
    btn.disabled = false;
  }
});

$('#logoutBtn').addEventListener('click', async () => {
  try { await api('POST', '/api/logout'); } catch (_) { /* ignore */ }
  showLogin();
});

// ---------- tabs ----------

$('#tabs').addEventListener('click', (e) => {
  const b = e.target.closest('button[data-tab]');
  if (b) switchTab(b.dataset.tab);
});

function switchTab(tab) {
  state.tab = tab;
  try { localStorage.setItem('modbusui.tab', tab); } catch (_) { /* ignore */ }
  $$('#tabs button').forEach((b) => b.classList.toggle('active', b.dataset.tab === tab));
  $$('.tab').forEach((s) => s.classList.toggle('hidden', s.id !== 'tab-' + tab));
  clearInterval(state.timers.tab);
  delete state.timers.tab;
  if (tab === 'devices') {
    // do not overwrite unsaved changes when switching tabs
    if (changes().total === 0) loadDevices(); else renderDevices();
    loadConns(true);
  }
  if (tab === 'service') { loadService(); state.timers.tab = setInterval(loadService, 5000); }
  if (tab === 'conns') { loadConns(); state.timers.tab = setInterval(loadConns, 3000); }
  if (tab === 'test') { prepareTest(); }
  if (tab === 'system') { loadSystem(); }
}

// ---------- status pill ----------

async function refreshPill() {
  const p = $('#statusPill');
  try {
    const s = await api('GET', '/api/service');
    const px = s.proxy;
    p.className = 'pill';
    if (px.error) { p.textContent = 'Status unknown'; p.classList.add('warn'); return; }
    if (px.active === 'active') {
      p.textContent = 'Proxy running · ' + fmtDuration(px.uptimeSec);
      p.classList.add('ok');
    } else {
      p.textContent = 'Proxy ' + (px.active === 'inactive' ? 'stopped' : px.active + '/' + px.sub);
      p.classList.add('bad');
    }
  } catch (_) {
    p.className = 'pill warn';
    p.textContent = 'no connection';
  }
}

// ---------- devices ----------

const FIELDS = ['name', 'host', 'port', 'timeout', 'listenPort'];

function cleanDev(d) {
  return { name: d.name, host: d.host, port: Number(d.port), timeout: Number(d.timeout), listenPort: Number(d.listenPort) };
}

function sameDev(a, b) {
  return FIELDS.every((f) => String(a[f]) === String(b[f]));
}

async function loadDevices() {
  try {
    const r = await api('GET', '/api/devices');
    state.original = r.devices.map(cleanDev);
    state.devices = r.devices.map((d, i) => ({ ...cleanDev(d), _orig: i }));
    state.hash = r.hash;
    state.readOnly = r.readOnly;
    state.reserved = r.reserved || {};
    state.uiPort = r.uiPort;
    const pb = $('#devProblem');
    if (r.readOnly) {
      pb.innerHTML = `<strong>Saving is blocked.</strong> The config <code>${esc(r.path)}</code> contains something the UI does not know:<br>${esc(r.problem)}` +
        (r.raw ? `<pre class="pre">${esc(r.raw)}</pre>` : '');
      pb.classList.remove('hidden');
    } else {
      pb.classList.add('hidden');
    }
    $('#addDevice').disabled = r.readOnly;
    renderDevices();
  } catch (err) {
    toast(err.message);
  }
}

function changes() {
  let added = 0; let changed = 0;
  const kept = new Set();
  state.devices.forEach((d) => {
    if (d._orig == null) { added++; return; }
    kept.add(d._orig);
    if (!sameDev(d, state.original[d._orig])) changed++;
  });
  const removed = state.original.length - kept.size;
  return { added, changed, removed, total: added + changed + removed };
}

function renderDevices() {
  const tb = $('#devTable tbody');
  const clientsByPort = {};
  if (state.conns) state.conns.devices.forEach((c) => { clientsByPort[c.listenPort] = c; });
  if (state.devices.length === 0) {
    tb.innerHTML = '<tr><td colspan="6" class="empty">No devices configured.</td></tr>';
  } else {
    tb.innerHTML = state.devices.map((d, i) => {
      let cls = ''; let badge = '';
      if (d._orig == null) { cls = 'row-new'; badge = '<span class="badge badge-new">new</span>'; }
      else if (!sameDev(d, state.original[d._orig])) { cls = 'row-changed'; badge = '<span class="badge badge-changed">changed</span>'; }
      const c = clientsByPort[d.listenPort];
      let clients = '<span class="muted">–</span>';
      if (c && d._orig != null && sameDev(d, state.original[d._orig])) {
        clients = clientList(c.clients);
      }
      return `<tr class="${cls}">
        <td>${esc(d.name) || '<span class="muted">(unnamed)</span>'}${badge}</td>
        <td class="mono">${esc(d.host)}:${esc(d.port)}</td>
        <td class="mono">${esc(d.listenPort)}</td>
        <td>${esc(d.timeout)} s</td>
        <td>${clients}</td>
        <td class="actions">
          ${state.readOnly ? '' : `<button class="btn-link" data-edit="${i}">Edit</button>
          <button class="btn-link danger" data-del="${i}">Delete</button>`}
          ${d._orig != null ? `<button class="btn-link" data-test="${d.listenPort}">Test</button>` : ''}
        </td>
      </tr>`;
    }).join('');
  }
  const ch = changes();
  const bar = $('#saveBar');
  if (ch.total > 0 && !state.readOnly) {
    const parts = [];
    if (ch.added) parts.push(`${ch.added} new`);
    if (ch.changed) parts.push(`${ch.changed} changed`);
    if (ch.removed) parts.push(`${ch.removed} deleted`);
    $('#saveInfo').textContent = 'Not saved: ' + parts.join(', ');
    bar.classList.remove('hidden');
  } else {
    bar.classList.add('hidden');
  }
}

$('#devTable').addEventListener('click', async (e) => {
  const b = e.target.closest('button');
  if (!b) return;
  if (b.dataset.edit != null) openDevDialog(Number(b.dataset.edit));
  if (b.dataset.del != null) {
    const i = Number(b.dataset.del);
    const d = state.devices[i];
    const ok = await confirmDlg('Delete device?', `“${d.name || d.host}” will be removed from the list. Takes effect only after “Save”.`, 'Delete', true);
    if (ok) { state.devices.splice(i, 1); renderDevices(); }
  }
  if (b.dataset.test != null) {
    try { localStorage.setItem('modbusui.test.port', b.dataset.test); } catch (_) { /* ignore */ }
    switchTab('test');
  }
});

$('#addDevice').addEventListener('click', () => openDevDialog(null));
$('#discardBtn').addEventListener('click', async () => {
  if (await confirmDlg('Discard changes?', 'All unsaved changes will be lost.', 'Discard', true)) loadDevices();
});

// Validation in the browser (the server validates again).
function validateDevice(d, idx, list) {
  const e = {};
  const name = String(d.name || '').trim();
  if (!name) e.name = 'Name is missing.';
  else if (name.length > 100) e.name = 'Name is too long (max. 100 characters).';
  else if (/[\u0000-\u001f\u007f]/.test(name)) e.name = 'Name contains invalid characters.';

  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(String(d.host).trim());
  if (!m || m.slice(1).some((x) => Number(x) > 255 || (x.length > 1 && x[0] === '0'))) e.host = 'Not a valid IPv4 address.';
  else if (['0', '127', '255'].includes(m[1]) || Number(m[1]) >= 224) e.host = 'This address cannot be used for a device.';

  const port = Number(d.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) e.port = 'Device port 1–65535.';
  const to = Number(d.timeout);
  if (!Number.isInteger(to) || to < 1 || to > 60) e.timeout = 'Whole seconds, 1–60.';
  const lp = Number(d.listenPort);
  if (!Number.isInteger(lp) || lp < 1024 || lp > 65535) e.listenPort = 'Proxy port 1024–65535.';
  else if (state.reserved[lp]) e.listenPort = `Port ${lp} is in use (${state.reserved[lp]}).`;

  list.forEach((o, j) => {
    if (j === idx) return;
    if (!e.listenPort && Number(o.listenPort) === lp) e.listenPort = `Proxy port ${lp} is already used by “${o.name}”.`;
    if (!e.host && String(o.host).trim() === String(d.host).trim() && Number(o.port) === port) e.host = `This target is already used by “${o.name}”.`;
  });
  return e;
}

function openDevDialog(index) {
  state.editIndex = index;
  const f = $('#devForm');
  const d = index == null
    ? { name: '', host: '', port: 502, timeout: 5, listenPort: '' }
    : state.devices[index];
  $('#devDlgTitle').textContent = index == null ? 'Add device' : 'Edit device';
  FIELDS.forEach((k) => { f.elements[k].value = d[k]; f.elements[k].classList.remove('invalid'); });
  $$('.field-err', f).forEach((s) => { s.textContent = ''; });
  f.dataset.portTouched = index == null ? '' : '1';
  $('#devDlg').showModal();
  f.elements.name.focus();
}

$('#devForm').elements.host.addEventListener('input', (e) => {
  const f = $('#devForm');
  if (f.dataset.portTouched) return;
  const s = suggestPort(e.target.value);
  if (s) f.elements.listenPort.value = s;
});
$('#devForm').elements.listenPort.addEventListener('input', () => { $('#devForm').dataset.portTouched = '1'; });
$('#devCancel').addEventListener('click', () => $('#devDlg').close());

$('#devForm').addEventListener('submit', (e) => {
  e.preventDefault();
  const f = e.target;
  const d = {};
  FIELDS.forEach((k) => { d[k] = f.elements[k].value.trim(); });
  const idx = state.editIndex == null ? -1 : state.editIndex;
  const errs = validateDevice(d, idx, state.devices);
  FIELDS.forEach((k) => {
    $(`.field-err[data-for="${k}"]`, f).textContent = errs[k] || '';
    f.elements[k].classList.toggle('invalid', !!errs[k]);
  });
  if (Object.keys(errs).length) return;
  const clean = cleanDev({ ...d, name: d.name.trim() });
  if (state.editIndex == null) state.devices.push({ ...clean, _orig: null });
  else state.devices[state.editIndex] = { ...clean, _orig: state.devices[state.editIndex]._orig };
  $('#devDlg').close();
  renderDevices();
});

$('#saveBtn').addEventListener('click', async () => {
  // validate everything in the browser first
  for (let i = 0; i < state.devices.length; i++) {
    const errs = validateDevice(state.devices[i], i, state.devices);
    if (Object.keys(errs).length) {
      toast(`Device “${state.devices[i].name}”: ${Object.values(errs)[0]}`, 6000);
      return;
    }
  }
  const ok = await confirmDlg('Save and restart the proxy?',
    'The proxy will be restarted once. Running queries (e.g. Node-RED) are interrupted briefly. ' +
    'If it does not start cleanly with the new config, the old one is restored automatically.',
    'Save & restart');
  if (!ok) return;
  try {
    const r = await api('POST', '/api/devices', { devices: state.devices.map(cleanDev), baseHash: state.hash });
    if (r.unchanged) { toast('No change – nothing to save.'); loadDevices(); return; }
    const st = await runJob(r.jobId, 'Save');
    // on success reload; on error/rollback keep the input so it can be fixed and saved again
    if (st && st.state === 'ok') loadDevices();
    loadConns(true);
  } catch (err) {
    if (err.data && err.data.errors) {
      toast(err.data.errors.map((x) => (x.index >= 0 ? `Device ${x.index + 1}: ` : '') + x.msg).join(' '), 8000);
    } else {
      toast(err.message, 6000);
    }
  }
});

// ---------- job progress ----------

function runJob(jobId, title) {
  return new Promise((resolve) => {
    const dlg = $('#jobDlg');
    const spin = $('#jobSpinner');
    $('#jobTitle').textContent = title;
    $('#jobMsg').textContent = 'Job queued …';
    $('#jobDetail').classList.add('hidden');
    $('#jobClose').disabled = true;
    $('#jobCancel').classList.add('hidden');
    spin.className = 'spinner';
    dlg.showModal();
    const t0 = Date.now();
    let finished = false;

    const finish = (st) => {
      finished = true;
      lastState = st;
      clearInterval(timer);
      spin.className = 'spinner ' + (st.state === 'ok' ? 'done-ok' : st.state === 'rolledback' ? 'done-warn' : 'done-bad');
      $('#jobMsg').textContent = st.message;
      if (st.detail) {
        $('#jobDetail').textContent = st.detail;
        $('#jobDetail').classList.remove('hidden');
      }
      $('#jobCancel').classList.add('hidden');
      $('#jobClose').disabled = false;
      refreshPill();
    };

    $('#jobClose').onclick = () => { dlg.close(); };
    let lastState = null;
    dlg.onclose = () => { finished = true; clearInterval(timer); resolve(lastState); };
    dlg.oncancel = (e) => { if (!finished) e.preventDefault(); };
    $('#jobCancel').onclick = async () => {
      try {
        await api('DELETE', '/api/job?id=' + encodeURIComponent(jobId));
        finish({ state: 'error', message: 'Job withdrawn. Nothing was changed.' });
      } catch (err) { toast(err.message); }
    };

    const poll = async () => {
      if (finished) return;
      try {
        const st = await api('GET', '/api/job?id=' + encodeURIComponent(jobId));
        if (['ok', 'rolledback', 'error'].includes(st.state)) { finish(st); return; }
        if (st.state === 'queued') {
          const waited = (Date.now() - t0) / 1000;
          if (waited > 10) {
            $('#jobMsg').textContent = 'The request is not being picked up. Is modbus-proxy-apply.path running? (page “Service”)';
            $('#jobCancel').classList.remove('hidden');
            $('#jobClose').disabled = false;
          } else {
            $('#jobMsg').textContent = 'Waiting to be executed …';
          }
        } else if (st.state === 'running') {
          $('#jobMsg').textContent = 'Running – checking the proxy …';
          $('#jobCancel').classList.add('hidden');
        } else if (st.state === 'unknown' && Date.now() - t0 > 30000) {
          finish({ state: 'error', message: 'No result received. Please check the “Service” page.' });
        }
      } catch (_) { /* keep trying */ }
    };
    const timer = setInterval(poll, 500);
    poll();
  });
}

// ---------- service ----------

const stateText = {
  active: 'running', inactive: 'stopped', failed: 'failed', activating: 'starting', deactivating: 'stopping',
};

function kv(rows) {
  return rows.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${v}</dd>`).join('');
}

async function loadService() {
  try {
    const s = await api('GET', '/api/service');
    const p = s.proxy;
    $('#svcUnit').textContent = p.unit;
    if (p.error) {
      $('#svcInfo').innerHTML = kv([['Error', esc(p.error)]]);
    } else {
      const cls = p.active === 'active' ? 'yes' : 'no';
      $('#svcInfo').innerHTML = kv([
        ['State', `<span class="${cls}">${esc(stateText[p.active] || p.active)}</span> <span class="muted">(${esc(p.sub)})</span>`],
        ['Running since', p.active === 'active' ? esc(fmtTime(p.since)) : '–'],
        ['Uptime', p.active === 'active' ? esc(fmtDuration(p.uptimeSec)) : '–'],
        ['PID', p.mainPid ? esc(p.mainPid) : '–'],
        ['Memory (RSS)', p.rssBytes ? esc(fmtBytes(p.rssBytes)) : '–'],
        ['Automatic restarts', esc(p.nRestarts)],
        ['Start at boot', p.enabled === 'enabled' ? 'yes' : esc(p.enabled || '–')],
      ]);
    }
    const a = s.applyPath;
    $('#pathInfo').innerHTML = a.error
      ? kv([['Error', esc(a.error)]])
      : kv([
        ['Unit', `<code>${esc(a.unit)}</code>`],
        ['State', a.active === 'active'
          ? '<span class="yes">active</span>'
          : `<span class="no">${esc(a.load === 'not-found' ? 'not installed' : (a.active || '?'))}</span>`],
      ]);
    const j = s.lastJob;
    $('#lastJob').innerHTML = j
      ? kv([
        ['Action', esc({ apply: 'Save config', start: 'Start', stop: 'Stop', restart: 'Restart' }[j.action] || j.action || '–')],
        ['Time', esc(fmtTime(j.finished || j.started))],
        ['Result', `<span class="${j.state === 'ok' ? 'yes' : 'no'}">${esc(j.state)}</span>`],
        ['Message', esc(j.message)],
        ...(j.backup ? [['Backup', `<code>${esc(j.backup)}</code>`]] : []),
      ])
      : kv([['–', 'no job yet']]);
  } catch (err) {
    $('#svcInfo').innerHTML = kv([['Error', esc(err.message)]]);
  }
}

$('#tab-service').addEventListener('click', async (e) => {
  const b = e.target.closest('button[data-svc]');
  if (!b) return;
  const act = b.dataset.svc;
  const texts = {
    start: ['Start the proxy?', 'The proxy will be started.', 'Start', false],
    restart: ['Restart the proxy?', 'Running queries (e.g. Node-RED) are interrupted briefly.', 'Restart', false],
    stop: ['Stop the proxy?', 'After that, NO device is reachable through the proxy until it is started again. Clients get no values.', 'Stop', true],
  }[act];
  if (!(await confirmDlg(...texts))) return;
  try {
    const r = await api('POST', '/api/service', { action: act });
    await runJob(r.jobId, texts[2]);
    loadService();
  } catch (err) {
    toast(err.message, 6000);
  }
});

// ---------- connections ----------

// One entry per client host: hostname (short), IP, number of connections.
// withPorts: also show the source ports (page “Connections”).
function clientList(clients, withPorts) {
  if (!clients || clients.length === 0) return '<span class="muted">none</span>';
  return `<ul class="client-list">${clients.map((c) => {
    const short = c.host ? c.host.split('.')[0] : '';
    const n = c.ports.length;
    return `<li title="${esc((c.host || 'no hostname') + '\n' + c.ports.map((p) => c.ip + ':' + p).join('\n'))}">
      ${short ? `<strong>${esc(short)}</strong> ` : ''}<span class="mono${short ? ' muted' : ''}">${esc(c.ip)}</span>
      ${n > 1 ? `<span class="badge badge-count">${n}×</span>` : ''}
      ${withPorts ? `<div class="muted small mono">port ${c.ports.map(esc).join(', ')}</div>` : ''}
    </li>`;
  }).join('')}</ul>`;
}

async function loadConns(quiet) {
  try {
    const r = await api('GET', '/api/connections');
    state.conns = r;
    if (state.tab === 'devices') { renderDevices(); return; }
    $('#connTime').textContent = 'as of ' + new Date(r.time * 1000).toLocaleTimeString();
    $('#connTable tbody').innerHTML = r.devices.length ? r.devices.map((d) => `<tr>
      <td>${esc(d.name) || '<span class="muted">(unnamed)</span>'}<div class="muted small mono">${esc(d.host)}:${esc(d.port)}</div></td>
      <td class="mono">${esc(d.listenPort)}</td>
      <td>${d.listening ? '<span class="yes">yes</span>' : '<span class="no">no</span>'}</td>
      <td>${clientList(d.clients, true)}</td>
      <td>${d.upstream > 0 ? `<span class="yes">connected</span>${d.upstream > 1 ? ` (${d.upstream})` : ''}` : '<span class="muted">no connection</span>'}</td>
    </tr>`).join('') : '<tr><td colspan="5" class="empty">No devices.</td></tr>';
    $('#listenTable tbody').innerHTML = r.listening.map((l) => `<tr>
      <td class="mono">${esc(l.port)}</td><td class="mono">${esc(l.addr)}</td><td>${esc(l.label)}</td></tr>`).join('');
  } catch (err) {
    if (!quiet) toast(err.message);
  }
}

// ---------- test query ----------

function lsGet(k, def) { try { const v = localStorage.getItem('modbusui.test.' + k); return v == null ? def : v; } catch (_) { return def; } }
function lsSet(k, v) { try { localStorage.setItem('modbusui.test.' + k, v); } catch (_) { /* ignore */ } }

async function prepareTest() {
  if (!state.original.length) {
    try { await loadDevices(); } catch (_) { /* ignore */ }
  }
  const sel = $('#tPort');
  const want = lsGet('port', '');
  sel.innerHTML = state.original.map((d) =>
    `<option value="${esc(d.listenPort)}">${esc(d.name || d.host)}  ·  port ${esc(d.listenPort)}  ·  ${esc(d.host)}</option>`).join('');
  if (want && state.original.some((d) => String(d.listenPort) === want)) sel.value = want;
  $('#tUnit').value = lsGet('unit', '1');
  $('#tFc').value = lsGet('fc', '3');
  $('#tAddr').value = lsGet('addr', '0');
  $('#tCount').value = lsGet('count', '2');
  $('#tTimeout').value = lsGet('timeout', '3000');
  updateAddrHint();
}

function parseAddr(s) {
  s = String(s).trim();
  const n = /^0x[0-9a-f]+$/i.test(s) ? parseInt(s, 16) : /^\d+$/.test(s) ? parseInt(s, 10) : NaN;
  return n;
}

// Shows the start register in the other notation (8264 ↔ 0x2048).
function updateAddrHint() {
  const raw = $('#tAddr').value.trim();
  const h = $('#tAddrHint');
  const n = parseAddr(raw);
  h.classList.remove('bad');
  if (raw === '') { h.textContent = 'decimal or 0x…'; return; }
  if (!Number.isInteger(n) || n < 0 || n > 65535) { h.textContent = 'invalid (0–65535 or 0x0000–0xFFFF)'; h.classList.add('bad'); return; }
  h.textContent = /^0x/i.test(raw) ? `= ${n} decimal` : `= 0x${n.toString(16).toUpperCase().padStart(4, '0')}`;
}
$('#tAddr').addEventListener('input', updateAddrHint);

$('#testForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  const addr = parseAddr($('#tAddr').value);
  if (!Number.isInteger(addr) || addr < 0 || addr > 65535) { toast('Invalid start register.'); return; }
  const q = {
    port: Number($('#tPort').value),
    unit: Number($('#tUnit').value),
    fc: Number($('#tFc').value),
    address: addr,
    count: Number($('#tCount').value),
    timeoutMs: Number($('#tTimeout').value),
  };
  lsSet('port', q.port); lsSet('unit', q.unit); lsSet('fc', q.fc);
  lsSet('addr', $('#tAddr').value.trim()); lsSet('count', q.count); lsSet('timeout', q.timeoutMs);
  const btn = $('#tSubmit');
  btn.disabled = true;
  $('#testResult').innerHTML = '<p class="muted">Querying …</p>';
  try {
    const r = await api('POST', '/api/modbus', q);
    renderTest(r, q);
  } catch (err) {
    $('#testResult').innerHTML = `<div class="box box-err">${esc(err.message)}</div>`;
  } finally {
    btn.disabled = false;
  }
});

function renderTest(r, q) {
  const hexAddr = (a) => '0x' + a.toString(16).toUpperCase().padStart(4, '0');
  let h = `<div class="box ${r.ok ? 'box-ok' : 'box-err'}">
    <strong>${r.ok ? 'Response received' : esc(r.error)}</strong>
    ${r.warning ? `<div class="small">Note: ${esc(r.warning)}</div>` : ''}
  </div>
  <div class="card">
    <div class="result-head">
      <div class="stat"><div class="label">Response time</div><div class="value">${esc(r.responseMs)} ms</div></div>
      <div class="stat"><div class="label">Connect</div><div class="value">${esc(r.connectMs)} ms</div></div>
      <div class="stat"><div class="label">Query</div><div class="value value-text">Port ${esc(q.port)} · ID ${esc(q.unit)} · FC0${esc(q.fc)} · register ${esc(q.address)} (${hexAddr(q.address)}) · ${esc(q.count)}×</div></div>
    </div>
    <dl class="kv hexkv">
      <dt>Sent</dt><dd class="hexline">${esc(r.requestHex)}</dd>
      <dt>Received</dt><dd class="hexline">${esc(r.responseHex || '–')}</dd>
    </dl>
  </div>`;
  if (r.registers && r.registers.length) {
    h += `<h3>16-bit values</h3><div class="table-wrap"><table class="table">
      <thead><tr><th>Register</th><th>Hex</th><th class="num">uint16</th><th class="num">int16</th></tr></thead><tbody>
      ${r.registers.map((x) => `<tr><td class="mono">${x.address} <span class="muted">(${hexAddr(x.address)})</span></td>
        <td class="mono">${x.hex}</td><td class="num">${fmtNum(x.u16)}</td><td class="num">${fmtNum(x.i16)}</td></tr>`).join('')}
      </tbody></table></div>`;
  }
  if (r.pairs && r.pairs.length) {
    h += `<h3>32-bit values <span class="muted small">(register pairs from the start register)</span></h3>
      <div class="table-wrap"><table class="table">
      <thead><tr><th>Registers</th><th class="num">float32 ABCD</th><th class="num">uint32 ABCD</th><th class="num">int32 ABCD</th>
        <th class="num">float32 CDAB</th><th class="num">uint32 CDAB</th><th class="num">int32 CDAB</th></tr></thead><tbody>
      ${r.pairs.map((x) => `<tr><td class="mono">${x.address}–${x.address + 1}</td>
        <td class="num">${esc(x.f32)}</td><td class="num">${fmtNum(x.u32)}</td><td class="num">${fmtNum(x.i32)}</td>
        <td class="num">${esc(x.f32Swap)}</td><td class="num">${fmtNum(x.u32Swap)}</td><td class="num">${fmtNum(x.i32Swap)}</td></tr>`).join('')}
      </tbody></table></div>
      <p class="muted small">ABCD = high register first (big endian). CDAB = words swapped. Which one is correct is in the device's documentation.</p>`;
  }
  $('#testResult').innerHTML = h;
}

// ---------- system ----------

async function loadSystem() {
  const c = $('#sysContent');
  try {
    const s = await api('GET', '/api/system');
    const pct = s.diskTotal ? Math.round((s.diskUsed / s.diskTotal) * 100) : 0;
    const memPct = s.memTotal ? Math.round(((s.memTotal - s.memAvailable) / s.memTotal) * 100) : 0;
    c.innerHTML = `
      <div class="grid3">
        <div class="card stat"><div class="label">Disk /</div>
          <div class="value">${s.diskError ? '–' : pct + ' %'}</div>
          <div class="sub">${s.diskError ? esc(s.diskError) : `${fmtBytes(s.diskUsed)} of ${fmtBytes(s.diskTotal)} used, ${fmtBytes(s.diskAvail)} free`}</div>
          <div class="bar"><span id="diskBar"></span></div></div>
        <div class="card stat"><div class="label">Journal</div>
          <div class="value">${fmtBytes(s.journalBytes)}</div>
          <div class="sub">${s.journalError ? esc(s.journalError) : 'on disk'}</div></div>
        <div class="card stat"><div class="label">/var/log total</div>
          <div class="value">${fmtBytes(s.varLogBytes)}</div>
          <div class="sub">${fmtNum(s.varLogBytes)} bytes${s.varLogPartial ? ' · not everything readable' : ''}</div></div>
        <div class="card stat"><div class="label">Memory</div>
          <div class="value">${memPct} %</div>
          <div class="sub">${fmtBytes(s.memTotal - s.memAvailable)} of ${fmtBytes(s.memTotal)}</div>
          <div class="bar"><span id="memBar"></span></div></div>
        <div class="card stat"><div class="label">System uptime</div>
          <div class="value">${fmtDuration(s.uptimeSec)}</div>
          <div class="sub">${esc(s.hostname)} · load ${esc(s.load)}</div></div>
        <div class="card stat"><div class="label">Web UI</div>
          <div class="value">v${esc(s.version)}</div>
          <div class="sub">${esc(s.goVersion)}</div></div>
      </div>
      <h3>Journal entries in the last 24 h</h3>
      <div class="table-wrap"><table class="table">
        <thead><tr><th>Service</th><th>Unit</th><th class="num">Entries</th></tr></thead><tbody>
        ${s.lines24h.map((u) => `<tr><td>${esc(u.label)}</td><td class="mono">${esc(u.unit)}</td>
          <td class="num">${u.error ? `<span class="no" title="${esc(u.error)}">error</span>` : fmtNum(u.lines)}</td></tr>`).join('')}
        </tbody></table></div>
      <p class="muted small">Also counts the messages systemd itself writes about these units (e.g. “Started …” on a restart).</p>
      <h3>Latest proxy messages</h3>
      <pre class="pre">${esc(s.proxyTail || '(none)')}</pre>
      <h3>Change or reset the password</h3>
      <div class="card small">
        <p>The password is not stored in readable form anywhere, only as a hash in <code>/etc/modbusui/password</code>.
          To change or reset it, run as root on this server:</p>
        <pre class="pre">modbusui -set-password</pre>
        <p>The old password is not needed, the new one is effective immediately. Logged-in browsers stay logged in until they log out;
          to log out everyone at once: <code>systemctl restart modbusui</code> (affects only the UI, not the proxy).</p>
        <p class="muted">Installation, update and troubleshooting: <code>README.md</code> in the source or release archive.</p>
      </div>`;
    const db = $('#diskBar'); if (db) db.style.width = pct + '%';
    const mb = $('#memBar'); if (mb) mb.style.width = memPct + '%';
  } catch (err) {
    c.innerHTML = `<div class="box box-err">${esc(err.message)}</div>`;
  }
}
$('#sysRefresh').addEventListener('click', loadSystem);

// ---------- warn about unsaved changes ----------

window.addEventListener('beforeunload', (e) => {
  if (changes().total > 0) { e.preventDefault(); e.returnValue = ''; }
});

// ---------- start ----------

(async function init() {
  try { state.tab = localStorage.getItem('modbusui.tab') || 'devices'; } catch (_) { /* ignore */ }
  if (!['devices', 'service', 'conns', 'test', 'system'].includes(state.tab)) state.tab = 'devices';
  try {
    const s = await api('GET', '/api/session');
    if (s.loggedIn) showApp();
    else showLogin(s.passwordSet ? '' : 'No password has been set yet. On the server as root: modbusui -set-password');
  } catch (err) {
    showLogin(err.message);
  }
})();
