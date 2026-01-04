// ==================== 全局状态 ====================
const state = {
    currentPage: 'status',
    refreshInterval: null,
    taskFilter: 'all'
};

// ==================== 工具函数 ====================

// 显示 Toast 消息
function showToast(message, type = 'info') {
    const container = document.getElementById('toast-container');
    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    toast.textContent = message;
    
    container.appendChild(toast);
    
    setTimeout(() => {
        toast.style.animation = 'slideOut 0.3s ease-out';
        setTimeout(() => toast.remove(), 300);
    }, 3000);
}

// 格式化时间
function formatTime(isoString) {
    if (!isoString) return '--';
    const date = new Date(isoString);
    return date.toLocaleString('zh-CN', { 
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit'
    });
}

// 格式化文件路径（仅显示文件名）
function formatPath(path) {
    if (!path) return '--';
    const parts = path.split(/[\\/]/);
    return parts[parts.length - 1];
}

// 获取状态徽章 HTML
function getStatusBadge(status) {
    const statusMap = {
        'success': '已成功',
        'completed': '已成功', // 向后兼容
        'processing': '处理中',
        'pending': '待处理',
        'failed': '失败',
        'discarded': '已丢弃'
    };
    
    // 将 completed 统一映射为 success 以便 CSS 样式正确应用
    const normalizedStatus = status.toLowerCase() === 'completed' ? 'success' : status.toLowerCase();
    const text = statusMap[status.toLowerCase()] || status;
    return `<span class="status-badge status-${normalizedStatus}">${text}</span>`;
}

// ==================== 页面导航 ====================

function initNavigation() {
    const navItems = document.querySelectorAll('.nav-item');
    
    navItems.forEach(item => {
        item.addEventListener('click', () => {
            const page = item.dataset.page;
            if (page !== state.currentPage) {
                switchPage(page);
            }
        });
    });
}

function switchPage(page) {
    // 更新导航状态
    document.querySelectorAll('.nav-item').forEach(item => {
        item.classList.toggle('active', item.dataset.page === page);
    });
    
    // 更新页面显示
    document.querySelectorAll('.page').forEach(p => {
        p.classList.toggle('active', p.id === `page-${page}`);
    });
    
    state.currentPage = page;
    
    // 加载对应页面数据
    switch(page) {
        case 'status':
            loadStatusPage();
            break;
        case 'tasks':
            loadTasksPage();
            break;
        case 'config':
            loadConfigPage();
            break;
        case 'logs':
            loadLogsPage();
            break;
    }
}

// ==================== 状态面板 ====================

async function loadStatusPage() {
    try {
        // 加载统计数据
        const stats = await window.go.app.App.GetStats();
        updateStats(stats);
        
        // 加载最近任务
        const tasks = await window.go.app.App.GetTasks();
        const recentTasks = tasks.slice(-5).reverse(); // 最近 5 个任务
        renderRecentTasks(recentTasks);
    } catch (error) {
        console.error('加载状态面板失败:', error);
        showToast('加载状态失败', 'error');
    }
}

function updateStats(stats) {
    document.getElementById('stat-success').textContent = stats.totalProcessed || stats.totalSuccess || 0;
    document.getElementById('stat-pending').textContent = stats.pendingTasks || 0;
    document.getElementById('stat-processing').textContent = stats.processingTasks || 0;
    document.getElementById('stat-failed').textContent = stats.totalFailed || 0;
    document.getElementById('stat-discarded').textContent = stats.totalDiscarded || 0;
}

function renderRecentTasks(tasks) {
    const container = document.getElementById('recent-task-list');
    
    if (tasks.length === 0) {
        container.innerHTML = '<div class="empty-state">暂无任务</div>';
        return;
    }
    
    const html = tasks.map(task => `
        <div class="task-item" style="padding: 1rem; border-bottom: 1px solid var(--border-color);">
            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem;">
                <strong>${task.streamerName || '未知主播'}</strong>
                ${getStatusBadge(task.status)}
            </div>
            <div style="font-size: 0.875rem; color: var(--text-secondary);">
                ${formatPath(task.inputPath)}
            </div>
            ${task.progress > 0 ? `
                <div style="margin-top: 0.5rem;">
                    <div style="background: #e2e8f0; height: 4px; border-radius: 2px; overflow: hidden;">
                        <div style="background: var(--primary-color); height: 100%; width: ${task.progress}%;"></div>
                    </div>
                </div>
            ` : ''}
        </div>
    `).join('');
    
    container.innerHTML = html;
}

