const API_BASE = '/__ditto__/api';
const SSE_URL = '/__ditto__/events';

let eventSource = null;
let autoScroll = true;
let editingIndex = -1; // -1 = creating new, >= 0 = editing existing

// --- SSE Connection ---

function connectSSE() {
  if (eventSource) eventSource.close();

  eventSource = new EventSource(SSE_URL);
  const status = document.getElementById('connection-status');

  eventSource.onopen = () => {
    status.textContent = 'Connected';
    status.className = 'status connected';
    loadMocks(); // refresh info (port, target, URLs) on reconnect
  };

  eventSource.onmessage = (e) => {
    const event = JSON.parse(e.data);
    addLogEntry(event);
  };

  eventSource.onerror = () => {
    status.textContent = 'Disconnected';
    status.className = 'status disconnected';
    setTimeout(connectSSE, 3000);
  };
}

// --- Request Log ---

function addLogEntry(event) {
  const container = document.getElementById('log-container');
  const empty = document.getElementById('log-empty');
  const body = document.getElementById('log-body');

  if (container.classList.contains('hidden')) {
    container.classList.remove('hidden');
    empty.classList.add('hidden');
  }

  const typeLower = event.type.toLowerCase();
  const methodLower = event.method.toLowerCase();

  // Main row
  const row = document.createElement('tr');
  row.className = 'log-row' + (typeLower === 'miss' ? ' row-miss' : '');
  row.dataset.type = typeLower;
  row.dataset.searchText = `${event.method} ${event.path} ${event.type} ${event.status}`.toLowerCase();
  row.innerHTML = `
    <td>${event.timestamp}</td>
    <td><span class="type-badge type-${typeLower}">${event.type}</span></td>
    <td class="method-${methodLower}">${event.method}</td>
    <td class="td-path" title="${event.path}">${event.path}</td>
    <td>${event.status || '-'}</td>
    <td>${event.duration_ms}ms</td>
    <td>${event.type === 'PROXY' ? '<button class="btn-save-mock" title="Save as mock">Save</button>' : ''}</td>
  `;

  // Detail row (expandable)
  const detailRow = document.createElement('tr');
  detailRow.className = 'log-detail';

  let prettyBody = '';
  try {
    prettyBody = JSON.stringify(JSON.parse(event.response_body), null, 2);
  } catch {
    prettyBody = event.response_body || '(no body)';
  }

  detailRow.innerHTML = `
    <td colspan="7">
      <div class="log-detail-content">
        <div class="log-detail-header">
          <span>Response Body</span>
        </div>
        <pre>${escapeHtml(prettyBody)}</pre>
      </div>
    </td>
  `;

  // Toggle detail on row click
  row.addEventListener('click', (e) => {
    if (e.target.closest('.btn-save-mock')) return;
    row.classList.toggle('expanded');
    detailRow.classList.toggle('show');
  });

  // Save as mock button
  const saveBtn = row.querySelector('.btn-save-mock');
  if (saveBtn) {
    saveBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      openEditorForNewMock(event.method, event.path, event.status, event.response_body);
    });
  }

  body.appendChild(row);
  body.appendChild(detailRow);

  // Apply current filter to new row
  const search = document.getElementById('log-search').value.toLowerCase().trim();
  const matchesType = activeTypeFilter === 'all' || typeLower === activeTypeFilter;
  const matchesSearch = !search || row.dataset.searchText.includes(search);
  if (!matchesType || !matchesSearch) {
    row.classList.add('filtered-out');
  }

  if (autoScroll) {
    container.scrollTop = container.scrollHeight;
  } else {
    document.getElementById('btn-jump').classList.remove('hidden');
  }
}

function clearLog() {
  const body = document.getElementById('log-body');
  const container = document.getElementById('log-container');
  const empty = document.getElementById('log-empty');

  body.innerHTML = '';
  container.classList.add('hidden');
  empty.classList.remove('hidden');
}

