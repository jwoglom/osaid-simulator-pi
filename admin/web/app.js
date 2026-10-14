'use strict';
const $ = (sel, el = document) => el.querySelector(sel);
const api = {
  async get(p) { const r = await fetch(p); if (!r.ok) throw await err(r); return r.headers.get('content-type')?.includes('json') ? r.json() : r.text(); },
  async send(method, p, body, raw) {
    const r = await fetch(p, { method, headers: raw ? { 'Content-Type': 'text/yaml' } : { 'Content-Type': 'application/json' }, body: raw ? body : JSON.stringify(body ?? {}) });
    if (!r.ok) throw await err(r); return r.json();
  },
};
async function err(r) { let m = r.statusText; try { m = (await r.json()).error || m; } catch { } return new Error(m); }
const fmtAge = s => s < 3600 ? `${Math.floor(s / 60)} min` : s < 86400 ? `${(s / 3600).toFixed(1)} h` : `${(s / 86400).toFixed(1)} d`;
const fmtMB = kb => `${Math.round(kb / 1024)} MB`;
const esc = s => String(s ?? '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

let state = null;
const cards = new Map();   // name -> card element
const logStreams = new Map();

// ---- status polling ----------------------------------------------------------
async function refresh() {
  try {
    state = await api.get('/api/status');
    render();
  } catch (e) {
    $('#sysline').textContent = `offline: ${e.message}`;
  }
}
setInterval(refresh, 3000);

function render() {
  const s = state.system, w = state.wifi;
  $('#sysline').innerHTML = `${esc(s.model)} · up ${fmtAge(s.uptime_s)} · ${fmtMB(s.mem_avail_kb)} free of ${fmtMB(s.mem_total_kb)}` +
    ` · <span class="${s.hci0 ? 'ok' : 'bad'}">hci0 ${s.hci0 ? 'present' : 'missing'}</span>` +
    ` · usb0 ${esc(s.usb0)} · clock ${s.ntp_synced ? 'NTP' : 'set from browser'}` +
    (state.build_running ? ` · <b>building ${esc(state.build_running)}</b>` : '') +
    ` · admin ${esc(state.version)}${s.mock ? ' · <b>MOCK</b>' : ''}`;
  $('#cfgpath').textContent = state.config_path;

  const names = new Set();
  for (const sim of state.simulators) {
    names.add(sim.name);
    let card = cards.get(sim.name);
    if (!card) { card = makeCard(sim); cards.set(sim.name, card); $('#sims').appendChild(card); }
    updateCard(card, sim);
  }
  for (const [name, card] of cards) if (!names.has(name)) { card.remove(); cards.delete(name); }

  // wifi
  const wf = $('#wifi-form');
  if (!wf.dataset.dirty) {
    wf.enabled.checked = w.enabled; wf.ssid.value = w.ssid || '';
  }
  $('#wifi-status').textContent = w.enabled ? `${w.state}${w.ip ? ' · ' + w.ip : ''}` : 'off';
  for (const f of document.querySelectorAll('form.fetch')) f.classList.toggle('hidden', !w.connected);

  $('#sysinfo').innerHTML = [
    ['Board', `${esc(s.model)} (${esc(s.board)})`], ['Hostname', esc(s.hostname)], ['Load', s.load1.toFixed(2)],
    ['Memory', `${fmtMB(s.mem_avail_kb)} available / ${fmtMB(s.mem_total_kb)}`],
    ['Swap', `${fmtMB(s.swap_free_kb)} free / ${fmtMB(s.swap_total_kb)}`],
    ['Clock', `${new Date(s.now).toLocaleString()} ${esc(s.timezone)} ${s.ntp_synced ? '(NTP)' : '(browser)'}`],
    ['Bluetooth', s.hci0 ? '<span class="ok">hci0 present</span>' : '<span class="bad">hci0 missing</span>'],
    ['USB link', esc(s.usb0)],
  ].map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('');
}

// ---- simulator cards ---------------------------------------------------------
function makeCard(sim) {
  const card = $('#sim-tpl').content.firstElementChild.cloneNode(true);
  card.dataset.name = sim.name;
  for (const b of card.querySelectorAll('button[data-act]')) {
    b.onclick = async () => {
      b.disabled = true;
      try { await api.send('POST', `/api/sims/${sim.name}/${b.dataset.act}`); await refresh(); }
      catch (e) { alert(`${b.dataset.act} ${sim.name}: ${e.message}`); }
      b.disabled = false;
    };
  }
  const ff = $('form.flags', card);
  ff.flags.oninput = () => ff.dataset.dirty = '1';
  ff.onsubmit = async ev => {
    ev.preventDefault();
    try { await api.send('PUT', `/api/sims/${sim.name}/flags`, { flags: ff.flags.value, restart: ff.restart.checked }); delete ff.dataset.dirty; await refresh(); }
    catch (e) { alert(e.message); }
  };
  $('details.logs', card).ontoggle = ev => toggleLog(sim.name, card, ev.target.open);
  $('details.builds', card).ontoggle = ev => { if (ev.target.open) loadBuilds(sim.name, card); };

  const drop = $('.drop', card), input = $('input.zip', card);
  input.onchange = () => input.files[0] && uploadZip(sim.name, card, input.files[0]);
  drop.ondragover = ev => { ev.preventDefault(); drop.classList.add('over'); };
  drop.ondragleave = () => drop.classList.remove('over');
  drop.ondrop = ev => { ev.preventDefault(); drop.classList.remove('over'); const f = ev.dataTransfer.files[0]; if (f) uploadZip(sim.name, card, f); };

  const fetchForm = $('form.fetch', card);
  $('.repo', fetchForm).textContent = sim.source?.github || '';
  fetchForm.ref.value = sim.source?.ref || 'main';
  if (!sim.source?.github) fetchForm.remove();
  else fetchForm.onsubmit = async ev => {
    ev.preventDefault();
    const msg = $('.buildmsg', card); msg.textContent = 'downloading…';
    try { const b = await api.send('POST', `/api/sims/${sim.name}/fetch`, { ref: fetchForm.ref.value }); followBuild(sim.name, card, b); }
    catch (e) { msg.textContent = `fetch failed: ${e.message}`; }
  };
  return card;
}

function updateCard(card, sim) {
  $('.title', card).textContent = sim.title;
  const st = sim.status;
  const badge = $('.badge', card);
  badge.textContent = st.active === 'active' ? `running · pid ${st.pid} · ${fmtMB(st.memory_kb)}` : st.active;
  badge.className = `badge ${st.active}`;
  const cur = sim.current_build;
  $('.meta', card).textContent = (cur ? `build ${cur.label} (${cur.id})` : 'no build installed') +
    (sim.exclusive ? ` · exclusive group "${sim.exclusive}"` : '') + (sim.ui_port ? ` · port ${sim.ui_port}` : '');
  const link = $('a.uilink', card);
  if (sim.ui_port) { link.href = `http://${location.hostname}:${sim.ui_port}/`; link.classList.remove('hidden'); }
  else link.classList.add('hidden');
  const ff = $('form.flags', card);
  if (!ff.dataset.dirty) ff.flags.value = sim.flags;
  $('button[data-act=start]', card).disabled = st.active === 'active' || !cur;
  $('button[data-act=stop]', card).disabled = st.active !== 'active';
  $('button[data-act=restart]', card).disabled = st.active !== 'active';
}

function toggleLog(name, card, open) {
  const pre = $('pre.log', card);
  const old = logStreams.get(name);
  if (old) { old.close(); logStreams.delete(name); }
  if (!open) return;
  pre.textContent = '';
  const es = new EventSource(`/api/sims/${name}/logs`);
  es.onmessage = ev => { pre.textContent += ev.data + '\n'; if (pre.textContent.length > 200000) pre.textContent = pre.textContent.slice(-150000); pre.scrollTop = pre.scrollHeight; };
  es.onerror = () => { pre.textContent += '[log stream closed]\n'; };
  logStreams.set(name, es);
}

async function loadBuilds(name, card) {
  const tbody = $('table.buildlist tbody', card);
  try {
    const list = await api.get(`/api/sims/${name}/builds`);
    tbody.innerHTML = list.length ? '' : '<tr><td class="muted">No builds yet. Upload a zip above.</td></tr>';
    for (const b of list) {
      const tr = document.createElement('tr');
      const status = b.running ? 'building…' : b.ok ? `ok in ${Math.round(b.seconds)}s` : `<span class="bad">failed</span>`;
      tr.innerHTML = `<td>${b.current ? '<b>▶ active</b>' : ''}</td><td class="mono">${esc(b.label)}<br><span class="muted">${esc(b.id)} · ${esc(b.kind)} · ${new Date(b.started).toLocaleString()}</span></td>` +
        `<td>${status}${b.error ? `<br><span class="muted small">${esc(b.error)}</span>` : ''}</td>` +
        `<td class="actions">${b.ok && !b.current ? '<button class="secondary" data-do="activate">Use</button> ' : ''}` +
        `<button class="secondary" data-do="log">Log</button> ${!b.current && !b.running ? '<button class="warn" data-do="delete">Delete</button>' : ''}</td>`;
      for (const btn of tr.querySelectorAll('button')) btn.onclick = async () => {
        try {
          if (btn.dataset.do === 'activate') { await api.send('POST', `/api/sims/${name}/builds/${b.id}/activate`); }
          else if (btn.dataset.do === 'delete') { await fetch(`/api/sims/${name}/builds/${b.id}`, { method: 'DELETE' }); }
          else { streamBuildLog(name, card, b.id); return; }
          await loadBuilds(name, card); await refresh();
        } catch (e) { alert(e.message); }
      };
      tbody.appendChild(tr);
    }
  } catch (e) { tbody.innerHTML = `<tr><td class="bad">${esc(e.message)}</td></tr>`; }
}

function uploadZip(name, card, file) {
  const msg = $('.buildmsg', card);
  msg.textContent = `uploading ${file.name} (${Math.round(file.size / 1024)} KB)…`;
  const fd = new FormData(); fd.append('zip', file, file.name);
  const xhr = new XMLHttpRequest();
  xhr.open('POST', `/api/sims/${name}/upload`);
  xhr.upload.onprogress = ev => { if (ev.lengthComputable) msg.textContent = `uploading ${file.name}: ${Math.round(100 * ev.loaded / ev.total)}%`; };
  xhr.onload = () => {
    try {
      const body = JSON.parse(xhr.responseText);
      if (xhr.status >= 300) throw new Error(body.error || xhr.statusText);
      followBuild(name, card, body);
    } catch (e) { msg.textContent = `upload failed: ${e.message}`; }
  };
  xhr.onerror = () => { msg.textContent = 'upload failed (network)'; };
  xhr.send(fd);
  $('input.zip', card).value = '';
}

function followBuild(name, card, b) {
  $('.buildmsg', card).textContent = `build ${b.id} (${b.label}) started`;
  streamBuildLog(name, card, b.id);
}

function streamBuildLog(name, card, id) {
  const pre = $('pre.buildlog', card);
  pre.classList.remove('hidden'); pre.textContent = '';
  if (pre._es) pre._es.close();
  const es = new EventSource(`/api/sims/${name}/builds/${id}/log`);
  pre._es = es;
  es.onmessage = ev => { pre.textContent += ev.data + '\n'; pre.scrollTop = pre.scrollHeight; };
  es.addEventListener('done', ev => { es.close(); $('.buildmsg', card).textContent = `build ${id}: ${ev.data}`; loadBuilds(name, card); refresh(); });
  es.onerror = () => es.close();
}

// ---- wifi / system / config --------------------------------------------------
$('#wifi-form').oninput = ev => ev.currentTarget.dataset.dirty = '1';
$('#wifi-form').onsubmit = async ev => {
  ev.preventDefault();
  const f = ev.currentTarget;
  $('#wifi-status').textContent = 'applying…';
  try {
    const st = await api.send('PUT', '/api/wifi', { enabled: f.enabled.checked, ssid: f.ssid.value, psk: f.psk.value, country: f.country.value.toUpperCase() });
    delete f.dataset.dirty; $('#wifi-status').textContent = st.enabled ? st.state : 'off';
  } catch (e) { $('#wifi-status').textContent = `error: ${e.message}`; }
};
for (const b of document.querySelectorAll('button[data-sys]')) b.onclick = async () => {
  if (!confirm(`${b.dataset.sys} the Pi?`)) return;
  try { await api.send('POST', `/api/system/${b.dataset.sys}`); $('#sysline').textContent = `${b.dataset.sys}…`; } catch (e) { alert(e.message); }
};
for (const [id, url] of [['#jar-form', '/api/jars'], ['#admin-form', '/api/admin/upload']]) $(id).onsubmit = async ev => {
  ev.preventDefault();
  const fd = new FormData(ev.currentTarget);
  const r = await fetch(url, { method: 'POST', body: fd });
  const j = await r.json().catch(() => ({}));
  alert(r.ok ? j.ok : `failed: ${j.error || r.statusText}`);
};

async function loadConfig() { $('#config').value = await api.get('/api/config'); $('#cfg-msg').textContent = ''; }
$('#cfg-reload').onclick = loadConfig;
$('#cfg-save').onclick = async () => {
  $('#cfg-msg').textContent = 'applying…';
  try { await api.send('PUT', '/api/config', $('#config').value, true); $('#cfg-msg').textContent = 'applied'; await refresh(); }
  catch (e) { $('#cfg-msg').textContent = `error: ${e.message}`; }
};
$('#cfg-template').onclick = () => {
  const ta = $('#config');
  ta.value = ta.value.replace(/\s*$/, '\n') + `
  - name: newsim
    title: New simulator
    source: { github: owner/repo, ref: main }
    build: { kind: go, package: ., output: newsim }
    exec: newsim -api :8084 \${FLAGS}
    flags: ""
    ui_port: 8084
    exclusive: radio
`;
  ta.scrollTop = ta.scrollHeight;
};

// ---- clock: the Pi has no RTC and no internet; the browser knows the time ----
async function syncClock() {
  try {
    await api.send('POST', '/api/time', { unix: Math.floor(Date.now() / 1000), tz: Intl.DateTimeFormat().resolvedOptions().timeZone });
  } catch (e) { console.warn('time sync', e); }
}

syncClock().then(refresh).then(loadConfig);