// 立即扫描按钮
document.getElementById('btn-scan-now')?.addEventListener('click', async () => {
    try {
        await window.go.app.App.ScanNow();
        showToast('扫描已启动', 'success');
        setTimeout(() => loadStatusPage(), 1000);
    } catch (error) {
        console.error('扫描失败:', error);
        showToast('启动扫描失败', 'error');
    }
});

// ==================== 任务列表 ====================

async function loadTasksPage() {
    try {
        const tasks = await window.go.app.App.GetTasks();
        renderTaskTable(tasks);
    } catch (error) {
        console.error('加载任务列表失败:', error);
        showToast('加载任务失败', 'error');
    }
}

function renderTaskTable(tasks) {
    const tbody = document.getElementById('task-table-body');
    
    // 应用过滤
    let filteredTasks = tasks;
    if (state.taskFilter !== 'all') {
        filteredTasks = tasks.filter(t => t.status.toLowerCase() === state.taskFilter);
    }
    
    if (filteredTasks.length === 0) {
        tbody.innerHTML = '<tr><td colspan="5" class="empty-state">暂无任务</td></tr>';
        return;
    }
    
    const html = filteredTasks.map(task => `
        <tr>
            <td>${getStatusBadge(task.status)}</td>
            <td>${task.streamerName || '--'}</td>
            <td title="${task.inputPath}">${formatPath(task.inputPath)}</td>
            <td>
                ${task.progress > 0 ? `${task.progress.toFixed(1)}%` : '--'}
                ${task.error ? `<br><span style="color: var(--error-color); font-size: 0.75rem;">${task.error}</span>` : ''}
            </td>
            <td>${formatTime(task.createdAt)}</td>
        </tr>
    `).join('');
    
    tbody.innerHTML = html;
}

// 任务过滤器
document.getElementById('task-filter')?.addEventListener('change', (e) => {
    state.taskFilter = e.target.value;
    loadTasksPage();
});

// ==================== 配置页面 ====================

async function loadConfigPage() {
    try {
        const config = await window.go.app.App.GetConfig();
        fillConfigForm(config);
        
        // 加载开机自启动状态
        const autoStartEnabled = await window.go.app.App.IsAutoStartEnabled();
        document.getElementById('config-autoStart').checked = autoStartEnabled;
    } catch (error) {
        console.error('加载配置失败:', error);
        showToast('加载配置失败', 'error');
    }
}

function fillConfigForm(config) {
    document.getElementById('config-inputDir').value = config.inputDir || '';
    document.getElementById('config-outputRoot').value = config.outputRoot || '';
    document.getElementById('config-discardDir').value = config.discardDir || '';
    document.getElementById('config-ffmpegPath').value = config.ffmpegPath || '';
    document.getElementById('config-ffprobePath').value = config.ffprobePath || '';
    document.getElementById('config-maxConcurrent').value = config.maxConcurrent || 2;
    document.getElementById('config-minFileSizeKB').value = config.minFileSizeKB || 1024;
    document.getElementById('config-pathTemplate').value = config.pathTemplate || '';
    document.getElementById('config-checkVideoStream').checked = config.checkVideoStream || false;
    document.getElementById('config-discardFailedFiles').checked = config.discardFailedFiles || false;
    document.getElementById('config-serverPort').value = config.serverPort || 8080;
    document.getElementById('config-webhookPath').value = config.webhookPath || '/webhook';
    document.getElementById('config-defaultCover').value = config.defaultCover || '';
    
    // 自定义参数（数组转换为多行文本）
    if (config.customArgs && config.customArgs.length > 0) {
        document.getElementById('config-customArgs').value = config.customArgs.join('\n');
    } else {
        document.getElementById('config-customArgs').value = '';
    }
}