function escapeHtml(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

// --- Mocks ---

async function loadMocks() {
  try {
    const res = await fetch(`${API_BASE}/mocks`);
    const data = await res.json();
    renderMocks(data.mocks);
    renderConnectURLs(data.info);
    updateFooter(data.info);
    document.getElementById('target-input').value = data.info.target || '';
    document.getElementById('port-input').value = data.info.port || 8888;
  } catch (err) {
    console.error('Failed to load mocks:', err);
  }
}

function renderMocks(mocks) {
  const list = document.getElementById('mock-list');
  const count = document.getElementById('mock-count');

  count.textContent = mocks.length;
  list.innerHTML = '';

  mocks.forEach((mock, index) => {
    const li = document.createElement('li');
    li.className = `mock-item${mock.enabled ? '' : ' disabled'}`;

    const methodLower = mock.method.toLowerCase();

    li.innerHTML = `
      <label class="toggle" onclick="event.stopPropagation()">
        <input type="checkbox" ${mock.enabled ? 'checked' : ''}
               onchange="toggleMock(${index})">
        <span class="slider"></span>
      </label>
      <span class="method method-${methodLower}">${mock.method}</span>
      <span class="path" title="${mock.path}" onclick="openEditorForExisting(${index})">${mock.path}</span>
      <div class="mock-actions">
        <button class="mock-action-btn" onclick="openEditorForExisting(${index})" title="Edit">&#9998;</button>
        <button class="mock-action-btn delete" onclick="deleteMock(${index})" title="Delete">&#10005;</button>
      </div>
    `;

    const pills = matchPills(mock.match);
    if (pills.length > 0) {
      const pillsEl = document.createElement('div');
      pillsEl.className = 'match-pills';
      pillsEl.innerHTML = pills.map(p => `<span class="match-pill" title="${escapeAttr(p)}">${escapeHtml(p)}</span>`).join('');
      li.appendChild(pillsEl);
    }

    list.appendChild(li);
  });
}

function matchPills(match) {
  if (!match) return [];
  const pills = [];
  if (match.query) {
    Object.entries(match.query).forEach(([k, v]) => pills.push(`?${k}=${v}`));
  }
  if (match.headers) {
    Object.entries(match.headers).forEach(([k, v]) => pills.push(`${k}: ${v}`));
  }
  if (match.body && Object.keys(match.body).length > 0) {
    pills.push(`body: ${JSON.stringify(match.body)}`);
  }
  return pills;
}

function escapeAttr(str) {
  return String(str).replace(/"/g, '&quot;');
}

async function toggleMock(index) {
  try {
    const res = await fetch(`${API_BASE}/mocks/${index}/toggle`, { method: 'POST' });
    const result = await res.json().catch(() => ({}));
    if (result.disabled_duplicates && result.disabled_duplicates.length > 0) {
      showToast(`${result.disabled_duplicates.length} duplicate mock(s) auto-disabled`, 'warn');
    }
    await loadMocks();
  } catch (err) {
    console.error('Failed to toggle mock:', err);
  }
}

async function reloadMocks() {
  try {
    await fetch(`${API_BASE}/mocks/reload`, { method: 'POST' });
    await loadMocks();
  } catch (err) {
    console.error('Failed to reload mocks:', err);
  }
}

async function deleteMock(index) {
  if (!confirm('Delete this mock? The JSON file will be removed.')) return;
  try {
    await fetch(`${API_BASE}/mocks/${index}`, { method: 'DELETE' });
    await loadMocks();
  } catch (err) {
    console.error('Failed to delete mock:', err);
  }
}

// --- Mock Editor Modal ---

function openEditorForNewMock(method, path, status, responseBody) {
  editingIndex = -1;
  document.getElementById('modal-title').textContent = 'Save as Mock';
  document.getElementById('edit-method').value = method || 'GET';

  // Strip query string from path; populate it as a match condition instead
  let cleanPath = path || '';
  let queryString = '';
  const queryIdx = cleanPath.indexOf('?');
  if (queryIdx >= 0) {
    queryString = cleanPath.slice(queryIdx + 1);
    cleanPath = cleanPath.slice(0, queryIdx);
  }

  document.getElementById('edit-path').value = cleanPath;
  document.getElementById('edit-status').value = status || 200;
  document.getElementById('edit-delay').value = 0;

  let prettyBody = '';
  try {
    prettyBody = JSON.stringify(JSON.parse(responseBody), null, 2);
  } catch {
    prettyBody = responseBody || '{}';
  }
  document.getElementById('edit-body').value = prettyBody;

  // Populate query conditions from the request URL so the user can keep them
  document.getElementById('edit-match-query').value = queryString
    ? new URLSearchParams(queryString).toString().split('&').join('\n').replace(/=/g, '=')
    : '';
  document.getElementById('edit-match-headers').value = '';
  document.getElementById('edit-match-body').value = '';
  document.getElementById('match-section').open = !!queryString;

  document.getElementById('modal-overlay').classList.remove('hidden');
}

async function openEditorForExisting(index) {
  try {
    const res = await fetch(`${API_BASE}/mocks`);
    const data = await res.json();
    const mock = data.mocks[index];
    if (!mock) return;

    editingIndex = index;
    document.getElementById('modal-title').textContent = 'Edit Mock';
    document.getElementById('edit-method').value = mock.method;
    document.getElementById('edit-path').value = mock.path;
    document.getElementById('edit-status').value = mock.status;
    document.getElementById('edit-delay').value = mock.delay_ms || 0;

    let prettyBody = '';
    try {
      prettyBody = JSON.stringify(mock.body, null, 2);
    } catch {
      prettyBody = JSON.stringify(mock.body);
    }
    document.getElementById('edit-body').value = prettyBody;

    const match = mock.match || {};
    document.getElementById('edit-match-query').value = mapToLines(match.query, '=');
    document.getElementById('edit-match-headers').value = mapToLines(match.headers, ': ');
    document.getElementById('edit-match-body').value = match.body
      ? JSON.stringify(match.body, null, 2)
      : '';
    document.getElementById('match-section').open = matchPills(match).length > 0;

    document.getElementById('modal-overlay').classList.remove('hidden');
  } catch (err) {
    console.error('Failed to load mock for editing:', err);
  }
}

function mapToLines(obj, separator) {
  if (!obj) return '';
  return Object.entries(obj)
    .map(([k, v]) => `${k}${separator}${v}`)
    .join('\n');
}

function linesToMap(text, separator) {
  if (!text || !text.trim()) return null;
  const result = {};
  for (const line of text.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const sepIdx = trimmed.indexOf(separator);
    if (sepIdx <= 0) continue;
    const key = trimmed.slice(0, sepIdx).trim();
    const value = trimmed.slice(sepIdx + separator.length).trim();
    if (key) result[key] = value;
  }
  return Object.keys(result).length > 0 ? result : null;
}

function closeModal(event) {
  if (event && event.target !== document.getElementById('modal-overlay')) return;
  document.getElementById('modal-overlay').classList.add('hidden');
}

async function saveMock() {
  const method = document.getElementById('edit-method').value;
  const path = document.getElementById('edit-path').value;
  const status = parseInt(document.getElementById('edit-status').value) || 200;
  const delayMs = parseInt(document.getElementById('edit-delay').value) || 0;
  const bodyText = document.getElementById('edit-body').value;

  let body;
  try {
    body = JSON.parse(bodyText);
  } catch (err) {
    alert('Invalid JSON in response body: ' + err.message);
    return;
  }

  // Build match conditions
  const match = {};
  const queryMap = linesToMap(document.getElementById('edit-match-query').value, '=');
  const headersMap = linesToMap(document.getElementById('edit-match-headers').value, ':');
  const bodyMatchText = document.getElementById('edit-match-body').value.trim();

  if (queryMap) match.query = queryMap;
  if (headersMap) match.headers = headersMap;
  if (bodyMatchText) {
    try {
      match.body = JSON.parse(bodyMatchText);
    } catch (err) {
      alert('Invalid JSON in match body: ' + err.message);
      return;
    }
  }

  const mock = { method, path, status, body, delay_ms: delayMs };
  if (Object.keys(match).length > 0) mock.match = match;

  try {
    let res;
    if (editingIndex >= 0) {
      res = await fetch(`${API_BASE}/mocks/${editingIndex}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(mock),
      });
    } else {
      res = await fetch(`${API_BASE}/mocks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(mock),
      });
    }

    if (!res.ok) {
      const text = await res.text();
      alert('Failed to save mock: ' + text);
      return;
    }

    const result = await res.json().catch(() => ({}));
    if (result.disabled_duplicates && result.disabled_duplicates.length > 0) {
      showToast(`${result.disabled_duplicates.length} duplicate mock(s) auto-disabled`, 'warn');
    }

    closeModal();
    await loadMocks();
  } catch (err) {
    console.error('Failed to save mock:', err);
    alert('Failed to save mock: ' + err.message);
  }
}

function showToast(message, kind) {
  const toast = document.createElement('div');
  toast.className = 'toast' + (kind ? ` ${kind}` : '');
  toast.textContent = message;
  document.body.appendChild(toast);
  setTimeout(() => toast.remove(), 3000);
}

// --- Target URL ---

async function updateTarget() {
  const input = document.getElementById('target-input');
  const url = input.value.trim();
  if (!url) return;

  try {
    const res = await fetch(`${API_BASE}/target/save`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ target: url }),
    });
    if (!res.ok) {
      const text = await res.text();
      alert('Failed to set target: ' + text);
      return;
    }
    await loadMocks(); // refresh info
  } catch (err) {
    console.error('Failed to update target:', err);
  }
}

