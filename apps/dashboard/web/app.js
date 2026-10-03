'use strict';
const $ = selector => document.querySelector(selector);
const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
let token = new URLSearchParams(location.hash.slice(1)).get('token') || sessionStorage.getItem('tidalbridge.token') || '';
if (token) sessionStorage.setItem('tidalbridge.token', token);
history.replaceState(null, '', location.pathname);

const view = {expanded: false, status: null, offset: 0, cpu: [], ram: [], cpuGhost: [], ramGhost: [], carrying: null, cores: 0, filter: 'all', jobsKey: '', devicesKey: '', modal: null, modalTimer: 0, modalKey: ''};
let abort, reconnect;

async function api(path, body) {
  const response = await fetch('/v1/' + path, {method: body === undefined ? 'GET' : 'POST', headers: {Authorization: 'Bearer ' + token, 'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
  if (!response.ok) {
    const text = await response.text();
    let message = text;
    try { message = JSON.parse(text).error || text; } catch {}
    throw new Error(message || response.statusText);
  }
  return response.json();
}
function showError(error) { const banner = $('#error'); banner.hidden = false; banner.textContent = error.message || String(error); }

// ---------------------------------------------------------------- wording
const size = mb => mb >= 1024 ? (mb / 1024).toFixed(1) + ' GB' : mb >= 1 || !mb ? Math.round(mb) + ' MB' : Math.max(1, Math.round(mb * 1024)) + ' KB';
// Devices are sold by their physical memory; the system sees less, because
// firmware and chips keep part of it. Workers report the physical size; this
// is the fallback for older ones.
const soldGB = r => r.ram_physical_mb ? Math.round(r.ram_physical_mb / 1024) : [1, 2, 3, 4, 6, 8, 10, 12, 16, 18, 24, 32, 64].find(gb => gb * 1024 >= (r.ram_total_mb || 0)) || Math.ceil((r.ram_total_mb || 0) / 1024);
const now = () => Date.now() + view.offset;
const clock = time => new Date(time).toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'});
function when(time) {
  const date = new Date(time);
  return date.toDateString() === new Date().toDateString() ? clock(time) : date.toLocaleDateString([], {month: 'short', day: 'numeric'}) + ', ' + clock(time);
}
function span(ms) {
  if (!(ms >= 0)) return '—';
  const s = ms / 1000;
  if (s < 10) return s.toFixed(1) + ' s';
  if (s < 60) return Math.round(s) + ' s';
  const m = Math.floor(s / 60), rest = Math.round(s % 60);
  if (m < 60) return m + ' min' + (rest ? ' ' + rest + ' s' : '');
  return Math.floor(m / 60) + ' h ' + (m % 60) + ' min';
}
// Processor work in round units.
const work = s => s < 10 ? s.toFixed(1) + ' s' : s < 60 ? Math.round(s) + ' s' : s < 3600 ? Math.round(s / 60) + ' min' : Math.floor(s / 3600) + ' h ' + Math.round(s % 3600 / 60) + ' min';
const holding = u => size(u.ram_mb) + ' · ' + u.cores.toFixed(1) + ' cores';
// What a phone run spared this laptop; approximate (≈) when the command never
// ran here, so the phone's own measurement stands in.
const saved = r => (r.measured ? '' : '≈ ') + work(r.cpu_seconds) + ' CPU' + (r.ram_mb ? ' · ' + size(r.ram_mb) : '');
const MODE_NAMES = {AUTO: 'Auto', CONSERVATIVE: 'Careful', PERFORMANCE: 'Max', BATTERY_SAVER: 'On charge'};
const ORIGINS = {detected: 'Detected automatically', automatic: "The project's task file", prepare: 'Getting the phone ready', '': 'Started by hand (CLI or MCP)'};
const NAMES = {
  typecheck: ['Type-check', 'check', '✓'], tsc: ['TypeScript check', 'check', '✓'], mypy: ['Type-check', 'check', '✓'],
  lint: ['Lint', 'check', '✓'], eslint: ['Lint', 'check', '✓'], ruff: ['Lint', 'check', '✓'], prettier: ['Format check', 'check', '✓'],
  format: ['Format', 'fix', '✎'], 'ruff-format': ['Format', 'fix', '✎'],
  test: ['Tests', 'test', '▶'], 'npm-test': ['Tests', 'test', '▶'], 'test-run': ['Tests', 'test', '▶'], vitest: ['Tests', 'test', '▶'], jest: ['Tests', 'test', '▶'],
  pytest: ['Python tests', 'test', '▶'], unittest: ['Python tests', 'test', '▶'], 'django-test': ['Django tests', 'test', '▶'],
  dev: ['Dev server', 'service', '◉'], serve: ['Dev server', 'service', '◉'],
};
const GENERIC = new Set(['frontend', 'backend', 'web', 'app', 'apps', 'server', 'client', 'api', 'src', 'site', 'ui', 'packages']);
function project(path) {
  const parts = String(path || '').split(/[\\/]+/).filter(Boolean);
  if (!parts.length) return '';
  const last = parts.at(-1);
  return GENERIC.has(last.toLowerCase()) && parts.length > 1 ? parts.at(-2) + ' · ' + last : last;
}
function describe(job) {
  const spec = job.spec || {}, argv = spec.argv || [], profile = spec.profile || '';
  const cut = profile.indexOf(':'), origin = cut > 0 ? profile.slice(0, cut) : '', name = cut > 0 ? profile.slice(cut + 1) : '';
  const where = project(spec.workspace), command = argv.join(' ');
  if (origin === 'prepare') return {title: 'Getting the phone ready', kind: 'prepare', icon: '⇣', project: where, command: 'Installs the project’s locked dependencies', origin};
  if (profile === 'android-browser') return {title: 'Browser capture', kind: 'command', icon: '◎', project: where, command: argv.slice(1).join(' '), origin};
  const script = argv[0] === 'npm' ? (argv[1] === 'test' ? 'test' : argv[1] === 'run' ? argv[2] : '') : '';
  let [title, kind, icon] = NAMES[name] || NAMES[script] || [command || 'Command', 'command', '›'];
  if (spec.service) [kind, icon] = ['service', '◉'];
  if (argv.some(a => a === '--fix' || a === '--write' || (a === '-w' && argv[0] === 'prettier'))) [title, kind, icon] = [kind === 'check' && title !== 'Format check' ? title + ' and fix' : 'Format', 'fix', '✎'];
  const part = argv.map(a => /^--shard=(\d+)\/(\d+)$/.exec(a)).find(Boolean);
  if (part) title += ' · part ' + part[1] + ' of ' + part[2];
  return {title, kind, icon, project: where, command, origin};
}
function placement(job) {
  const target = job.attempts?.at(-1)?.target || job.decision?.target || '';
  if (!target || target === 'WAIT') return {label: 'Waiting', cls: ''};
  if (target === 'REJECT') return {label: 'Not run', cls: ''};
  if (target === 'LOCAL') return {label: job.decision?.forced_local ? 'Laptop · forced' : 'Laptop', cls: 'laptop'};
  return {label: 'Phone', cls: 'phone'};
}
const STATES = {COMPLETED: ['Done', 'ok'], FAILED: ['Failed', 'bad'], RUNNING: ['Running', 'run'], QUEUED: ['Queued', 'wait'], WAIT: ['Waiting', 'wait'], CANCELLED: ['Cancelled', 'off'], INTERRUPTED: ['Interrupted', 'off'], REJECTED: ['Not run', 'off']};
function stateOf(job) {
  const [label, cls] = STATES[job.state] || [job.state, 'off'];
  return job.evacuated ? {label: 'Moved: phone too hot', cls: 'wait'} : {label, cls};
}
const running = job => !job.finished && ['RUNNING', 'QUEUED', 'WAIT'].includes(job.state);
const startOf = job => Date.parse(job.attempts?.at(-1)?.started || job.created);
function elapsed(job) {
  if (running(job)) return now() - startOf(job);
  const attempt = job.attempts?.at(-1);
  return attempt?.duration_ms || (job.finished ? Date.parse(job.finished) - Date.parse(job.created) : NaN);
}
function heat(worker) {
  const t = worker.profile?.capabilities?.resources?.thermal;
  const hot = ['severe', 'critical', 'emergency', 'shutdown'].includes(t);
  if (!hot && worker.cooling_until && Date.parse(worker.cooling_until) > now()) return ['Cooling down', 'warn', 'Takes work again at ' + clock(worker.cooling_until)];
  return {nominal: ['Cool', 'ok', 'Full speed'], warm: ['Warm', 'ok', 'Full speed'], moderate: ['Warm', 'warn', 'One job at a time'], severe: ['Hot', 'bad', 'Takes no new work'],
    critical: ['Too hot', 'bad', 'Running jobs moved to the laptop'], emergency: ['Too hot', 'bad', 'Running jobs moved to the laptop'], shutdown: ['Too hot', 'bad', 'Running jobs moved to the laptop']}[t]
    || ['No reading yet', 'warn', 'Waiting for the phone'];
}
function battery(r) {
  if (r.battery_percent == null) return ['—', 'Battery unknown'];
  return [r.battery_percent + '%', r.charging ? 'Charging' : 'On battery'];
}
const realWorkers = status => (status.workers || []).filter(w => !w.profile?.capabilities?.simulated);
function story(status) {
  const workers = realWorkers(status), worker = workers.find(w => ['READY', 'BUSY'].includes(w.state)) || workers[0];
  if (status.paused) return {head: 'Paused: everything runs on this laptop', line: 'Resume to let the phone take work again.', worker};
  if (!worker) return {head: 'No phone connected', line: 'Everything runs on this laptop. Connect your phone by USB to share the work.'};
  if (!['READY', 'BUSY'].includes(worker.state)) return {head: 'The phone is not ready', line: 'It is ' + worker.state.toLowerCase().replace(/_/g, ' ') + '. Commands run on this laptop until it recovers.', worker};
  if (worker.draining) return {head: 'The phone takes no new work', line: 'You stopped new work for it; whatever it runs now finishes.', worker, on: true};
  const [label, , note] = heat(worker);
  if (['Cooling down', 'Hot', 'Too hot'].includes(label)) return {head: 'The phone is cooling down', line: note + '. Commands run on this laptop meanwhile.', worker, on: true};
  if (worker.active_jobs) return {head: 'The phone is running ' + worker.active_jobs + (worker.active_jobs > 1 ? ' commands' : ' command'), line: 'This laptop keeps its CPU and memory for you.', worker, on: true, busy: true};
  return {head: 'The phone is ready to help', line: 'Checks, tests, formatters and dev servers move to it whenever that relieves this laptop.', worker, on: true};
}

// ---------------------------------------------------------------- live data
function renderPulse(pulse) {
  view.offset = Date.parse(pulse.now) - Date.now();
  const host = pulse.host, r = host.resources, carry = view.carrying = pulse.carrying || null;
  const used = 100 * (1 - r.ram_available_mb / Math.max(1, r.ram_total_mb));
  view.cores = r.logical_cores;
  remember(view.cpu, r.cpu_percent);
  remember(view.ram, used);
  // Dashed lines: about where this laptop would be with the phone's work.
  remember(view.cpuGhost, Math.min(100, r.cpu_percent + (carry ? carry.laptop_percent : 0)));
  remember(view.ramGhost, Math.min(100, used + (carry ? 100 * carry.ram_mb / Math.max(1, r.ram_total_mb) : 0)));
  $('#cpu').textContent = Math.round(r.cpu_percent) + '%';
  $('#cpu-note').textContent = carry?.laptop_percent >= 1 ? '≈ ' + Math.round(carry.laptop_percent) + '% more without the phone' : 'last minute';
  $('#ram').textContent = size(r.ram_total_mb - r.ram_available_mb);
  const total = r.ram_physical_mb ? soldGB(r) + ' GB' : size(r.ram_total_mb);
  $('#ram-note').textContent = size(r.ram_available_mb) + ' free of ' + total + (carry ? ' · the phone holds ' + size(carry.ram_mb) : r.ram_physical_mb ? ' (' + size(r.ram_total_mb) + ' usable)' : '');
  $('#laptop-line').textContent = 'CPU ' + Math.round(r.cpu_percent) + '% · ' + size(r.ram_available_mb) + ' free';
  $('#overhead').textContent = 'Tidal Bridge itself: ' + host.daemon_rss_mb.toFixed(0) + ' MB, ' + host.daemon_cpu_percent.toFixed(1) + '% CPU';
  sparkline($('#cpu-spark'), view.cpu);
  sparkline($('#ram-spark'), view.ram);
  ghost($('#cpu-ghost'), view.cpuGhost, view.cpu);
  ghost($('#ram-ghost'), view.ramGhost, view.ram);
  for (const element of document.querySelectorAll('[data-holding]')) {
    const u = carry?.jobs?.[element.dataset.holding];
    if (u) element.textContent = holding(u);
  }
  for (const element of document.querySelectorAll('[data-since]')) element.textContent = span(now() - Number(element.dataset.since));
  $('#updated').textContent = 'Live · ' + new Date().toLocaleTimeString();
}
function ghost(path, values, actual) {
  const on = values.some((v, i) => v - actual[i] > 0.5);
  path.classList.toggle('on', on);
  if (on) sparkline(path, values);
}
function remember(list, value) { list.push(value); if (list.length > 60) list.shift(); }
function sparkline(path, values) {
  if (values.length < 2) return;
  const step = 120 / 59, offset = 120 - (values.length - 1) * step;
  path.setAttribute('d', values.map((v, i) => (i ? 'L' : 'M') + (offset + i * step).toFixed(1) + ' ' + (28 - Math.min(100, Math.max(0, v)) * 0.26).toFixed(1)).join(''));
}

function renderStatus(status) {
  view.status = status;
  const settings = JSON.stringify(status.automation || {});
  if (settings !== view.automationKey) { view.automationKey = settings; loadAutomation(); }
  const s = story(status), worker = s.worker, caps = worker?.profile?.capabilities || {}, r = caps.resources || {}, calibration = worker?.profile?.calibration || {};
  $('#headline').textContent = s.head;
  $('#subline').textContent = s.line;
  const bridge = $('#bridge');
  bridge.classList.toggle('phone-off', !s.on);
  bridge.classList.toggle('busy', !!s.busy);
  bridge.style.setProperty('--span', Math.max(80, bridge.querySelector('.link').clientWidth - 8) + 'px');
  $('#phone-name').textContent = worker ? caps.model || worker.serial : 'Phone';
  $('#phone-line').textContent = worker ? battery(r)[0] + ' · ' + heat(worker)[0] : 'not connected';
  $('#link-line').textContent = worker ? (worker.transport === 'usb_adb' ? 'USB cable' : worker.transport) + (calibration.rtt_ms ? ' · ' + Math.round(calibration.rtt_ms) + ' ms' : '') : 'not connected';
  const remote = Math.max(0, status.active_jobs - status.local_active);
  $('#hero-stats').innerHTML = `<span>On the phone now <b>${remote}</b></span><span>On this laptop <b>${status.local_active}</b></span><span>Waiting <b>${status.queue_depth}</b></span><span>Mode <b>${esc(MODE_NAMES[status.mode] || status.mode)}</b></span>`;

  const [heatLabel, , heatNote] = worker ? heat(worker) : ['—', '', ''];
  $('#phone-heat').textContent = worker ? heatLabel : 'Not connected';
  $('#phone-tag').textContent = worker ? battery(r)[0] + (r.charging ? ' ⚡' : '') : '—';
  $('#phone-note').textContent = worker ? heatNote + ' · ' + size(r.ram_available_mb || 0) + ' free for jobs' : 'Connect it by USB';
  const bar = $('#battery-bar');
  bar.style.width = (r.battery_percent ?? 0) + '%';
  bar.classList.toggle('low', r.battery_percent != null && r.battery_percent < 20 && !r.charging);

  const relief = status.relief || {}, today = new Date().toDateString();
  const finishedToday = (status.jobs || []).filter(j => j.finished && new Date(j.created).toDateString() === today && !j.spec?.profile?.startsWith('prepare:'));
  const share = finishedToday.length ? finishedToday.filter(j => placement(j).cls === 'phone').length / finishedToday.length : 0;
  $('#relief').innerHTML = `${esc(work(relief.cpu_seconds_today || 0))}<small> of CPU work</small>`;
  $('#relief-note').textContent = (relief.phone_jobs_today || 0) + (relief.phone_jobs_today === 1 ? ' command' : ' commands') + ' on the phone' + (relief.peak_ram_mb_today ? ' · held up to ' + size(relief.peak_ram_mb_today) : '')
    + (relief.forced_today ? ' · ' + relief.forced_today + ' forced onto the laptop' : '');
  $('#relief-bar').style.width = Math.round(share * 100) + '%';

  for (const button of document.querySelectorAll('[data-mode]')) button.setAttribute('aria-checked', String(button.dataset.mode === status.mode));
  const capacity = status.capacity || {};
  for (const button of document.querySelectorAll('[data-capacity]')) button.setAttribute('aria-checked', String(Number(button.dataset.capacity) === capacity.max));
  $('#adaptive').checked = !capacity.fixed;
  const pause = $('#pause');
  pause.textContent = status.paused ? 'Resume' : 'Pause';
  pause.classList.toggle('paused', !!status.paused);
  renderJobs();
  renderDevices();
  if (view.modal) {
    const job = status.jobs?.find(j => j.id === view.modal);
    const key = job ? job.state + (job.finished || '') : '';
    if (key !== view.modalKey) { view.modalKey = key; refreshModal(); }
  }
}

const FILTERS = {all: () => true, running, phone: j => placement(j).cls === 'phone', laptop: j => placement(j).cls === 'laptop', problems: j => ['FAILED', 'INTERRUPTED', 'REJECTED'].includes(j.state) || j.evacuated};
function renderJobs(force) {
  const jobs = (view.status?.jobs || []).filter(FILTERS[view.filter]);
  const key = view.filter + view.expanded + '|' + jobs.map(j => j.id + j.state + (j.finished || '') + (j.attempts?.length || 0)).join(',');
  if (!force && key === view.jobsKey) return;
  view.jobsKey = key;
  const more = jobs.length > 12 ? `<li><button type="button" class="more" data-more>${view.expanded ? 'Show fewer' : 'Show all ' + jobs.length}</button></li>` : '';
  $('#jobs').innerHTML = jobs.length ? (view.expanded ? jobs : jobs.slice(0, 12)).map(row).join('') + more : view.filter === 'all'
    ? '<li class="empty"><strong>Nothing yet</strong>Run a check, tests or a dev server in any project; it appears here.</li>'
    : '<li class="empty"><strong>Nothing here</strong>No commands match this filter.</li>';
}
function row(job) {
  const d = describe(job), where = placement(job), state = stateOf(job), live = running(job), u = live && view.carrying?.jobs?.[job.id];
  const pill = where.cls !== 'phone' ? '' : live ? `<span class="saved" data-holding="${esc(job.id)}">${u ? esc(holding(u)) : ''}</span>`
    : job.spared ? `<span class="saved" data-tip="${job.spared.measured ? "This command's usual cost on this laptop; the phone did it instead." : "The phone's own measurement; this command has not run on this laptop since measuring began."}">${esc(saved(job.spared))}</span>` : '';
  return `<li><button type="button" class="job" data-job="${esc(job.id)}"><span class="kind ${d.kind}" aria-hidden="true">${d.icon}</span>`
    + `<span class="job-main"><span class="title">${esc(d.title)}</span><span class="sub">${d.project ? esc(d.project) + ' · ' : ''}<code>${esc(d.command)}</code></span>${pill}</span>`
    + `<span class="where ${where.cls}">${esc(where.label)}</span><span class="state ${state.cls}">${esc(state.label)}</span>`
    + `<span class="dur${live ? ' live' : ''}"${live ? ` data-since="${startOf(job)}"` : ''}>${span(elapsed(job))}</span></button></li>`;
}

function renderDevices() {
  const workers = view.status?.workers || [];
  const key = JSON.stringify(workers.map(w => [w.id, w.state, w.draining, w.active_jobs, w.capacity, w.effective_capacity, w.capacity_reason, w.cooling_until, w.error, w.profile?.capabilities?.resources]));
  if (key === view.devicesKey) return;
  view.devicesKey = key;
  $('#devices').innerHTML = workers.length ? workers.map(device).join('')
    : '<div class="empty"><strong>No phone connected</strong>Connect an Android phone by USB with USB debugging on, then run <code>scripts\\install.ps1</code>.</div>';
}
function stat(label, value, note, cls, tip) {
  return `<div class="stat"><span>${label}${tip ? `<i class="info" tabindex="0" data-tip="${esc(tip)}">?</i>` : ''}</span><b class="${cls || ''}">${esc(value)}</b>${note ? `<small>${esc(note)}</small>` : ''}</div>`;
}
function device(worker) {
  const caps = worker.profile?.capabilities || {}, r = caps.resources || {}, calibration = worker.profile?.calibration || {};
  const [heatLabel, heatClass, heatNote] = heat(worker), [charge, power] = battery(r), ready = ['READY', 'BUSY'].includes(worker.state);
  const tools = [['node', 'Node'], ['python', 'Python']].map(([key, name]) => caps.runtimes?.['debian-' + key] && name + ' ' + caps.runtimes['debian-' + key].split('.').slice(0, 2).join('.')).filter(Boolean).join(' · ');
  const label = !ready ? worker.state.toLowerCase().replace(/_/g, ' ') : worker.draining ? 'Takes no new work' : worker.active_jobs ? 'Working' : 'Ready';
  return `<article class="device"><div class="device-head"><div><h3>${esc(caps.model || worker.serial)}</h3><p>Android ${esc(caps.android_version || '?')} · ${r.ram_total_mb ? soldGB(r) + ' GB RAM · ' : ''}${esc(worker.transport === 'usb_adb' ? 'USB cable' : worker.transport)}${caps.simulated ? ' · simulated, runs on this laptop' : ''}</p></div>`
    + `<span class="state ${!ready ? 'bad' : worker.draining ? 'wait' : worker.active_jobs ? 'run' : 'ok'}">${esc(label)}</span></div><div class="stats">`
    + stat('Jobs', worker.active_jobs + ' running', 'up to ' + (worker.effective_capacity || worker.capacity) + ' now' + (worker.capacity_reason ? ': ' + worker.capacity_reason : ''), '', 'The phone has no fan and shares its memory with your apps. With Adapt on, it takes fewer jobs while you use it, while it is warm or on battery, and when memory runs short.')
    + stat('Heat', heatLabel, heatNote, heatClass, "Android's own heat level. Tidal Bridge goes one job at a time when warm, takes no new work when hot, and moves running jobs to the laptop when too hot.")
    + stat('Battery', charge, power, r.battery_percent != null && r.battery_percent < 20 && !r.charging ? 'bad' : '', 'Below 15% without a charger the phone takes no work.')
    + stat('Screen', r.in_use ? 'In use' : 'Idle', r.in_use ? 'One job at a time' : 'Full speed', '', 'While you use the phone it takes one job at a time, so it stays responsive.')
    + stat('Free for jobs', size(r.ram_available_mb || 0), 'of ' + soldGB(r) + ' GB (' + size(r.ram_total_mb || 0) + ' usable)', '', "Memory Android can give to new work without closing your apps. The phone's settings show more, because they also count apps Android would close. Of the phone's physical memory, the modem and graphics chips keep about a gigabyte.")
    + stat('Free storage', size(r.storage_available_mb || 0), r.project_copies ? r.project_copies + (r.project_copies === 1 ? ' project copy' : ' project copies') + ' kept' : 'for project copies', '', 'The phone keeps a copy of each project it works on. Copies unused for two weeks are removed, sooner when its storage runs low; a removed copy is rebuilt when needed.')
    + stat('Engine', caps.features?.engine_native ? 'Native Node' : 'Debian', tools, '', 'Node tools run directly on the phone; installs and other programs run in its Debian system.')
    + stat('Response time', calibration.rtt_ms ? Math.round(calibration.rtt_ms) + ' ms' : '—', 'per request over the cable', '', 'How long one round trip between this laptop and the phone takes.')
    + `</div><div class="device-foot"><span>${worker.error ? esc(worker.error) : 'Worker ' + esc(worker.id.slice(0, 16))}</span>`
    + `<button type="button" class="button" data-drain="${esc(worker.id)}" data-value="${!worker.draining}" data-tip="${worker.draining ? 'Let the phone take new work again.' : 'The phone finishes what it is running but takes nothing new. Use it before unplugging, or to keep the phone free for yourself.'}">${worker.draining ? 'Take work again' : 'Stop new work'}</button></div></article>`;
}

// ---------------------------------------------------------------- details
async function openJob(id) {
  view.modal = id;
  view.modalKey = '';
  $('#sheet').innerHTML = '<p class="muted">Loading…</p>';
  $('#modal').hidden = false;
  document.documentElement.classList.add('locked');
  $('#modal-close').focus();
  await refreshModal();
}
function closeModal() {
  view.modal = null;
  clearTimeout(view.modalTimer);
  $('#modal').hidden = true;
  document.documentElement.classList.remove('locked');
}
async function refreshModal() {
  clearTimeout(view.modalTimer);
  const id = view.modal;
  if (!id) return;
  try {
    const job = await api('jobs/' + encodeURIComponent(id));
    if (view.modal !== id) return;
    const terminal = $('#sheet .terminal'), stuck = !terminal || terminal.scrollHeight - terminal.scrollTop - terminal.clientHeight < 30;
    const opened = [...document.querySelectorAll('#sheet details')].map(d => d.open);
    $('#sheet').innerHTML = sheet(job);
    document.querySelectorAll('#sheet details').forEach((d, i) => { d.open = !!opened[i]; });
    const next = $('#sheet .terminal');
    if (next && stuck) next.scrollTop = next.scrollHeight;
    if (running(job)) view.modalTimer = setTimeout(refreshModal, 2000);
  } catch (error) {
    $('#sheet').innerHTML = `<p class="banner">${esc(error.message)}</p>`;
  }
}
function explain(job) {
  if (job.evacuated) return 'The phone got too hot while running this, so Tidal Bridge stopped it there and ran it on this laptop.';
  const where = placement(job), text = (job.decision?.explanation || '').replace(/^worker-[0-9a-f]+:\s*/, '');
  const lead = {phone: 'Ran on the phone', laptop: 'Ran on this laptop'}[where.cls] || 'Not run yet';
  const why = text ? ': ' + text.charAt(0).toLowerCase() + text.slice(1) : '.';
  return lead + why + (job.error ? ' ' + job.error : '');
}
function kv(label, value) { return value === undefined || value === null || value === '' ? '' : `<div><span>${label}</span><b>${esc(value)}</b></div>`; }
const clean = text => String(text || '').replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '').slice(-200000);
// What a job cost where it ran and, for a phone run, what that spared this laptop.
function resources(job, a, m, where, live) {
  if (where !== 'phone') {
    const cpu = a.host_cpu_seconds ?? m.cpu_seconds, ram = a.host_peak_ram_mb ?? m.peak_ram_mb;
    const usage = kv('CPU time', cpu != null ? work(cpu) : '') + kv('Peak memory', ram ? size(ram) : '');
    return usage ? `<div class="section-label">Resources on this laptop</div><div class="kv">${usage}</div>` : '';
  }
  const r = job.spared, u = view.carrying?.jobs?.[job.id];
  let say = '';
  if (live) say = `Right now the phone holds <b data-holding="${esc(job.id)}">${u ? esc(holding(u)) : '…'}</b> of this work; this laptop keeps that free.`;
  else if (r?.measured) say = `This laptop was spared about <b>${esc(work(r.cpu_seconds))}</b> of CPU time and <b>${esc(size(r.ram_mb || 0))}</b> of memory, what this command takes when it runs here`
    + (r.laptop_seconds && view.cores ? `: about ${Math.round(Math.min(100, 100 * r.cpu_seconds / r.laptop_seconds / view.cores))}% of its processor for ${esc(span(r.laptop_seconds * 1000))}.` : '.');
  else if (r) say = `The phone did <b>${esc(work(r.cpu_seconds))}</b> of CPU work and held up to <b>${esc(size(r.ram_mb || 0))}</b> instead of this laptop. That is the phone's own measurement; this command has not run on this laptop since measuring began.`;
  const usage = kv('Phone CPU time', a.worker_cpu_seconds != null ? work(a.worker_cpu_seconds) : '') + kv('Phone peak memory', a.worker_peak_ram_mb ? size(a.worker_peak_ram_mb) : '')
    + kv('Moving it cost this laptop', m.cpu_seconds != null ? work(m.cpu_seconds) + ' CPU · ' + size(m.peak_ram_mb || 0) : '')
    + (r?.measured ? kv('Here it takes', r.laptop_seconds ? span(r.laptop_seconds * 1000) : '') + kv('Laptop CPU time', work(r.cpu_seconds)) + kv('Laptop peak memory', r.ram_mb ? size(r.ram_mb) : '') : '')
    + kv('Sent to the phone', a.bytes_sent ? size(a.bytes_sent / 1048576) : '') + kv('Sync', a.sync_ms ? span(a.sync_ms) : '');
  return say || usage ? `<div class="section-label">What the phone spared this laptop</div>${say ? `<p class="say">${say}</p>` : ''}${usage ? `<div class="kv">${usage}</div>` : ''}` : '';
}
function sheet(job) {
  const d = describe(job), where = placement(job), state = stateOf(job), live = running(job), spec = job.spec || {}, a = job.attempts?.at(-1) || {}, m = job.adapter_metrics || {};
  const environment = where.cls === 'phone' ? (spec.runtime === 'debian' ? (spec.engine === 'native' ? 'Debian, native Node' : 'Debian') : 'Phone default') : where.cls === 'laptop' ? 'Windows' : '';
  const out = clean(job.stdout), err = clean(job.stderr);
  const candidates = job.decision?.candidates || [];
  let html = `<div class="sheet-head"><span class="kind ${d.kind}" aria-hidden="true">${d.icon}</span><div><h2 id="sheet-title">${esc(d.title)}</h2><div class="badges">`
    + `<span class="where ${where.cls}">${esc(where.label)}</span><span class="state ${state.cls}">${esc(state.label)}</span>${job.exit_code != null ? `<span class="where">exit code ${job.exit_code}</span>` : ''}</div></div></div>`
    + `<div class="command"><code>${esc(d.command)}</code><button type="button" data-copy="${esc(d.command)}">Copy</button></div>`
    + `<p class="say">${esc(explain(job))}</p>`
    + `<div class="kv">${kv('Project', d.project || spec.workspace)}${kv('Folder', spec.working_directory || '(project root)')}${kv('Started', when(a.started || job.created))}`
    + `${live ? `<div><span>Running for</span><b data-since="${startOf(job)}">${span(elapsed(job))}</b></div>` : kv('Took', span(elapsed(job)))}${kv('Chosen by', ORIGINS[d.origin])}${kv('Environment', environment)}</div>`;
  html += resources(job, a, m, where.cls, live);
  if (job.write_back) {
    const wb = job.write_back;
    html += `<div class="section-label">Files changed</div><p class="say">${wb.conflict ? 'Not copied back: ' + esc(wb.conflict) + '. The command ran on this laptop instead.' : 'Copied back from the phone: ' + esc([...(wb.applied || []), ...(wb.deleted || []).map(p => p + ' (deleted)')].join(', ') || 'nothing changed')}</p>`;
  }
  html += `<div class="section-label">Output</div><pre class="terminal">${out || err ? esc(out) + (err ? `<span class="err">${esc(err)}</span>` : '') : 'No output' + (live ? ' yet.' : '.')}</pre>`;
  if (candidates.length) {
    html += `<div class="section-label">Why here</div><ul class="reasons">` + candidates.map(c => `<li class="${c.eligible ? 'ok' : 'no'}"><b>${c.target.startsWith('worker-') ? 'Phone' : esc(c.target)}: ${c.eligible ? 'could run it' : 'not possible'}</b>${esc((c.reasons || []).join(' '))}</li>`).join('') + '</ul>';
  }
  const record = {...job};
  delete record.stdout;
  delete record.stderr;
  html += `<details class="raw"><summary>Technical record (JSON)</summary><pre>${esc(JSON.stringify(record, null, 2))}</pre></details>`;
  html += `<div class="actions">${live ? `<button type="button" class="button danger" data-cancel="${esc(job.id)}">Cancel</button>` : ''}<button type="button" class="button" data-close>Close</button></div>`;
  return html;
}

// ---------------------------------------------------------------- automation and inspector
async function loadAutomation() { try { renderAutomation(await api('automation')); } catch (error) { showError(error); } }
function renderAutomation(a) {
  $('#universal').checked = !!a.universal;
  const chips = (list, name) => (list || []).length ? list.map(path => `<span class="chip" title="${esc(path)}"><span>${esc(path)}</span><button type="button" aria-label="Remove ${esc(path)}" data-remove="${name}" data-path="${esc(path)}">×</button></span>`).join('') : '<span class="none">None</span>';
  $('#share-list').innerHTML = chips(a.share_env, 'share_env');
  $('#excluded-list').innerHTML = chips(a.excluded, 'excluded');
  $('#task-projects').innerHTML = (a.projects || []).length ? a.projects.map(p => `<div class="task-project"><div>${esc(project(p.workspace))}<small>${esc((p.tasks || []).map(t => t.name).join(' · '))}</small></div><button type="button" class="button" data-automation="${esc(p.workspace)}" data-enabled="${!p.enabled}">${p.enabled ? 'Turn off' : 'Turn on'}</button></div>`).join('') : '<span class="none">None</span>';
}
function words(line) { return [...line.matchAll(/"([^"]*)"|'([^']*)'|(\S+)/g)].map(m => m[1] ?? m[2] ?? m[3]); }
$('#route-form').addEventListener('submit', async event => {
  event.preventDefault();
  try {
    const argv = words($('#route-command').value);
    if (!argv.length) throw new Error('Enter a command.');
    const d = await api('explain', {argv, workspace: $('#route-workspace').value, estimated_duration_ms: Number($('#route-estimate').value) * 1000, policy: {local_fallback: true}});
    const verdict = d.target === 'LOCAL' ? 'Would run on this laptop' : d.target === 'WAIT' ? 'Would wait for capacity' : d.target === 'REJECT' ? 'Would not run' : 'Would run on the phone';
    $('#route-result').innerHTML = `<div class="verdict"><strong>${verdict}</strong><p class="muted">${esc((d.explanation || '').replace(/^worker-[0-9a-f]+:\s*/, ''))}</p>`
      + (d.candidates?.length ? '<ul class="reasons">' + d.candidates.map(c => `<li class="${c.eligible ? 'ok' : 'no'}"><b>${c.target.startsWith('worker-') ? 'Phone' : esc(c.target)}: ${c.eligible ? 'could run it' : 'not possible'}</b>${esc((c.reasons || []).join(' '))}</li>`).join('') + '</ul>' : '') + '</div>';
  } catch (error) { showError(error); }
});

// ---------------------------------------------------------------- interaction
document.addEventListener('click', async event => {
  if (!(event.target instanceof Element)) return;
  if (event.target === $('#modal')) return closeModal();
  const target = event.target.closest('button');
  if (!target) return;
  const data = target.dataset;
  try {
    if (data.job) return openJob(data.job);
    if ('close' in data || target.id === 'modal-close') return closeModal();
    if (data.copy !== undefined) { await navigator.clipboard?.writeText(data.copy); target.textContent = 'Copied'; return; }
    if (data.cancel) { await api('jobs/' + data.cancel + '/cancel', {}); return refreshModal(); }
    if (data.drain) return void await api('devices/' + data.drain + '/drain', {draining: data.value === 'true'});
    if (data.mode) return void await api('settings', {mode: data.mode});
    if (data.capacity) return void await api('settings', {worker_concurrency: Number(data.capacity)});
    if ('more' in data) { view.expanded = !view.expanded; return renderJobs(true); }
    if (data.filter) {
      view.filter = data.filter;
      for (const button of document.querySelectorAll('[data-filter]')) button.setAttribute('aria-checked', String(button === target));
      return renderJobs(true);
    }
    if (data.remove) return renderAutomation(await api('automation/settings', {list: data.remove, path: data.path, remove: true}));
    if (data.automation) return renderAutomation(await api('automation', {workspace: data.automation, enabled: data.enabled === 'true'}));
    if (target.id === 'pause') return void await api('settings', {paused: !view.status?.paused});
    if (target.id === 'refresh') return void await api('refresh', {});
    if (target.id === 'theme') {
      const light = document.body.classList.toggle('light');
      try { localStorage.setItem('tidalbridge.theme', light ? 'light' : 'dark'); } catch {}
    }
  } catch (error) { showError(error); }
});
for (const form of document.querySelectorAll('form.add')) {
  form.addEventListener('submit', async event => {
    event.preventDefault();
    const input = form.querySelector('input');
    try { renderAutomation(await api('automation/settings', {list: form.dataset.list, path: input.value.trim()})); input.value = ''; } catch (error) { showError(error); }
  });
}
$('#adaptive').addEventListener('change', async event => {
  try { await api('settings', {fixed_capacity: !event.target.checked}); } catch (error) { showError(error); event.target.checked = !event.target.checked; }
});
$('#universal').addEventListener('change', async event => {
  try { renderAutomation(await api('automation/settings', {universal: event.target.checked})); } catch (error) { showError(error); event.target.checked = !event.target.checked; }
});
document.addEventListener('keydown', event => { if (event.key === 'Escape' && view.modal) closeModal(); });
const tip = $('#tip');
function showTip(element) {
  tip.textContent = element.dataset.tip;
  tip.hidden = false;
  const box = element.getBoundingClientRect(), width = tip.offsetWidth, height = tip.offsetHeight;
  tip.style.left = Math.max(8, Math.min(innerWidth - width - 8, box.left + box.width / 2 - width / 2)) + 'px';
  tip.style.top = (box.top > height + 14 ? box.top - height - 8 : box.bottom + 8) + 'px';
}
for (const type of ['mouseover', 'focusin']) document.addEventListener(type, event => { const element = event.target instanceof Element && event.target.closest('[data-tip]'); if (element) showTip(element); });
for (const type of ['mouseout', 'focusout']) document.addEventListener(type, event => { if (event.target instanceof Element && event.target.closest('[data-tip]')) tip.hidden = true; });
try { if (localStorage.getItem('tidalbridge.theme') === 'light') document.body.classList.add('light'); } catch {}

// ---------------------------------------------------------------- stream
function setLive(on) {
  $('#live').classList.toggle('on', on);
  $('#live-text').textContent = on ? 'Live' : 'Reconnecting';
}
function handle(block) {
  let type = 'message', data = '';
  for (const line of block.split('\n')) {
    if (line.startsWith('event: ')) type = line.slice(7);
    else if (line.startsWith('data: ')) data += line.slice(6);
  }
  if (!data) return;
  const value = JSON.parse(data);
  if (type === 'pulse') renderPulse(value);
  else if (type === 'workers') { if (view.status) renderStatus({...view.status, workers: value.workers}); }
  else { $('#error').hidden = true; renderStatus(value); }
}
async function connect() {
  clearTimeout(reconnect);
  abort?.abort();
  if (document.hidden) return;
  abort = new AbortController();
  try {
    if (!token) throw new Error('Open the dashboard from the Tidal Bridge app (or `tidalbridge dashboard`), which adds its access key.');
    const response = await fetch('/v1/events', {headers: {Authorization: 'Bearer ' + token}, signal: abort.signal});
    if (!response.ok) throw new Error('The access key was not accepted. Open the dashboard again from the app.');
    setLive(true);
    const reader = response.body.getReader(), decoder = new TextDecoder();
    let buffer = '';
    for (;;) {
      const {value, done} = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, {stream: true});
      let cut;
      while ((cut = buffer.indexOf('\n\n')) >= 0) { handle(buffer.slice(0, cut)); buffer = buffer.slice(cut + 2); }
    }
  } catch (error) {
    if (error.name === 'AbortError') return;
    showError(error);
  }
  setLive(false);
  if (!document.hidden) reconnect = setTimeout(connect, 3000);
}
document.addEventListener('visibilitychange', () => { if (document.hidden) { abort?.abort(); clearTimeout(reconnect); } else connect(); });
connect();
