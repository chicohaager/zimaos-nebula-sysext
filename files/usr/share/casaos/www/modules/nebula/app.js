'use strict';
// Nebula tile. Helpers (api, esc, $, setStatus, describe, initTheme, download, zfw) come from common.js.

const API = '/nebula';
let zfwPresent = false;

function armed(btn) {
  if (btn.dataset.armed) return true;
  btn.dataset.armed = '1';
  const label = btn.textContent;
  btn.textContent = 'Click again';
  setTimeout(() => { delete btn.dataset.armed; btn.textContent = label; }, 4000);
  return false;
}

function day(s) { return s ? s.slice(0, 10) : '—'; }

function statusHTML(d) {
  const t = d.tile;
  const on = d.active === 'active';
  const state = on ? '<span class="pill pill-ok">running</span>' : `<span class="pill pill-off">${esc(d.active || 'stopped')}</span>`;
  const boot = d.boot ? '<span class="pill pill-accent">starts at boot</span>' : '';
  const role = t ? `<span class="pill pill-accent">${t.mode === 'lighthouse' ? 'lighthouse' : 'node'}</span>` : '<span class="pill pill-warn">config not written by this tile</span>';
  const host = d.host || {};
  const nodes = d.can_sign && t && t.mode === 'lighthouse' ? `
    <section class="card">
      <h2 class="section-title">Devices in the network</h2>
      ${d.nodes.length ? `<div class="table-wrap"><table>
        <thead><tr><th>Name</th><th>Address</th><th>Signed</th><th>Certificate valid until</th></tr></thead>
        <tbody>${d.nodes.map((n) => `<tr><td><b>${esc(n.name)}</b></td><td class="mono">${esc(n.ip)}</td><td>${esc(day(n.signed))}</td><td>${esc(day(n.not_after))}</td></tr>`).join('')}</tbody>
      </table></div>` : '<p class="empty">No other device yet.</p>'}
      <form id="sign" style="margin-top:12px" autocomplete="off">
        <h4 class="section-title">Add a device</h4>
        <div class="form-grid">
          <label>Name<input type="text" name="name" placeholder="laptop" maxlength="40" required></label>
          <label>Address (empty: next free)<input type="text" name="ip" placeholder="next free in ${esc(t.cidr)}"></label>
          <label>This host as the device reaches it<input type="text" name="endpoint" placeholder="vpn.example.org:${esc(t.listen_port)}" required></label>
        </div>
        <button class="btn-primary" type="submit" style="margin-top:10px">Sign and make config.yml</button>
      </form>
    </section>` : '';
  return `
  <section class="card">
    <div class="head">
      <h3>${esc(t && t.network ? t.network : 'Nebula')}</h3>${state}${boot}${role}
      <div class="app-actions">
        ${on || d.boot ? '<button class="btn-secondary" data-act="disable">Turn off</button>' : '<button class="btn-primary" data-act="enable">Turn on</button>'}
        ${on ? '<button class="btn-secondary" data-act="restart">Restart</button>' : ''}
        <button class="btn-danger" data-act="reset">Reset</button>
      </div>
    </div>
    ${d.host_error ? `<p class="app-err">${esc(d.host_error)}</p>` : ''}
    <dl class="kv">
      <dt>This host</dt><dd><b>${esc(host.name || '—')}</b> <span class="mono">${esc((host.networks || []).join(', '))}</span></dd>
      ${t ? `<dt>Network</dt><dd class="mono">${esc(t.cidr)}</dd>` : ''}
      ${t && t.mode === 'node' ? `<dt>Lighthouse</dt><dd class="mono">${esc(t.lighthouse)} via ${esc(t.lighthouse_endpoint)}</dd>` : ''}
      ${t && t.listen_port ? `<dt>Listen port</dt><dd class="mono">${esc(t.listen_port)}/udp</dd>` : ''}
      <dt>Certificate valid until</dt><dd>${esc(day(host.not_after))}${d.ca ? ` <span class="muted">(CA ${esc(day(d.ca.not_after))})</span>` : ''}</dd>
      <dt>Interface</dt><dd class="mono">${esc(d.interface || 'not up')}</dd>
    </dl>
    <div class="zfw-line" data-port="${esc(t && t.listen_port ? t.listen_port : '')}"></div>
    <p class="muted" style="font-size:12px;margin-top:8px">Reset switches Nebula off and moves every file (CA key included) to <span class="mono">.reset/</span> — nothing is deleted.</p>
  </section>${nodes}`;
}

async function zfwLine() {
  const el = document.querySelector('.zfw-line');
  if (!zfwPresent || !el) return;
  const port = Number(el.dataset.port);
  if (!port) {
    el.innerHTML = '<span class="pill pill-off">ZFW</span> <span class="muted">No fixed listen port: this host only dials out, ZFW needs no rule.</span>';
    return;
  }
  try {
    el.innerHTML = await zfw.allowsUdp(port)
      ? `<span class="pill pill-ok">ZFW</span> UDP ${port} is open in the ZFW firewall.`
      : `<span class="pill pill-warn">ZFW</span> The ZFW firewall drops UDP ${port}: other devices cannot reach this host. <button class="btn-small btn-primary" data-act="zfw-open" data-port="${port}">Open UDP ${port} in ZFW</button>`;
  } catch (e) {
    el.innerHTML = `<span class="pill pill-off">ZFW</span> <span class="muted">Could not read the ZFW rules: ${esc(describe(e))}</span>`;
  }
}

