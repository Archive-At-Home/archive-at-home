let stats = null;

function formatDuration(startTime) {
    const now = new Date();
    const start = new Date(startTime);
    const diff = now - start;

    const hours = Math.floor(diff / (1000 * 60 * 60));
    const minutes = Math.floor((diff % (1000 * 60 * 60)) / (1000 * 60));

    if (hours > 24) {
        const days = Math.floor(hours / 24);
        return days + ' 天 ' + (hours % 24) + ' 小时';
    }
    return hours + ' 小时 ' + minutes + ' 分钟';
}

function updateUI() {
    if (!stats) return;

    // Connection status
    const statusEl = document.getElementById('connectionStatus');
    if (stats.connected) {
        const connectedSince = new Date(stats.connectedSince).toLocaleString('zh-CN');
        statusEl.innerHTML = `
            <span class="status-indicator connected">
                <span class="dot"></span>
                已连接
            </span>
            <div class="stat-sub" style="margin-top: 8px;">连接时间: ${connectedSince}</div>
        `;
    } else {
        const lastDisconnect = stats.lastDisconnect ?
            new Date(stats.lastDisconnect).toLocaleString('zh-CN') : '未知';
        statusEl.innerHTML = `
            <span class="status-indicator disconnected">
                <span class="dot"></span>
                未连接
            </span>
            <div class="stat-sub" style="margin-top: 8px;">最后断开: ${lastDisconnect}</div>
        `;
    }

    // Node info
    document.getElementById('nodeId').textContent = '节点 ID: ' + stats.nodeId;
    document.getElementById('serverUrl').textContent = '服务器: ' + stats.serverUrl;

    // Task statistics
    document.getElementById('todayTasksCompleted').textContent = stats.todayTasksCompleted;
    document.getElementById('tasksCompleted').textContent = stats.tasksCompleted;
    const failInfo = document.getElementById('taskFailInfo');
    if (stats.tasksFailed > 0) {
        failInfo.textContent = ' • ' + stats.tasksFailed + ' 失败';
    } else {
        failInfo.textContent = '';
    }

    // GP statistics
    document.getElementById('gpBalance').textContent = stats.gpBalance.toLocaleString();
    document.getElementById('todayGPCost').textContent = stats.todayGPCost.toLocaleString();
    document.getElementById('totalGPCost').textContent = stats.totalGPCost.toLocaleString();

    // Quota status
    const quotaEl = document.getElementById('quotaStatus');
    if (stats.haveFreeQuota) {
        quotaEl.innerHTML = '<span class="quota-badge available">✓ 有免费额度</span>';
    } else {
        quotaEl.innerHTML = '<span class="quota-badge unavailable">✗ 无免费额度</span>';
    }

    // GP progress (if max GP cost is set)
    if (stats.maxGPCost > 0) {
        const progress = (stats.todayGPCost / stats.maxGPCost) * 100;
        document.getElementById('gpProgress').style.width = Math.min(progress, 100) + '%';
    } else {
        document.getElementById('gpProgress').style.width = '0%';
    }

    // Average statistics
    document.getElementById('avgGPPerTask').textContent = stats.avgGPPerTask.toFixed(1);
    document.getElementById('avgSizeMiB').textContent = stats.avgSizeMiB.toFixed(1);

    // Total size
    const totalSizeGiB = stats.totalSizeMiB / 1024;
    if (totalSizeGiB >= 1) {
        document.getElementById('totalSizeMiB').textContent = totalSizeGiB.toFixed(2);
        document.getElementById('totalSizeMiB').nextElementSibling.textContent = 'GiB';
    } else {
        document.getElementById('totalSizeMiB').textContent = stats.totalSizeMiB.toFixed(1);
    }

    // Uptime
    document.getElementById('uptime').textContent = formatDuration(stats.startTime);
    document.getElementById('startTime').textContent = '启动于 ' + new Date(stats.startTime).toLocaleString('zh-CN');

    // Last update
    document.getElementById('lastUpdate').textContent = new Date().toLocaleString('zh-CN');
}

async function loadStats() {
    try {
        const response = await fetch('/api/stats');
        if (!response.ok) throw new Error('Failed to fetch stats');

        stats = await response.json();
        updateUI();
    } catch (error) {
        console.error('Error loading stats:', error);
    }
}

async function reconnect() {
    try {
        const response = await fetch('/api/reconnect', { method: 'POST' });
        const result = await response.json();

        if (response.ok) {
            alert('✓ ' + result.message);
            setTimeout(loadStats, 1000);
        } else {
            alert('✗ 重连失败: ' + (result.message || '未知错误'));
        }
    } catch (error) {
        alert('✗ 重连失败: ' + error.message);
    }
}

async function refreshStatus() {
    try {
        const response = await fetch('/api/refresh', { method: 'POST' });
        const result = await response.json();

        if (response.ok) {
            alert('✓ ' + result.message);
            setTimeout(loadStats, 1000);
        } else {
            alert('✗ 刷新失败: ' + (result.message || '未知错误'));
        }
    } catch (error) {
        alert('✗ 刷新失败: ' + error.message);
    }
}

// Load stats on page load
loadStats();

// Auto-refresh every 5 seconds
setInterval(loadStats, 5000);