// --- Port management ---

async function changePort() {
  const input = document.getElementById('port-input');
  const port = parseInt(input.value);
  if (!port || port < 1024 || port > 65535) {
    showPortError('Port must be between 1024 and 65535');
    return;
  }

  hidePortError();

  try {
    const res = await fetch(`${API_BASE}/port`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ port }),
    });
    const data = await res.json();
    if (!res.ok) {
      showPortError(data.error || 'Failed to change port');
      if (data.suggestions && data.suggestions.length > 0) {
        showPortSuggestions(data.suggestions);
      }
      return;
    }
    showToast(`Port changed to ${data.port}, reconnecting...`);
    // Wait for the new port to be ready before redirecting
    await waitForPort(data.port);
    window.location.href = `http://localhost:${data.port}/__ditto__/`;
  } catch (err) {
    showPortError('Failed to change port: ' + err.message);
  }
}

function showPortError(msg) {
  const el = document.getElementById('port-error');
  el.textContent = msg;
  el.classList.remove('hidden');
}

function hidePortError() {
  document.getElementById('port-error').classList.add('hidden');
  document.getElementById('port-suggestions').classList.add('hidden');
}

function showPortSuggestions(ports) {
  const el = document.getElementById('port-suggestions');
  el.innerHTML = ports.map(p =>
    `<button class="port-suggestion" onclick="selectPort(${p})">${p}</button>`
  ).join('');
  el.classList.remove('hidden');
}