async function load() {
  const d = await api(`${API}/status`);
  $('#conf-dir').textContent = d.conf_dir;
  if (!d.configured) {
    $('#main').replaceChildren($('#setup').content.cloneNode(true));
    return;
  }
  $('#main').innerHTML = statusHTML(d);
  const saved = safeGet('nebula.endpoint');
  const ep = document.querySelector('#sign input[name=endpoint]');
  if (saved && ep) ep.value = saved;
  await zfwLine();
}

async function refresh() {
  try {
    await load();
    setStatus('');
  } catch (e) {
    $('#main').innerHTML = `<section class="card"><p class="app-err">${esc(describe(e))}</p></section>`;
    setStatus(describe(e), 'err');
  }
}

// ZFW steps after on/off; a ZFW failure is reported, the Nebula action itself stands.
function listenPort() { const el = document.querySelector('.zfw-line'); return el ? Number(el.dataset.port) : 0; }

async function zfwOpen(port) {
  if (!zfwPresent || !port) return null;
  try {
    if (await zfw.allowsUdp(port)) return null;
    await zfw.openUdp(port, `Nebula ${port}/udp`, 'Added by the Nebula tile when Nebula was turned on');
    return { note: `UDP ${port} opened in ZFW` };
  } catch (e) {
    return { warning: `ZFW was not changed (${describe(e)}) — other devices cannot reach this host until UDP ${port} is open` };
  }
}

async function zfwClose(port) {
  if (!zfwPresent || !port) return null;
  try {
    return (await zfw.closeUdp(port)) ? { note: `UDP ${port} closed in ZFW` } : null;
  } catch (e) {
    return { warning: `the ZFW rule for UDP ${port} could not be removed (${describe(e)})` };
  }
}

async function act(label, fn) {
  setStatus(`${label} …`);
  try {
    const r = await fn();
    setStatus(r && r.warning ? `${label}: done, but ${r.warning}` : `${label}: done${r && r.note ? ` — ${r.note}` : ''}.`, r && r.warning ? 'err' : 'ok');
    await load();
    return r;
  } catch (e) {
    setStatus(`${label}: ${describe(e)}`, 'err');
    return null;
  }
}

function showNodeConfig(name, cfg) {
  const node = $('#node-dialog').content.firstElementChild.cloneNode(true);
  node.querySelector('.node-name').textContent = name;
  node.querySelector('pre').textContent = cfg;
  node.addEventListener('click', async (e) => {
    const a = e.target.dataset.act;
    if (a === 'copy') { await navigator.clipboard.writeText(cfg).catch(() => {}); e.target.textContent = 'Copied'; }
    if (a === 'download') download(`${name}-config.yml`, cfg);
    if (a === 'close') node.remove();
  });
  document.body.appendChild(node);
}

document.addEventListener('click', async (e) => {
  const tab = e.target.closest('.tab-btn');
  if (tab) {
    document.querySelectorAll('.tab-btn').forEach((b) => b.classList.toggle('active', b === tab));
    document.querySelectorAll('.tab-panel').forEach((p) => p.classList.toggle('active', p.id === `setup-${tab.dataset.tab}`));
    return;
  }
  const btn = e.target.closest('button[data-act]');
  if (!btn || btn.closest('.modal')) return;
  switch (btn.dataset.act) {
    // On also opens the listen port in ZFW (it drops unknown UDP, measured on 1.8.0); Off and
    // Reset remove exactly that rule again.
    case 'enable': {
      const port = listenPort();
      await act('Turning Nebula on', async () => { await api(`${API}/action`, { method: 'POST', body: { action: 'enable' } }); return zfwOpen(port); });
      break;
    }
    case 'disable': {
      const port = listenPort();
      await act('Turning Nebula off', async () => { await api(`${API}/action`, { method: 'POST', body: { action: 'disable' } }); return zfwClose(port); });
      break;
    }
    case 'restart': await act('Restarting Nebula', () => api(`${API}/action`, { method: 'POST', body: { action: 'restart' } })); break;
    case 'reset':
      if (!armed(btn)) return;
      { const port = listenPort(); await act('Resetting (files kept in .reset/)', async () => { await api(`${API}/reset`, { method: 'POST', body: {} }); return zfwClose(port); }); }
      break;
    case 'zfw-open': {
      const port = Number(btn.dataset.port);
      await act(`Opening UDP ${port} in ZFW`, () => zfw.openUdp(port, `Nebula ${port}/udp`, 'Added by the Nebula tile'));
      break;
    }
    default:
  }
});

document.addEventListener('submit', async (e) => {
  const f = e.target;
  e.preventDefault();
  const v = Object.fromEntries(new FormData(f).entries());
  if (f.id === 'setup-new') {
    await act('Creating the network', () => api(`${API}/setup`, { method: 'POST',
      body: { mode: 'new', network: v.network, cidr: v.cidr, node_name: v.node_name, listen_port: Number(v.listen_port) } }));
  } else if (f.id === 'setup-join') {
    await act('Joining the network', () => api(`${API}/setup`, { method: 'POST',
      body: { mode: 'join', ca_crt: v.ca_crt, host_crt: v.host_crt, host_key: v.host_key, lighthouse: v.lighthouse.trim(),
        lighthouse_endpoint: v.lighthouse_endpoint.trim(), listen_port: Number(v.listen_port || 0) } }));
  } else if (f.id === 'sign') {
    safeSet('nebula.endpoint', v.endpoint);
    const r = await act(`Signing ${v.name}`, () => api(`${API}/nodes`, { method: 'POST', body: { name: v.name, ip: v.ip.trim(), endpoint: v.endpoint.trim() } }));
    if (r && r.config) showNodeConfig(v.name, r.config);
  }
});

initTheme('nebula.theme');
$('#refresh-btn').addEventListener('click', refresh);
(async () => { zfwPresent = await zfw.present(); refresh(); })();
