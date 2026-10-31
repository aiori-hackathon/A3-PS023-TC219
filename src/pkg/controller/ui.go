package controller

import (
	"net/http"
)

// handleUI serves the embedded web dashboard for managing the VM controller.
func (h *Handler) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(uiHTML))
}

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>KVM Cloud Hypervisor VM Controller</title>
  <style>
    :root {
      --bg: #0f172a;
      --card-bg: #1e293b;
      --card-border: #334155;
      --primary: #3b82f6;
      --primary-hover: #2563eb;
      --success: #10b981;
      --warning: #f59e0b;
      --danger: #ef4444;
      --text: #f8fafc;
      --text-muted: #94a3b8;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
    body { background-color: var(--bg); color: var(--text); padding: 24px; }
    .container { max-width: 1200px; margin: 0 auto; }
    header { display: flex; justify-content: space-between; align-items: center; padding-bottom: 20px; border-bottom: 1px solid var(--card-border); margin-bottom: 24px; flex-wrap: wrap; gap: 12px; }
    .title-group h1 { font-size: 24px; font-weight: 700; display: flex; align-items: center; gap: 10px; }
    .badge { font-size: 11px; padding: 3px 8px; border-radius: 999px; text-transform: uppercase; font-weight: 700; letter-spacing: 0.5px; }
    .badge-online { background: rgba(16, 185, 129, 0.2); color: #34d399; border: 1px solid rgba(16, 185, 129, 0.3); }
    .badge-offline { background: rgba(239, 68, 68, 0.2); color: #f87171; border: 1px solid rgba(239, 68, 68, 0.3); }
    .token-bar { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-muted); }
    .token-bar input { background: var(--card-bg); border: 1px solid var(--card-border); color: var(--text); padding: 6px 10px; border-radius: 6px; font-size: 12px; width: 170px; }
    .btn { background: var(--primary); color: white; border: none; padding: 7px 14px; border-radius: 6px; cursor: pointer; font-size: 13px; font-weight: 500; transition: background 0.15s; }
    .btn:hover { background: var(--primary-hover); }
    .btn:disabled { opacity: 0.4; cursor: not-allowed; }
    .btn-danger { background: var(--danger); }
    .btn-danger:hover { background: #dc2626; }
    .btn-secondary { background: #475569; }
    .btn-secondary:hover { background: #334155; }
    .btn-sm { padding: 4px 8px; font-size: 12px; }
    
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 16px; margin-bottom: 24px; }
    .card { background: var(--card-bg); border: 1px solid var(--card-border); border-radius: 10px; padding: 18px; }
    .card h2 { font-size: 16px; font-weight: 600; margin-bottom: 14px; display: flex; justify-content: space-between; align-items: center; }
    .stat-row { display: flex; justify-content: space-between; margin-bottom: 8px; font-size: 13px; color: var(--text-muted); }
    .stat-row strong { color: var(--text); }
    .progress-bar-bg { background: #334155; height: 8px; border-radius: 4px; overflow: hidden; margin-top: 6px; }
    .progress-bar-fill { background: var(--primary); height: 100%; transition: width 0.3s; }

    .form-group { margin-bottom: 12px; }
    .form-group label { display: block; font-size: 12px; color: var(--text-muted); margin-bottom: 4px; }
    .form-control { width: 100%; background: #0f172a; border: 1px solid var(--card-border); color: var(--text); padding: 8px 10px; border-radius: 6px; font-size: 13px; }
    .form-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }

    table { width: 100%; border-collapse: collapse; margin-top: 10px; font-size: 13px; }
    th { text-align: left; padding: 10px; color: var(--text-muted); border-bottom: 1px solid var(--card-border); font-weight: 600; font-size: 12px; }
    td { padding: 12px 10px; border-bottom: 1px solid #293548; vertical-align: middle; }
    tr:hover td { background: rgba(255,255,255,0.02); }

    .state-CREATING { background: rgba(245, 158, 11, 0.2); color: #fbbf24; border: 1px solid rgba(245, 158, 11, 0.3); }
    .state-CREATED { background: rgba(59, 130, 246, 0.2); color: #60a5fa; border: 1px solid rgba(59, 130, 246, 0.3); }
    .state-RUNNING { background: rgba(16, 185, 129, 0.2); color: #34d399; border: 1px solid rgba(16, 185, 129, 0.3); box-shadow: 0 0 8px rgba(16,185,129,0.3); }
    .state-STOPPED { background: rgba(148, 163, 184, 0.2); color: #cbd5e1; border: 1px solid rgba(148, 163, 184, 0.3); }
    .state-ERROR { background: rgba(239, 68, 68, 0.2); color: #f87171; border: 1px solid rgba(239, 68, 68, 0.3); }

    .ip-tag { background: #0f172a; border: 1px solid #334155; padding: 3px 8px; border-radius: 4px; font-family: monospace; font-size: 12px; cursor: pointer; color: #38bdf8; display: inline-flex; align-items: center; gap: 4px; }
    .ip-tag:hover { background: #1e293b; border-color: #38bdf8; }
    .actions { display: flex; gap: 6px; }
    .toast { position: fixed; bottom: 20px; right: 20px; background: #1e293b; border: 1px solid var(--card-border); padding: 12px 18px; border-radius: 8px; box-shadow: 0 8px 24px rgba(0,0,0,0.4); display: none; z-index: 100; font-size: 13px; }
    .os-badge { background: #312e81; color: #a5b4fc; padding: 2px 6px; border-radius: 4px; font-size: 11px; font-weight: 600; }
  </style>
</head>
<body>
<div class="container">
  <header>
    <div class="title-group">
      <h1>Cloud Hypervisor VM Controller</h1>
      <span class="badge badge-online">2-Node Controller</span>
    </div>
    <div class="token-bar">
      <span>API Token:</span>
      <input type="text" id="apiToken" value="secret-controller-key" title="Bearer token for Controller">
      <button class="btn btn-secondary btn-sm" onclick="fetchData()">Refresh</button>
    </div>
  </header>

  <!-- Node Status Cards -->
  <div class="grid" id="workerGrid"></div>

  <!-- VM Deployment Form -->
  <div class="card" style="margin-bottom: 24px;">
    <h2>Deploy New MINIX Virtual Machine</h2>
    <form id="createForm" onsubmit="handleCreate(event)">
      <div class="form-row">
        <div class="form-group">
          <label>VM Name / Hostname</label>
          <input type="text" class="form-control" id="vmName" placeholder="e.g. minix-server-1" required>
        </div>
        <div class="form-group">
          <label>Target Worker Host</label>
          <select class="form-control" id="targetWorker">
            <option value="auto" selected>Auto (Memory-Aware Scheduler)</option>
            <option value="worker-1">worker-1</option>
            <option value="worker-2">worker-2</option>
          </select>
        </div>
      </div>
      <div class="form-row">
        <div class="form-group">
          <label>vCPUs (1 - 4)</label>
          <select class="form-control" id="vmVCPUs">
            <option value="1">1 vCPU</option>
            <option value="2" selected>2 vCPUs</option>
            <option value="4">4 vCPUs</option>
          </select>
        </div>
        <div class="form-group">
          <label>Memory (128 MB - 2048 MB)</label>
          <select class="form-control" id="vmMem">
            <option value="256">256 MB RAM</option>
            <option value="512" selected>512 MB RAM</option>
            <option value="1024">1024 MB RAM</option>
            <option value="2048">2048 MB RAM</option>
          </select>
        </div>
      </div>
      <div style="display: flex; justify-content: space-between; align-items: center; margin-top: 10px;">
        <label style="font-size: 13px; color: var(--text-muted); display: flex; align-items: center; gap: 6px; cursor: pointer;">
          <input type="checkbox" id="autoBoot" checked> Auto-boot immediately into RUNNING state
        </label>
        <button type="submit" class="btn" id="deployBtn">Deploy VM</button>
      </div>
    </form>
  </div>

  <!-- Virtual Machines Table -->
  <div class="card">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; flex-wrap: wrap; gap: 8px;">
      <h2>Managed Virtual Machines</h2>
      <div style="display: flex; align-items: center; gap: 8px; font-size: 13px;">
        <span style="color: var(--text-muted);">Manage Worker:</span>
        <select class="form-control" id="workerFilter" style="width: auto; padding: 4px 10px; font-size: 12px;" onchange="renderVMs(cachedVMs)">
          <option value="all">All Workers</option>
        </select>
      </div>
    </div>
    <div style="overflow-x: auto;">
      <table>
        <thead>
          <tr>
            <th>VM ID & Name</th>
            <th>Host Node</th>
            <th>Allocated Specs</th>
            <th>Live Resource Usage</th>
            <th>OS</th>
            <th>Network IP (Connect)</th>
            <th>Lifecycle Stage</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody id="vmTableBody">
          <tr><td colspan="8" style="text-align: center; color: var(--text-muted);">No virtual machines deployed yet.</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</div>

<div class="toast" id="toast"></div>

<script>
function showToast(msg, isError) {
  var t = document.getElementById('toast');
  t.innerText = msg;
  t.style.display = 'block';
  t.style.borderColor = isError ? 'var(--danger)' : 'var(--success)';
  t.style.color = isError ? '#f87171' : '#34d399';
  setTimeout(function() { t.style.display = 'none'; }, 3500);
}

function copySSH(ip, port) {
  var host = (window.location && window.location.hostname) ? window.location.hostname : (ip || '127.0.0.1');
  var p = port && port !== 22 ? ' -p ' + port : '';
  var cmd = 'ssh -o StrictHostKeyChecking=no minix@' + host + p;
  navigator.clipboard.writeText(cmd).then(function() {
    showToast('Copied: ' + cmd, false);
  }).catch(function() {
    showToast('Connect via: ' + cmd, false);
  });
}

function copyConsole(sock) {
  var cmd = 'nc -U ' + sock;
  navigator.clipboard.writeText(cmd).then(function() {
    showToast('Copied console command: ' + cmd, false);
  }).catch(function() {
    showToast('Connect via: ' + cmd, false);
  });
}

var cachedVMs = [];
var cachedWorkers = [];

function getToken() {
  return document.getElementById('apiToken').value.trim();
}

function fetchData() {
  var token = getToken();
  var headers = token ? { 'Authorization': 'Bearer ' + token } : {};

  // Fetch Workers
  fetch('/workers', { headers: headers })
    .then(function(res) { return res.json(); })
    .then(function(data) {
      cachedWorkers = data.data || [];
      renderWorkers(cachedWorkers);
    })
    .catch(function(err) { console.error('Failed to fetch workers', err); });

  // Fetch VMs
  fetch('/vms', { headers: headers })
    .then(function(res) { return res.json(); })
    .then(function(data) {
      cachedVMs = data.data || [];
      renderVMs(cachedVMs);
    })
    .catch(function(err) { console.error('Failed to fetch VMs', err); });
}

function renderWorkers(workers) {
  var grid = document.getElementById('workerGrid');
  grid.innerHTML = '';

  // Sort workers deterministically by ID so worker-1 is always on the left, worker-2 on the right
  var sortedWorkers = (workers || []).slice().sort(function(a, b) {
    return (a.id || '').localeCompare(b.id || '');
  });

  var targetSel = document.getElementById('targetWorker');
  var prevTarget = targetSel ? targetSel.value : 'auto';
  var targetOptions = '<option value="auto">Auto (Best Memory Available)</option>';

  var filterSel = document.getElementById('workerFilter');
  var prevFilter = filterSel ? filterSel.value : 'all';
  var filterOptions = '<option value="all">All Workers</option>';

  sortedWorkers.forEach(function(w) {
    var isOnline = w.healthy;
    var r = w.resources || {};
    var total = r.total_memory_mb || 0;
    var free = r.free_memory_mb || 0;
    var reserved = r.reserved_memory_mb || 512;
    var used = total - (r.available_memory_mb || 0);
    var pct = total > 0 ? Math.round((used / total) * 100) : 0;

    var statusLabel = isOnline ? (free + ' MB free') : 'OFFLINE';
    targetOptions += '<option value="' + escapeHtml(w.id) + '">' + escapeHtml(w.id) + ' (' + statusLabel + ')' + (isOnline ? '' : ' - UNAVAILABLE') + '</option>';
    filterOptions += '<option value="' + escapeHtml(w.id) + '">' + escapeHtml(w.id) + '</option>';

    var card = document.createElement('div');
    card.className = 'card';
    card.innerHTML = 
      '<h2>' + escapeHtml(w.id) + ' <span style="font-size: 12px; font-weight: normal; color: var(--text-muted);">' + escapeHtml(w.url) + '</span>' +
      '<span class="badge ' + (isOnline ? 'badge-online' : 'badge-offline') + '">' + (isOnline ? 'HEALTHY' : 'OFFLINE') + '</span></h2>' +
      '<div class="stat-row"><span>Available Memory:</span><strong>' + free + ' MB free</strong></div>' +
      '<div class="stat-row"><span>System Reserve:</span><strong>' + reserved + ' MB</strong></div>' +
      '<div class="stat-row"><span>Running VMs:</span><strong>' + (r.running_vms || 0) + ' active</strong></div>' +
      '<div class="progress-bar-bg"><div class="progress-bar-fill" style="width: ' + pct + '%;"></div></div>' +
      '<div style="font-size: 11px; color: var(--text-muted); margin-top: 4px; text-align: right;">' + pct + '% RAM Committed</div>';
    grid.appendChild(card);
  });

  if (targetSel) {
    targetSel.innerHTML = targetOptions;
    targetSel.value = prevTarget;
  }
  if (filterSel) {
    filterSel.innerHTML = filterOptions;
    filterSel.value = prevFilter;
  }
}

function renderVMs(vms) {
  var tbody = document.getElementById('vmTableBody');
  var filter = document.getElementById('workerFilter') ? document.getElementById('workerFilter').value : 'all';

  // Sort VMs deterministically by creation time and ID so rows never jump around
  var sortedVMs = (vms || []).slice().sort(function(a, b) {
    var ta = new Date(a.created_at || 0).getTime();
    var tb = new Date(b.created_at || 0).getTime();
    if (ta !== tb) return ta - tb;
    return (a.id || '').localeCompare(b.id || '');
  });

  var filtered = sortedVMs.filter(function(vm) {
    if (filter === 'all') return true;
    return vm.host_id === filter;
  });

  if (!filtered || filtered.length === 0) {
    var msg = (filter !== 'all') ? ('No virtual machines deployed on ' + escapeHtml(filter) + '.') : 'No virtual machines deployed yet. Deploy one above!';
    tbody.innerHTML = '<tr><td colspan="8" style="text-align: center; color: var(--text-muted); padding: 20px;">' + msg + '</td></tr>';
    return;
  }

  tbody.innerHTML = '';
  filtered.forEach(function(vm) {
    var tr = document.createElement('tr');
    var state = vm.state;
    var ip = vm.ip_address || '127.0.0.1';
    var port = vm.ssh_port || 0;
    var host = (window.location && window.location.hostname) ? window.location.hostname : (ip || '127.0.0.1');
    var connText = port > 0 ? (host + ':' + port) : (state === 'RUNNING' ? 'Starting SSH...' : 'Stopped');
    var sshCmd = 'ssh -o StrictHostKeyChecking=no minix@' + host + (port ? ' -p ' + port : '');
    var os = vm.os || 'MINIX 3';

    var u = vm.resource_usage || {};
    var cpu = typeof u.cpu_percent === 'number' ? u.cpu_percent : 0;
    var usedMem = typeof u.memory_used_mb === 'number' ? u.memory_used_mb : 0;
    var limitMem = (u.memory_limit_mb && u.memory_limit_mb > 0) ? u.memory_limit_mb : (vm.memory_mb || 512);
    var memPct = (typeof u.memory_percent === 'number' && u.memory_percent > 0) ? u.memory_percent : (limitMem > 0 ? Math.round((usedMem / limitMem) * 100) : 0);

    var usageHtml = '';
    if (state === 'RUNNING') {
      var cpuBarW = Math.min(Math.max(cpu, 2), 100);
      var memBarW = Math.min(Math.max(memPct, 2), 100);
      usageHtml = 
        '<div style="min-width: 175px;">' +
          '<div style="display:flex; justify-content:space-between; font-size:11px; margin-bottom:2px;">' +
            '<span style="color:var(--text-muted);">CPU:</span>' +
            '<strong style="color:#38bdf8;">' + cpu.toFixed(1) + '%</strong>' +
          '</div>' +
          '<div class="progress-bar-bg" style="height:4px; margin-bottom:5px;">' +
            '<div class="progress-bar-fill" style="width:' + cpuBarW + '%; background:#38bdf8;"></div>' +
          '</div>' +
          '<div style="display:flex; justify-content:space-between; font-size:11px; margin-bottom:2px;">' +
            '<span style="color:var(--text-muted);">RAM:</span>' +
            '<strong style="color:#34d399;">' + usedMem + ' MB <span style="font-size:10px; font-weight:normal; color:var(--text-muted);">/ ' + limitMem + ' MB (' + memPct + '%)</span></strong>' +
          '</div>' +
          '<div class="progress-bar-bg" style="height:4px;">' +
            '<div class="progress-bar-fill" style="width:' + memBarW + '%; background:#10b981;"></div>' +
          '</div>' +
        '</div>';
    } else if (state === 'STOPPED') {
      usageHtml = '<span style="font-size:12px; color:var(--text-muted);">0% CPU · 0 MB RAM</span><br><small style="font-size:10px; color:#64748b;">(Stopped)</small>';
    } else if (state === 'CREATED') {
      usageHtml = '<span style="font-size:12px; color:var(--text-muted);">0% CPU · 0 MB RAM</span><br><small style="font-size:10px; color:#64748b;">(Ready to boot)</small>';
    } else if (state === 'ERROR') {
      usageHtml = '<span style="font-size:12px; color:#f87171;">Unavailable</span>';
    } else {
      usageHtml = '<span style="font-size:12px; color:#fbbf24;">Initializing...</span>';
    }

    var actions = '';
    if (state === 'CREATED' || state === 'STOPPED') {
      actions += '<button class="btn btn-sm" onclick="actionVM(\'' + vm.id + '\', \'start\')">Start</button> ';
    }
    if (state === 'RUNNING') {
      actions += '<button class="btn btn-secondary btn-sm" onclick="actionVM(\'' + vm.id + '\', \'stop\')">Stop</button> ';
    }
    if (state === 'STOPPED' || state === 'CREATED' || state === 'ERROR') {
      actions += '<button class="btn btn-danger btn-sm" onclick="deleteVM(\'' + vm.id + '\')">Delete</button>';
    }

    tr.innerHTML = 
      '<td><strong>' + escapeHtml(vm.name) + '</strong><br><span style="font-size: 11px; color: var(--text-muted);">' + escapeHtml(vm.id) + '</span></td>' +
      '<td><span style="font-size: 12px; font-weight: 600; color: #38bdf8;">' + escapeHtml(vm.host_id || 'unassigned') + '</span></td>' +
      '<td>' + vm.vcpus + ' vCPU · ' + vm.memory_mb + ' MB</td>' +
      '<td>' + usageHtml + '</td>' +
      '<td><span class="os-badge">' + escapeHtml(os) + '</span></td>' +
      '<td>' +
      (port > 0 ? 
        '<span class="ip-tag" onclick="copySSH(\'' + ip + '\', ' + port + ')" title="Click to copy: ' + escapeHtml(sshCmd) + '"><strong>' + escapeHtml(connText) + '</strong> [SSH]</span>' :
        '<span style="font-size:12px;color:var(--text-muted);">' + escapeHtml(connText) + '</span>'
      ) +
      (vm.serial_socket ? '<br><span class="ip-tag" style="margin-top:4px;color:#a3e635;font-size:11px;" onclick="copyConsole(\'' + escapeHtml(vm.serial_socket) + '\')" title="Click to copy serial console command">Socket: nc -U</span>' : '') +
      '</td>' +
      '<td><span class="badge state-' + state + '">' + state + '</span>' +
      (vm.error_message ? '<br><small style="color: #f87171; font-size: 10px;">' + escapeHtml(vm.error_message) + '</small>' : '') +
      '</td>' +
      '<td><div class="actions">' + actions + '</div></td>';
    tbody.appendChild(tr);
  });
}

function escapeHtml(str) {
  if (!str) return '';
  return str.replace(/[&<>"']/g, function(m) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#039;' }[m];
  });
}

function handleCreate(e) {
  e.preventDefault();
  var token = getToken();
  var btn = document.getElementById('deployBtn');
  btn.disabled = true;
  btn.innerText = 'Deploying...';

  var targetWorker = document.getElementById('targetWorker').value;

  var payload = {
    name: document.getElementById('vmName').value.trim(),
    os: 'MINIX 3',
    vcpus: parseInt(document.getElementById('vmVCPUs').value),
    memory_mb: parseInt(document.getElementById('vmMem').value),
    auto_boot: document.getElementById('autoBoot').checked
  };

  if (targetWorker && targetWorker !== 'auto') {
    payload.worker_id = targetWorker;
  }

  fetch('/vms', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': 'Bearer ' + token
    },
    body: JSON.stringify(payload)
  })
  .then(function(res) {
    return res.json().then(function(data) { return { status: res.status, ok: res.ok, data: data }; });
  })
  .then(function(res) {
    if (res.ok) {
      showToast('VM ' + res.data.data.name + ' created on ' + res.data.data.host_id + ' (IP: ' + res.data.data.ip_address + ')', false);
      document.getElementById('vmName').value = '';
      fetchData();
    } else {
      showToast('Creation failed: ' + (res.data.error || 'Unknown error'), true);
    }
  })
  .catch(function(err) {
    showToast('Network error: ' + err.message, true);
  })
  .finally(function() {
    btn.disabled = false;
    btn.innerText = 'Deploy VM';
  });
}

function actionVM(id, action) {
  var token = getToken();
  fetch('/vms/' + id + '/' + action, {
    method: 'POST',
    headers: { 'Authorization': 'Bearer ' + token }
  })
  .then(function(res) { return res.json(); })
  .then(function(data) {
    if (data.success) {
      showToast('VM ' + action + ' succeeded', false);
      fetchData();
    } else {
      showToast('Action failed: ' + (data.error || 'Error'), true);
    }
  })
  .catch(function(err) {
    showToast('Request error: ' + err.message, true);
  });
}

function deleteVM(id) {
  if (!confirm('Are you sure you want to delete and reclaim resources for VM ' + id + '?')) return;
  var token = getToken();
  fetch('/vms/' + id, {
    method: 'DELETE',
    headers: { 'Authorization': 'Bearer ' + token }
  })
  .then(function(res) { return res.json(); })
  .then(function(data) {
    if (data.success) {
      showToast('VM deleted', false);
      fetchData();
    } else {
      showToast('Delete failed: ' + (data.error || 'Error'), true);
    }
  })
  .catch(function(err) {
    showToast('Delete error: ' + err.message, true);
  });
}

fetchData();
setInterval(fetchData, 2500);
</script>
</body>
</html>`