async function waitForPort(port, maxAttempts = 30) {
  for (let i = 0; i < maxAttempts; i++) {
    try {
      const res = await fetch(`http://localhost:${port}/__ditto__/api/mocks`, { mode: 'no-cors' });
      return; // server is ready
    } catch {
      await new Promise(r => setTimeout(r, 200));
    }
  }
}

function selectPort(port) {
  document.getElementById('port-input').value = port;
  hidePortError();
  changePort();
}

// --- Connection URLs ---

function renderConnectURLs(info) {
  if (!info) return;
  const container = document.getElementById('connect-urls');
  const scheme = info.https ? 'https' : 'http';

  const urls = [
    { label: 'Android emulator', url: `${scheme}://10.0.2.2:${info.port}` },
    { label: 'iOS simulator', url: `${scheme}://localhost:${info.port}` },
  ];

  if (info.local_ips && info.local_ips.length > 0) {
    urls.push({
      label: 'Physical device',
      url: `${scheme}://${info.local_ips[0]}:${info.port}`
    });
  }

  container.innerHTML = urls.map(({ label, url }) => `
    <div class="connect-row">
      <span class="connect-label">${label}</span>
      <span class="connect-url" onclick="copyURL(this)" title="Click to copy">${url}</span>
    </div>
  `).join('');
}

function copyURL(el) {
  navigator.clipboard.writeText(el.textContent).then(() => {
    el.classList.add('copied');
    const original = el.textContent;
    el.textContent = 'Copied!';
    setTimeout(() => {
      el.textContent = original;
      el.classList.remove('copied');
    }, 1200);
  });
}

// --- Footer ---

function updateFooter(info) {
  if (!info) return;
  document.getElementById('footer-info').textContent = '';
  if (info.version) {
    document.getElementById('version-badge').textContent = info.version;
  }
}

// --- Browser & QR ---

async function openInBrowser() {
  try {
    await fetch(`${API_BASE}/open-browser`, { method: 'POST' });
  } catch (err) {
    // Fallback: open in current browser context
    window.open(window.location.href, '_blank');
  }
}