function getConfigFromForm() {
    // 自定义参数（多行文本转换为数组）
    const customArgsText = document.getElementById('config-customArgs').value.trim();
    const customArgs = customArgsText ? customArgsText.split('\n').filter(line => line.trim()) : [];
    
    return {
        inputDir: document.getElementById('config-inputDir').value.trim(),
        outputRoot: document.getElementById('config-outputRoot').value.trim(),
        discardDir: document.getElementById('config-discardDir').value.trim(),
        ffmpegPath: document.getElementById('config-ffmpegPath').value.trim(),
        ffprobePath: document.getElementById('config-ffprobePath').value.trim(),
        maxConcurrent: parseInt(document.getElementById('config-maxConcurrent').value) || 2,
        minFileSizeKB: parseInt(document.getElementById('config-minFileSizeKB').value) || 1024,
        pathTemplate: document.getElementById('config-pathTemplate').value.trim(),
        checkVideoStream: document.getElementById('config-checkVideoStream').checked,
        discardFailedFiles: document.getElementById('config-discardFailedFiles').checked,
        serverPort: parseInt(document.getElementById('config-serverPort').value) || 8080,
        webhookPath: document.getElementById('config-webhookPath').value.trim(),
        defaultCover: document.getElementById('config-defaultCover').value.trim(),
        customArgs: customArgs
    };
}

// 保存配置按钮
document.getElementById('btn-save-config')?.addEventListener('click', async () => {
    try {
        const config = getConfigFromForm();
        await window.go.app.App.SaveConfig(config);
        
        // 保存开机自启动设置
        const autoStartEnabled = document.getElementById('config-autoStart').checked;
        await window.go.app.App.SetAutoStart(autoStartEnabled);
        
        showToast('配置已保存', 'success');
    } catch (error) {
        console.error('保存配置失败:', error);
        showToast(`保存失败: ${error}`, 'error');
    }
});

// 高级设置折叠
document.querySelector('.collapsible .section-toggle')?.addEventListener('click', function() {
    this.parentElement.classList.toggle('open');
});

// ==================== 错误日志 ====================

async function loadLogsPage() {
    try {
        const logs = await window.go.app.App.GetErrorLogs(50); // 获取最近 50 条
        renderLogs(logs);
    } catch (error) {
        console.error('加载日志失败:', error);
        showToast('加载日志失败', 'error');
    }
}

function renderLogs(logs) {
    const container = document.getElementById('error-log-list');
    
    if (logs.length === 0) {
        container.innerHTML = '<div class="empty-state">暂无错误日志</div>';
        return;
    }
    
    const html = logs.map(log => `
        <div class="log-item">
            <div class="log-time">${formatTime(log.timestamp)}</div>
            <div class="log-file">${formatPath(log.filePath)}</div>
            <div class="log-error">${log.error}</div>
        </div>
    `).join('');
    
    container.innerHTML = html;
}

// 刷新日志按钮
document.getElementById('btn-refresh-logs')?.addEventListener('click', () => {
    loadLogsPage();
    showToast('日志已刷新', 'info');
});

// ==================== 自动刷新 ====================

function startAutoRefresh() {
    // 每 3 秒刷新一次当前页面
    state.refreshInterval = setInterval(() => {
        if (state.currentPage === 'status') {
            loadStatusPage();
        } else if (state.currentPage === 'tasks') {
            loadTasksPage();
        }
    }, 3000);
}

function stopAutoRefresh() {
    if (state.refreshInterval) {
        clearInterval(state.refreshInterval);
        state.refreshInterval = null;
    }
}

// ==================== 系统信息 ====================

async function loadSystemInfo() {
    try {
        const info = await window.go.app.App.GetSystemInfo();
        document.getElementById('version-info').textContent = `版本: ${info.version}`;
    } catch (error) {
        console.error('加载系统信息失败:', error);
    }
}

// ==================== 应用初始化 ====================

async function initApp() {
    console.log('前端初始化中...');
    
    // 初始化导航
    initNavigation();
    
    // 加载系统信息
    await loadSystemInfo();
    
    // 加载默认页面
    await loadStatusPage();
    
    // 启动自动刷新
    startAutoRefresh();
    
    console.log('前端初始化完成');
}

// 页面加载完成后初始化
if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initApp);
} else {
    initApp();
}

// 页面卸载时停止刷新
window.addEventListener('beforeunload', () => {
    stopAutoRefresh();
});
