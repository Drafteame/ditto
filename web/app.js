const API_BASE = '/__ditto__/api';
const SSE_URL = '/__ditto__/events';

let eventSource = null;
let autoScroll = true;

// --- SSE Connection ---

function connectSSE() {
  if (eventSource) eventSource.close();

  eventSource = new EventSource(SSE_URL);
  const status = document.getElementById('connection-status');

  eventSource.onopen = () => {
    status.textContent = 'Connected';
    status.className = 'status connected';
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
  const table = document.getElementById('log-table');
  const empty = document.getElementById('log-empty');
  const body = document.getElementById('log-body');

  if (table.classList.contains('hidden')) {
    table.classList.remove('hidden');
    empty.classList.add('hidden');
  }

  const row = document.createElement('tr');

  const typeLower = event.type.toLowerCase();
  const methodLower = event.method.toLowerCase();

  row.innerHTML = `
    <td>${event.timestamp}</td>
    <td><span class="type-badge type-${typeLower}">${event.type}</span></td>
    <td class="method-${methodLower}">${event.method}</td>
    <td title="${event.path}">${event.path}</td>
    <td>${event.status || '-'}</td>
    <td>${event.duration_ms}ms</td>
  `;

  body.appendChild(row);

  if (autoScroll) {
    body.scrollTop = body.scrollHeight;
  }
}

function clearLog() {
  const body = document.getElementById('log-body');
  const table = document.getElementById('log-table');
  const empty = document.getElementById('log-empty');

  body.innerHTML = '';
  table.classList.add('hidden');
  empty.classList.remove('hidden');
}

// --- Mocks ---

async function loadMocks() {
  try {
    const res = await fetch(`${API_BASE}/mocks`);
    const data = await res.json();
    renderMocks(data.mocks);
    updateFooter(data.info);
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
      <span class="path" title="${mock.path}">${mock.path}</span>
      <span class="mock-status">${mock.status}</span>
    `;

    list.appendChild(li);
  });
}

async function toggleMock(index) {
  try {
    await fetch(`${API_BASE}/mocks/${index}/toggle`, { method: 'POST' });
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

// --- Footer ---

function updateFooter(info) {
  if (!info) return;
  const el = document.getElementById('footer-info');
  let parts = [`Port: ${info.port}`];
  if (info.target) parts.push(`Target: ${info.target}`);
  if (info.https) parts.push('HTTPS');
  parts.push(`Mocks dir: ${info.mocks_dir}`);
  el.textContent = parts.join('  |  ');
}

// --- Init ---

connectSSE();
loadMocks();