async function showQRCode() {
  try {
    const res = await fetch(`${API_BASE}/qr`);
    const qrURL = res.headers.get('X-Ditto-QR-URL') || '';
    const blob = await res.blob();
    const img = await createImageBitmap(blob);

    const canvas = document.getElementById('qr-canvas');
    canvas.width = img.width;
    canvas.height = img.height;
    const ctx = canvas.getContext('2d');
    ctx.drawImage(img, 0, 0);

    document.getElementById('qr-url-text').textContent = qrURL;
    document.getElementById('qr-overlay').classList.remove('hidden');
  } catch (err) {
    console.error('Failed to generate QR code:', err);
  }
}

function closeQR(event) {
  if (event && event.target !== document.getElementById('qr-overlay')) return;
  document.getElementById('qr-overlay').classList.add('hidden');
}

// --- Sidebar toggle (mobile) ---

function toggleSidebar() {
  const sidebar = document.getElementById('sidebar');
  const overlay = document.getElementById('sidebar-overlay');
  const isOpen = sidebar.classList.toggle('open');
  overlay.classList.toggle('visible', isOpen);
}

// --- Keyboard shortcuts ---

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    closeModal();
    closeQR();
  }
});

// --- Log filtering ---

let activeTypeFilter = 'all';

function toggleFilter(type) {
  activeTypeFilter = type;

  // Update button states
  document.querySelectorAll('.filter-btn').forEach(btn => {
    btn.classList.toggle('active', btn.dataset.type === type);
  });

  filterLog();
}

function clearSearch() {
  document.getElementById('log-search').value = '';
  document.getElementById('search-clear').classList.add('hidden');
  filterLog();
}

function filterLog() {
  const search = document.getElementById('log-search').value.toLowerCase().trim();
  document.getElementById('search-clear').classList.toggle('hidden', !search);
  const rows = document.querySelectorAll('.log-row');

  rows.forEach(row => {
    const type = row.dataset.type || '';
    const text = row.dataset.searchText || '';

    const matchesType = activeTypeFilter === 'all' || type === activeTypeFilter;
    const matchesSearch = !search || text.includes(search);

    row.classList.toggle('filtered-out', !matchesType || !matchesSearch);
  });
}

// --- Scroll tracking ---

function jumpToLatest() {
  const container = document.getElementById('log-container');
  container.scrollTop = container.scrollHeight;
  autoScroll = true;
  document.getElementById('btn-jump').classList.add('hidden');
}

document.addEventListener('DOMContentLoaded', () => {
  const container = document.getElementById('log-container');
  container.addEventListener('scroll', () => {
    const atBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 50;
    autoScroll = atBottom;
    if (atBottom) {
      document.getElementById('btn-jump').classList.add('hidden');
    }
  });
});

// --- Context detection ---

function isInsideWails() {
  return new URLSearchParams(window.location.search).get('desktop') === '1';
}

function isMobile() {
  return /iPhone|iPad|iPod|Android/i.test(navigator.userAgent);
}

function setupContextButtons() {
  const browserBtn = document.getElementById('btn-browser');
  const qrBtn = document.getElementById('btn-qr');

  if (isMobile()) {
    // On phone: hide both buttons
    if (browserBtn) browserBtn.style.display = 'none';
    if (qrBtn) qrBtn.style.display = 'none';
  } else if (!isInsideWails()) {
    // In regular browser: hide the browser button (already in one)
    if (browserBtn) browserBtn.style.display = 'none';
  }
}

// --- Update check ---

async function checkForUpdate() {
  try {
    const res = await fetch(`${API_BASE}/update-check`);
    const data = await res.json();
    if (data.available) {
      const banner = document.getElementById('update-banner');
      document.getElementById('update-text').textContent =
        `Ditto ${data.latest} is available (you have ${data.current}).`;
      document.getElementById('update-link').href = data.download_url;
      banner.classList.remove('hidden');
    }
  } catch (err) {
    // Silently ignore update check failures
  }
}

function dismissUpdate() {
  document.getElementById('update-banner').classList.add('hidden');
}

// --- Init ---

connectSSE();
loadMocks();
setupContextButtons();
checkForUpdate();
