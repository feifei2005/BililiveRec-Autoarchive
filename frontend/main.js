// ==================== 全局状态 ====================
const state = {
    currentPage: 'status',
    refreshInterval: null,
    taskFilter: 'all',
    isShuttingDown: false,
    currentTaskTab: 'pending',        // 任务列表当前选中的标签
    currentTranscodeTab: 'pending'    // 转码任务当前选中的标签
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

// HTML 转义，防止 XSS
function escapeHtml(text) {
    if (!text) return '';
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
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
        case 'transcode':
            loadTranscodePage();
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
        renderTasks(tasks);
        
        // 更新转封装进度
        await updateRemuxProgress();
        
        // 更新转封装暂停状态
        await updateRemuxPauseStatus();
    } catch (error) {
        console.error('加载任务列表失败:', error);
        showToast('加载任务失败', 'error');
    }
}

// ==================== 转封装进度显示 ====================

// 更新转封装进度显示
async function updateRemuxProgress() {
    try {
        const progress = await window.go.app.App.GetRemuxProgress();
        const panel = document.getElementById('remux-progress-panel');
        
        if (!panel) return;
        
        // 进度面板始终显示
        panel.style.display = 'block';
        
        // 检查是否有活跃的转封装任务
        const isActive = progress && progress.isActive;
        
        // 更新暂停状态样式
        if (isActive && progress.isPaused) {
            panel.classList.add('paused');
            panel.classList.remove('active');
        } else if (isActive) {
            panel.classList.remove('paused');
            panel.classList.add('active');
        } else {
            panel.classList.remove('paused');
            panel.classList.remove('active');
        }
        
        // 更新当前文件名
        const currentFileEl = document.getElementById('remux-current-file');
        if (currentFileEl) {
            if (isActive) {
                currentFileEl.textContent = progress.currentFileStr || formatPath(progress.currentFile) || '--';
            } else {
                currentFileEl.textContent = '暂无任务';
            }
        }
        
        // 更新进度条
        const progressBar = document.getElementById('remux-progress-bar');
        const progressText = document.getElementById('remux-progress-text');
        if (progressBar) {
            progressBar.style.width = isActive ? `${progress.progress || 0}%` : '0%';
        }
        if (progressText) {
            progressText.textContent = isActive ? `${(progress.progress || 0).toFixed(1)}%` : '0%';
        }
        
        // 更新文件大小
        const sourceSizeEl = document.getElementById('remux-source-size');
        if (sourceSizeEl) {
            sourceSizeEl.textContent = isActive ? (progress.sourceSizeStr || '--') : '--';
        }
        
        // 更新已写入量
        const writtenSizeEl = document.getElementById('remux-written-size');
        if (writtenSizeEl) {
            writtenSizeEl.textContent = isActive ? (progress.writtenSizeStr || '--') : '--';
        }
        
        // 更新速度
        const speedEl = document.getElementById('remux-speed');
        if (speedEl) {
            speedEl.textContent = isActive ? (progress.speedStr || '--') : '--';
        }
        
        // 更新已用时间
        const elapsedEl = document.getElementById('remux-elapsed');
        if (elapsedEl) {
            elapsedEl.textContent = isActive ? (progress.elapsedStr || '--') : '--';
        }
        
    } catch (error) {
        console.error('获取转封装进度失败:', error);
        // 出错时显示占位符但不隐藏面板
        const panel = document.getElementById('remux-progress-panel');
        if (panel) {
            panel.style.display = 'block';
            panel.classList.remove('paused', 'active');
        }
        // 设置占位符
        const currentFileEl = document.getElementById('remux-current-file');
        if (currentFileEl) currentFileEl.textContent = '暂无任务';
        const progressBar = document.getElementById('remux-progress-bar');
        if (progressBar) progressBar.style.width = '0%';
        const progressText = document.getElementById('remux-progress-text');
        if (progressText) progressText.textContent = '0%';
        const sourceSizeEl = document.getElementById('remux-source-size');
        if (sourceSizeEl) sourceSizeEl.textContent = '--';
        const writtenSizeEl = document.getElementById('remux-written-size');
        if (writtenSizeEl) writtenSizeEl.textContent = '--';
        const speedEl = document.getElementById('remux-speed');
        if (speedEl) speedEl.textContent = '--';
        const elapsedEl = document.getElementById('remux-elapsed');
        if (elapsedEl) elapsedEl.textContent = '--';
    }
}

// ==================== 转封装暂停控制 ====================

// 转封装暂停状态
const remuxPauseState = {
    paused: false,
    pauseAfterCurrent: false
};

// 初始化转封装暂停控制按钮
function initRemuxPauseControls() {
    // 立即暂停按钮
    document.getElementById('btn-pause-remux')?.addEventListener('click', pauseRemux);
    
    // 恢复按钮
    document.getElementById('btn-resume-remux')?.addEventListener('click', resumeRemux);
    
    // 当前任务后暂停按钮
    document.getElementById('btn-pause-remux-after-current')?.addEventListener('click', pauseRemuxAfterCurrent);
    
    // 取消当前任务后暂停按钮
    document.getElementById('btn-cancel-pause-remux-after')?.addEventListener('click', cancelPauseRemuxAfterCurrent);
}

// 立即暂停转封装
async function pauseRemux() {
    try {
        await window.go.app.App.PauseRemux();
        showToast('转封装已暂停', 'success');
        await updateRemuxPauseStatus();
    } catch (error) {
        console.error('暂停转封装失败:', error);
        showToast(`暂停失败: ${error}`, 'error');
    }
}

// 恢复转封装
async function resumeRemux() {
    try {
        await window.go.app.App.ResumeRemux();
        showToast('转封装已恢复', 'success');
        await updateRemuxPauseStatus();
    } catch (error) {
        console.error('恢复转封装失败:', error);
        showToast(`恢复失败: ${error}`, 'error');
    }
}

// 当前任务后暂停转封装
async function pauseRemuxAfterCurrent() {
    try {
        await window.go.app.App.PauseRemuxAfterCurrent();
        showToast('已设置：当前任务完成后暂停', 'info');
        await updateRemuxPauseStatus();
    } catch (error) {
        console.error('设置暂停失败:', error);
        showToast(`设置失败: ${error}`, 'error');
    }
}

// 取消当前任务后暂停转封装
async function cancelPauseRemuxAfterCurrent() {
    try {
        await window.go.app.App.CancelPauseRemuxAfterCurrent();
        showToast('已取消当前任务后暂停', 'info');
        await updateRemuxPauseStatus();
    } catch (error) {
        console.error('取消暂停失败:', error);
        showToast(`取消失败: ${error}`, 'error');
    }
}

// 更新转封装暂停状态显示
async function updateRemuxPauseStatus() {
    try {
        const status = await window.go.app.App.GetRemuxPauseStatus();
        remuxPauseState.paused = status.paused;
        remuxPauseState.pauseAfterCurrent = status.pauseAfterCurrent;
        
        const pauseBtn = document.getElementById('btn-pause-remux');
        const resumeBtn = document.getElementById('btn-resume-remux');
        const pauseAfterBtn = document.getElementById('btn-pause-remux-after-current');
        const cancelPauseAfterBtn = document.getElementById('btn-cancel-pause-remux-after');
        const statusText = document.querySelector('#remux-pause-status .pause-status-text');
        const pauseControls = document.getElementById('remux-pause-controls');
        
        // 暂停控制面板始终显示
        if (pauseControls) {
            pauseControls.style.display = 'flex';
        }
        
        if (status.paused) {
            // 已暂停状态
            if (pauseBtn) pauseBtn.style.display = 'none';
            if (resumeBtn) resumeBtn.style.display = 'inline-flex';
            if (pauseAfterBtn) pauseAfterBtn.style.display = 'none';
            if (cancelPauseAfterBtn) cancelPauseAfterBtn.style.display = 'none';
            
            if (statusText) {
                const pausedTimeStr = status.pausedTimeStr ? ` (${status.pausedTimeStr})` : '';
                statusText.textContent = `已暂停${pausedTimeStr}`;
                statusText.className = 'pause-status-text paused';
            }
        } else if (status.pauseAfterCurrent) {
            // 等待当前任务后暂停
            if (pauseBtn) pauseBtn.style.display = 'inline-flex';
            if (resumeBtn) resumeBtn.style.display = 'none';
            if (pauseAfterBtn) pauseAfterBtn.style.display = 'none';
            if (cancelPauseAfterBtn) cancelPauseAfterBtn.style.display = 'inline-flex';
            
            if (statusText) {
                statusText.textContent = '等待当前任务完成后暂停...';
                statusText.className = 'pause-status-text waiting';
            }
        } else {
            // 正常运行状态
            if (pauseBtn) pauseBtn.style.display = 'inline-flex';
            if (resumeBtn) resumeBtn.style.display = 'none';
            if (pauseAfterBtn) pauseAfterBtn.style.display = 'inline-flex';
            if (cancelPauseAfterBtn) cancelPauseAfterBtn.style.display = 'none';
            
            if (statusText) {
                statusText.textContent = '运行中';
                statusText.className = 'pause-status-text running';
            }
        }
    } catch (error) {
        console.error('获取转封装暂停状态失败:', error);
    }
}

// 标准化任务状态
function normalizeTaskStatus(status) {
    const s = status.toLowerCase();
    if (s === 'completed') return 'success';
    return s;
}

function renderTasks(tasks) {
    // 按状态分组
    const grouped = {
        pending: tasks.filter(t => normalizeTaskStatus(t.status) === 'pending'),
        processing: tasks.filter(t => normalizeTaskStatus(t.status) === 'processing'),
        success: tasks.filter(t => normalizeTaskStatus(t.status) === 'success'),
        failed: tasks.filter(t => {
            const s = normalizeTaskStatus(t.status);
            return s === 'failed' || s === 'discarded';
        })
    };
    
    // 更新计数（标签上的数字）
    document.getElementById('pending-count').textContent = grouped.pending.length;
    document.getElementById('processing-count').textContent = grouped.processing.length;
    document.getElementById('success-count').textContent = grouped.success.length;
    document.getElementById('failed-count').textContent = grouped.failed.length;
    
    // 渲染每个分区
    renderTaskList('pending-tasks', grouped.pending);
    renderTaskList('processing-tasks', grouped.processing);
    renderTaskList('success-tasks', grouped.success);
    renderTaskList('failed-tasks', grouped.failed);
}

// 初始化任务列表状态标签切换
function initTaskStatusTabs() {
    const tabsContainer = document.getElementById('task-status-tabs');
    if (!tabsContainer) return;
    
    tabsContainer.querySelectorAll('.status-tab-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            const status = btn.dataset.status;
            switchTaskStatusTab(status);
        });
    });
}

// 切换任务列表状态标签
function switchTaskStatusTab(status) {
    state.currentTaskTab = status;
    
    // 更新标签激活状态
    document.querySelectorAll('#task-status-tabs .status-tab-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.status === status);
    });
    
    // 更新内容面板显示
    document.querySelectorAll('#page-tasks .status-tab-pane').forEach(pane => {
        pane.classList.toggle('active', pane.id === `task-pane-${status}`);
    });
}

function renderTaskList(containerId, tasks) {
    const container = document.getElementById(containerId);
    if (!container) return;
    
    if (tasks.length === 0) {
        container.innerHTML = '<p class="empty-message">暂无任务</p>';
        return;
    }
    
    container.innerHTML = tasks.map(task => `
        <div class="task-item ${normalizeTaskStatus(task.status)}">
            <div class="task-info">
                <span class="task-name">${escapeHtml(task.streamerName || formatPath(task.inputPath))}</span>
                <span class="task-time">${formatTime(task.createdAt)}</span>
            </div>
            <div class="task-file" title="${escapeHtml(task.inputPath)}">${escapeHtml(formatPath(task.inputPath))}</div>
            ${normalizeTaskStatus(task.status) === 'processing' && task.progress > 0 ? `
                <div class="task-progress">
                    <div class="progress-bar" style="width: ${task.progress}%"></div>
                    <span class="progress-text">${task.progress.toFixed(1)}%</span>
                </div>
            ` : ''}
            ${task.error ? `<div class="task-error">${escapeHtml(task.error)}</div>` : ''}
        </div>
    `).join('');
}

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
    document.getElementById('config-scanIntervalMin').value = config.scanIntervalMin || 5;
    document.getElementById('config-conflictMode').value = config.conflictMode || 'skip';
    document.getElementById('config-pathTemplate').value = config.pathTemplate || '';
    document.getElementById('config-checkVideoStream').checked = config.checkVideoStream || false;
    document.getElementById('config-discardFailedFiles').checked = config.discardFailedFiles || false;
    document.getElementById('config-deleteOriginal').checked = config.deleteOriginal || false;
    document.getElementById('config-serverPort').value = config.serverPort || 8080;
    document.getElementById('config-webhookPath').value = config.webhookPath || '/webhook';
    document.getElementById('config-webhookEnabled').checked = config.webhookEnabled !== false; // 默认为 true
    document.getElementById('config-defaultCover').value = config.defaultCover || '';
    document.getElementById('config-saveHistory').checked = config.saveHistory !== false; // 默认为 true
    document.getElementById('config-streamerNameRegex').value = config.streamerNameRegex || '';
    
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
        scanIntervalMin: parseInt(document.getElementById('config-scanIntervalMin').value) || 5,
        conflictMode: document.getElementById('config-conflictMode').value || 'skip',
        pathTemplate: document.getElementById('config-pathTemplate').value.trim(),
        checkVideoStream: document.getElementById('config-checkVideoStream').checked,
        discardFailedFiles: document.getElementById('config-discardFailedFiles').checked,
        deleteOriginal: document.getElementById('config-deleteOriginal').checked,
        serverPort: parseInt(document.getElementById('config-serverPort').value) || 8080,
        webhookPath: document.getElementById('config-webhookPath').value.trim(),
        webhookEnabled: document.getElementById('config-webhookEnabled').checked,
        defaultCover: document.getElementById('config-defaultCover').value.trim(),
        saveHistory: document.getElementById('config-saveHistory').checked,
        streamerNameRegex: document.getElementById('config-streamerNameRegex').value.trim(),
        customArgs: customArgs
    };
}

// 保存并应用配置按钮
document.getElementById('btn-save-config')?.addEventListener('click', async () => {
    try {
        const config = getConfigFromForm();
        await window.go.app.App.SaveConfig(config);
        
        // 保存开机自启动设置
        const autoStartEnabled = document.getElementById('config-autoStart').checked;
        await window.go.app.App.SetAutoStart(autoStartEnabled);
        
        showToast('设置已成功应用', 'success');
    } catch (error) {
        console.error('保存配置失败:', error);
        showToast(`保存配置失败: ${error}`, 'error');
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

// ==================== 转码功能 ====================

// 转码状态
const transcodeState = {
    scannedVideos: [],
    isPolling: false,
    pauseStatus: {
        paused: false,
        pauseAfterCurrent: false
    }
};

// FFmpeg 预设参数
const ffmpegPresets = {
    'h264-high': '-c:v libx264 -preset slow -crf 18 -c:a aac -b:a 256k',
    'h264-medium': '-c:v libx264 -preset medium -crf 23 -c:a aac -b:a 192k',
    'h265-medium': '-c:v libx265 -preset medium -crf 28 -c:a aac -b:a 192k',
    'av1-medium': '-c:v libsvtav1 -preset 6 -crf 30 -c:a libopus -b:a 128k'
};

// 默认 FFmpeg 参数
const DEFAULT_FFMPEG_PARAMS = '-c:v libx264 -preset medium -crf 23 -c:a aac -b:a 192k';

// 转码设置缓存
let cachedTranscodeSettings = null;

// 从后端加载转码设置
async function loadTranscodeSettings() {
    try {
        const settings = await window.go.app.App.GetTranscodeSettings();
        cachedTranscodeSettings = settings;
        return settings;
    } catch (error) {
        console.error('加载转码设置失败:', error);
        return {
            params: DEFAULT_FFMPEG_PARAMS,
            format: 'mp4',
            preserveCover: true,
            deleteSourceOnSuccess: false,
            maxFps: 0
        };
    }
}

// 格式化帧率值显示
function formatMaxFps(value) {
    if (value <= 0) {
        return '不限制';
    }
    return String(value);
}

// 加载转码页面
async function loadTranscodePage() {
    // 从后端加载保存的转码设置
    const settings = await loadTranscodeSettings();
    
    // 恢复 FFmpeg 参数
    const paramsInput = document.getElementById('ffmpeg-params');
    if (paramsInput && !paramsInput.value) {
        paramsInput.value = settings.params || DEFAULT_FFMPEG_PARAMS;
    }
    
    // 恢复帧率上限
    const maxFpsInput = document.getElementById('max-fps');
    if (maxFpsInput && !maxFpsInput.value) {
        maxFpsInput.value = formatMaxFps(settings.maxFps);
    }
    
    // 恢复输出格式
    const formatSelect = document.getElementById('output-format');
    if (formatSelect && settings.format) {
        formatSelect.value = settings.format;
    }
    
    // 恢复保留封面选项
    const preserveCoverCheckbox = document.getElementById('preserve-cover');
    if (preserveCoverCheckbox) {
        preserveCoverCheckbox.checked = settings.preserveCover !== false; // 默认为 true
    }
    
    // 恢复删除源文件选项
    const deleteSourceCheckbox = document.getElementById('delete-source-on-success');
    if (deleteSourceCheckbox) {
        deleteSourceCheckbox.checked = settings.deleteSourceOnSuccess === true; // 默认为 false
    }
    
    // 加载转码任务列表
    await loadTranscodeTasks();
}

// 选择文件夹
async function selectTranscodeFolder() {
    try {
        const result = await window.go.app.App.SelectFolder();
        if (result) {
            document.getElementById('transcode-folder').value = result;
        }
    } catch (error) {
        console.error('选择文件夹失败:', error);
        showToast('选择文件夹失败', 'error');
    }
}

// 扫描文件夹
async function scanTranscodeFolder() {
    const folder = document.getElementById('transcode-folder').value;
    if (!folder) {
        showToast('请先选择文件夹', 'warning');
        return;
    }
    
    try {
        showToast('正在扫描...', 'info');
        const files = await window.go.app.App.ScanVideoFolder(folder);
        transcodeState.scannedVideos = files || [];
        renderVideoList(transcodeState.scannedVideos);
        showToast(`扫描完成，找到 ${transcodeState.scannedVideos.length} 个视频文件`, 'success');
    } catch (error) {
        console.error('扫描文件夹失败:', error);
        showToast(`扫描失败: ${error}`, 'error');
    }
}

// 渲染视频列表
function renderVideoList(files) {
    const container = document.getElementById('video-list');
    const countBadge = document.getElementById('video-count');
    
    countBadge.textContent = files.length;
    
    if (files.length === 0) {
        container.innerHTML = '<div class="empty-state">未找到视频文件</div>';
        return;
    }
    
    const html = files.map((f, index) => `
        <div class="video-item">
            <input type="checkbox" id="video-${index}" checked data-path="${escapeHtml(f.path)}">
            <label for="video-${index}" class="video-item-content">
                <span class="filename">${escapeHtml(f.name)}</span>
                <span class="video-info">
                    <span class="duration">${f.duration || '--'}</span>
                    <span class="resolution">${f.resolution || '--'}</span>
                    <span class="cover-status ${f.hasCover ? 'has-cover' : 'no-cover'}">
                        ${f.hasCover ? '有封面' : '无封面'}
                    </span>
                </span>
            </label>
        </div>
    `).join('');
    
    container.innerHTML = html;
}

// 全选/取消全选
function toggleSelectAllVideos() {
    const selectAll = document.getElementById('select-all-videos');
    const checkboxes = document.querySelectorAll('#video-list input[type="checkbox"]');
    checkboxes.forEach(cb => cb.checked = selectAll.checked);
}

// 开始转码
async function startTranscode() {
    const selectedFiles = [...document.querySelectorAll('#video-list input:checked')]
        .map(el => el.dataset.path)
        .filter(Boolean);
    
    if (selectedFiles.length === 0) {
        showToast('请选择要转码的视频', 'warning');
        return;
    }
    
    const params = document.getElementById('ffmpeg-params').value.trim();
    const format = document.getElementById('output-format').value;
    const preserveCover = document.getElementById('preserve-cover').checked;
    const deleteSourceOnSuccess = document.getElementById('delete-source-on-success').checked;
    
    // 解析帧率上限
    const maxFpsInput = document.getElementById('max-fps').value.trim();
    let maxFps = 0; // 0 表示不限制
    if (maxFpsInput && maxFpsInput !== '不限制') {
        const parsed = parseFloat(maxFpsInput);
        if (!isNaN(parsed) && parsed > 0) {
            maxFps = parsed;
        }
    }
    
    // 注意：设置会在后端 StartTranscode 中自动保存
    
    try {
        showToast('正在添加转码任务...', 'info');
        const result = await window.go.app.App.StartTranscode({
            files: selectedFiles,
            params: params,
            format: format,
            preserveCover: preserveCover,
            deleteSourceOnSuccess: deleteSourceOnSuccess,
            maxFps: maxFps
        });
        
        if (result.success) {
            showToast(`已添加 ${result.taskCount} 个转码任务`, 'success');
            document.getElementById('btn-cancel-transcode').disabled = false;
            startTranscodePolling();
        } else {
            showToast(`添加任务失败: ${result.error}`, 'error');
        }
    } catch (error) {
        console.error('开始转码失败:', error);
        showToast(`开始转码失败: ${error}`, 'error');
    }
}

// 取消转码
async function cancelTranscode() {
    try {
        await window.go.app.App.CancelAllTranscode();
        showToast('已取消所有转码任务', 'info');
        document.getElementById('btn-cancel-transcode').disabled = true;
        await loadTranscodeTasks();
    } catch (error) {
        console.error('取消转码失败:', error);
        showToast(`取消失败: ${error}`, 'error');
    }
}

// 取消单个任务
async function cancelTranscodeTask(taskId) {
    try {
        await window.go.app.App.CancelTranscodeTask(taskId);
        showToast('已取消任务', 'info');
        await loadTranscodeTasks();
    } catch (error) {
        console.error('取消任务失败:', error);
        showToast(`取消失败: ${error}`, 'error');
    }
}

// 加载转码任务列表
async function loadTranscodeTasks() {
    try {
        const tasks = await window.go.app.App.GetTranscodeTasks();
        renderTranscodeTaskList(tasks || []);
        
        // 检查是否有进行中的任务
        const hasActiveTasks = tasks && tasks.some(t =>
            t.status === 'processing' || t.status === 'pending'
        );
        document.getElementById('btn-cancel-transcode').disabled = !hasActiveTasks;
        
        // 获取并更新全局状态（总剩余时间）
        await updateGlobalTranscodeStatus();
        
        // 更新暂停状态
        await updateTranscodePauseStatus();
        
        return hasActiveTasks;
    } catch (error) {
        console.error('加载转码任务失败:', error);
        return false;
    }
}

// 更新全局转码状态（总剩余时间）
async function updateGlobalTranscodeStatus() {
    try {
        const status = await window.go.app.App.GetTranscodeGlobalStatus();
        const etaValueEl = document.getElementById('transcode-global-eta-value');
        const etaBarEl = document.getElementById('transcode-global-eta');
        
        if (etaValueEl && status) {
            if (status.pendingCount > 0 || status.processingCount > 0) {
                etaValueEl.textContent = status.totalRemainingString || '计算中...';
                if (etaBarEl) {
                    etaBarEl.style.display = 'flex';
                }
            } else {
                etaValueEl.textContent = '--';
                if (etaBarEl) {
                    etaBarEl.style.display = 'none';
                }
            }
        }
    } catch (error) {
        console.error('获取全局转码状态失败:', error);
    }
}

// 渲染转码任务列表（分页标签形式）
function renderTranscodeTaskList(tasks) {
    // 按状态分组（任务已在后端按 SeqNum 排序，保持添加顺序）
    const grouped = {
        pending: tasks.filter(t => t.status === 'pending'),
        processing: tasks.filter(t => t.status === 'processing'),
        success: tasks.filter(t => t.status === 'success'),
        cancelled: tasks.filter(t => t.status === 'cancelled' || t.status === 'failed')
    };
    
    // 更新标签上的计数
    document.getElementById('transcode-pending-count').textContent = grouped.pending.length;
    document.getElementById('transcode-processing-count').textContent = grouped.processing.length;
    document.getElementById('transcode-success-count').textContent = grouped.success.length;
    document.getElementById('transcode-cancelled-count').textContent = grouped.cancelled.length;
    
    // 渲染每个分区
    renderTranscodeTaskPane('transcode-pending-tasks', grouped.pending);
    renderTranscodeTaskPane('transcode-processing-tasks', grouped.processing);
    renderTranscodeTaskPane('transcode-success-tasks', grouped.success);
    renderTranscodeTaskPane('transcode-cancelled-tasks', grouped.cancelled);
}

// 渲染单个转码任务面板
function renderTranscodeTaskPane(containerId, tasks) {
    const container = document.getElementById(containerId);
    if (!container) return;
    
    if (tasks.length === 0) {
        container.innerHTML = '<p class="empty-message">暂无任务</p>';
        return;
    }
    
    container.innerHTML = tasks.map(t => renderTranscodeTaskItem(t)).join('');
}

// 初始化转码任务状态标签切换
function initTranscodeStatusTabs() {
    const tabsContainer = document.getElementById('transcode-status-tabs');
    if (!tabsContainer) return;
    
    tabsContainer.querySelectorAll('.status-tab-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            const status = btn.dataset.status;
            switchTranscodeStatusTab(status);
        });
    });
}

// 切换转码任务状态标签
function switchTranscodeStatusTab(status) {
    state.currentTranscodeTab = status;
    
    // 更新标签激活状态
    document.querySelectorAll('#transcode-status-tabs .status-tab-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.status === status);
    });
    
    // 更新内容面板显示
    document.querySelectorAll('#tab-task-list .status-tab-pane').forEach(pane => {
        pane.classList.toggle('active', pane.id === `transcode-pane-${status}`);
    });
}

// 渲染单个转码任务项
function renderTranscodeTaskItem(t) {
    const statusClass = getTranscodeStatusClass(t.status);
    const statusText = getTranscodeStatusText(t.status);
    
    // 视频信息（分辨率、帧数、预计时间）
    let videoInfoHtml = '';
    if (t.width && t.height) {
        const resolution = `${t.width}x${t.height}`;
        const framesInfo = t.totalFrames > 0 ? `${t.totalFrames} 帧` : '';
        videoInfoHtml = `
            <div class="task-video-info">
                <span class="video-resolution">📐 ${resolution}</span>
                ${framesInfo ? `<span class="video-frames">🎞️ ${framesInfo}</span>` : ''}
                ${t.predictedTimeString ? `<span class="video-predicted">⏱️ 预计 ${t.predictedTimeString}</span>` : ''}
            </div>
        `;
    }
    
    // 进度信息（显示已用时间、剩余时间、进度百分比）
    let progressHtml = '';
    if (t.status === 'processing') {
        const etaText = t.etaString || '计算中...';
        const elapsedText = t.elapsedString || '--';
        const speedText = t.speed ? `${t.speed}` : '';
        const fpsText = t.currentFPS ? `${t.currentFPS.toFixed(1)} fps` : '';
        const progressPercent = t.progress ? t.progress.toFixed(1) : '0.0';
        const processedFrames = t.processedFrame || 0;
        const totalFrames = t.totalFrames || 0;
        
        progressHtml = `
            <div class="task-progress-container">
                <div class="progress-bar-bg">
                    <div class="progress-bar-fill" style="width: ${t.progress}%"></div>
                </div>
                <div class="progress-info">
                    <span class="progress-elapsed">已用: ${elapsedText}</span>
                    <span class="progress-eta">剩余: ${etaText}</span>
                    <span class="progress-percent">${progressPercent}%</span>
                </div>
                <div class="progress-details">
                    ${totalFrames > 0 ? `<span class="progress-frames">${processedFrames}/${totalFrames} 帧</span>` : ''}
                    ${fpsText ? `<span class="progress-fps">${fpsText}</span>` : ''}
                    ${speedText ? `<span class="progress-speed">${speedText}</span>` : ''}
                </div>
            </div>
        `;
    }
    
    // 操作按钮
    let actionsHtml = '';
    if (t.status === 'processing' || t.status === 'pending') {
        actionsHtml = `<button class="btn btn-sm btn-danger" onclick="cancelTranscodeTask('${t.id}')">取消</button>`;
    } else if ((t.status === 'failed' || t.status === 'cancelled') && t.errorLogPath) {
        // 对于有错误日志的失败/取消任务，显示"查看错误"按钮
        actionsHtml = `<button class="btn btn-sm btn-secondary" onclick="openTranscodeErrorLog('${t.id}')">📄 查看错误日志</button>`;
    }
    
    return `
        <div class="transcode-task-item ${statusClass}">
            <div class="task-header">
                <span class="task-filename">${escapeHtml(formatPath(t.inputFile))}</span>
                <span class="task-status-badge ${statusClass}">${statusText}</span>
            </div>
            ${videoInfoHtml}
            ${progressHtml}
            <div class="task-actions">
                ${actionsHtml}
            </div>
        </div>
    `;
}

// 打开转码错误日志
async function openTranscodeErrorLog(taskId) {
    try {
        await window.go.app.App.OpenTranscodeErrorLog(taskId);
    } catch (error) {
        console.error('打开错误日志失败:', error);
        showToast(`打开错误日志失败: ${error}`, 'error');
    }
}

// 获取转码状态样式类
function getTranscodeStatusClass(status) {
    const classMap = {
        'pending': 'status-pending',
        'processing': 'status-processing',
        'success': 'status-success',
        'failed': 'status-failed',
        'cancelled': 'status-cancelled'
    };
    return classMap[status] || 'status-pending';
}

// 获取转码状态文本
function getTranscodeStatusText(status) {
    const textMap = {
        'pending': '等待中',
        'processing': '转码中',
        'success': '已完成',
        'failed': '失败',
        'cancelled': '已取消'
    };
    return textMap[status] || status;
}

// 开始轮询转码任务状态
function startTranscodePolling() {
    if (transcodeState.isPolling) return;
    
    transcodeState.isPolling = true;
    pollTranscodeStatus();
}

// 轮询转码状态
async function pollTranscodeStatus() {
    if (!transcodeState.isPolling) return;
    
    const hasActiveTasks = await loadTranscodeTasks();
    
    if (hasActiveTasks && state.currentPage === 'transcode') {
        setTimeout(pollTranscodeStatus, 1000);
    } else {
        transcodeState.isPolling = false;
    }
}

// 停止转码轮询
function stopTranscodePolling() {
    transcodeState.isPolling = false;
}

// 预设按钮点击事件
function initPresetButtons() {
    document.querySelectorAll('.preset-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            const preset = btn.dataset.preset;
            if (ffmpegPresets[preset]) {
                document.getElementById('ffmpeg-params').value = ffmpegPresets[preset];
                // 高亮当前选中的预设
                document.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'));
                btn.classList.add('active');
                showToast(`已应用预设: ${btn.textContent}`, 'info');
            }
        });
    });
}

// 页签切换功能
function initTranscodeTabs() {
    document.querySelectorAll('.transcode-tabs .tab-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            // 移除所有 active
            document.querySelectorAll('.transcode-tabs .tab-btn').forEach(b => b.classList.remove('active'));
            document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
            
            // 添加 active
            btn.classList.add('active');
            const tabId = 'tab-' + btn.dataset.tab;
            document.getElementById(tabId)?.classList.add('active');
            
            // 切换到任务列表时刷新任务
            if (btn.dataset.tab === 'task-list') {
                loadTranscodeTasks();
            }
        });
    });
}

// 刷新任务列表按钮
async function refreshTranscodeTasks() {
    showToast('正在刷新...', 'info');
    await loadTranscodeTasks();
    showToast('已刷新', 'success');
}

// 清除已完成任务
async function clearCompletedTasks() {
    try {
        await window.go.app.App.ClearCompletedTranscodeTasks();
        showToast('已清除完成的任务', 'success');
        await loadTranscodeTasks();
    } catch (error) {
        console.error('清除任务失败:', error);
        showToast(`清除失败: ${error}`, 'error');
    }
}

// 保存转码设置
async function saveTranscodeSettings() {
    const params = document.getElementById('ffmpeg-params').value.trim();
    const format = document.getElementById('output-format').value;
    const preserveCover = document.getElementById('preserve-cover').checked;
    const deleteSourceOnSuccess = document.getElementById('delete-source-on-success').checked;
    
    // 解析帧率上限
    const maxFpsInput = document.getElementById('max-fps').value.trim();
    let maxFps = 0; // 0 表示不限制
    if (maxFpsInput && maxFpsInput !== '不限制') {
        const parsed = parseFloat(maxFpsInput);
        if (!isNaN(parsed) && parsed > 0) {
            maxFps = parsed;
        }
    }
    
    try {
        await window.go.app.App.SaveTranscodeSettings({
            params: params,
            format: format,
            preserveCover: preserveCover,
            deleteSourceOnSuccess: deleteSourceOnSuccess,
            maxFps: maxFps
        });
        
        // 更新缓存
        cachedTranscodeSettings = {
            params: params,
            format: format,
            preserveCover: preserveCover,
            deleteSourceOnSuccess: deleteSourceOnSuccess,
            maxFps: maxFps
        };
        
        showToast('转码设置已保存', 'success');
    } catch (error) {
        console.error('保存转码设置失败:', error);
        showToast(`保存失败: ${error}`, 'error');
    }
}

// 转码页面事件绑定
function initTranscodeEvents() {
    // 页签切换
    initTranscodeTabs();
    
    // 初始化转码任务状态标签
    initTranscodeStatusTabs();
    
    // 文件夹选择和扫描
    document.getElementById('btn-select-folder')?.addEventListener('click', selectTranscodeFolder);
    document.getElementById('btn-scan-folder')?.addEventListener('click', scanTranscodeFolder);
    
    // 转码控制
    document.getElementById('btn-save-transcode-settings')?.addEventListener('click', saveTranscodeSettings);
    document.getElementById('btn-start-transcode')?.addEventListener('click', startTranscode);
    document.getElementById('btn-cancel-transcode')?.addEventListener('click', cancelTranscode);
    
    // 任务列表操作
    document.getElementById('btn-refresh-tasks')?.addEventListener('click', refreshTranscodeTasks);
    document.getElementById('btn-clear-completed')?.addEventListener('click', clearCompletedTasks);
    
    // 全选复选框
    document.getElementById('select-all-videos')?.addEventListener('change', toggleSelectAllVideos);
    
    initPresetButtons();
    
    // 初始化拖拽区域
    initDropZone();
    
    // 初始化��率上限输入框交互
    initMaxFpsInput();
    
    // 初始化暂停控制按钮
    initTranscodePauseControls();
}

// ==================== 转码暂停控制 ====================

// 初始化暂停控制按钮
function initTranscodePauseControls() {
    // 立即暂停按钮
    document.getElementById('btn-pause-transcode')?.addEventListener('click', pauseTranscode);
    
    // 恢复按钮
    document.getElementById('btn-resume-transcode')?.addEventListener('click', resumeTranscode);
    
    // 当前任务后暂停按钮
    document.getElementById('btn-pause-after-current')?.addEventListener('click', pauseTranscodeAfterCurrent);
    
    // 取消当前任务后暂停按钮
    document.getElementById('btn-cancel-pause-after')?.addEventListener('click', cancelPauseTranscodeAfterCurrent);
}

// 立即暂停转码
async function pauseTranscode() {
    try {
        await window.go.app.App.PauseTranscode();
        showToast('转码已暂停', 'success');
        await updateTranscodePauseStatus();
    } catch (error) {
        console.error('暂停转码失败:', error);
        showToast(`暂停失败: ${error}`, 'error');
    }
}

// 恢复转码
async function resumeTranscode() {
    try {
        await window.go.app.App.ResumeTranscode();
        showToast('转码已恢复', 'success');
        await updateTranscodePauseStatus();
    } catch (error) {
        console.error('恢复转码失败:', error);
        showToast(`恢复失败: ${error}`, 'error');
    }
}

// 当前任务后暂停
async function pauseTranscodeAfterCurrent() {
    try {
        await window.go.app.App.PauseTranscodeAfterCurrent();
        showToast('已设置：当前任务完成后暂停', 'info');
        await updateTranscodePauseStatus();
    } catch (error) {
        console.error('设置暂停失败:', error);
        showToast(`设置失败: ${error}`, 'error');
    }
}

// 取消当前任务后暂停
async function cancelPauseTranscodeAfterCurrent() {
    try {
        await window.go.app.App.CancelPauseTranscodeAfterCurrent();
        showToast('已取消当前任务后暂停', 'info');
        await updateTranscodePauseStatus();
    } catch (error) {
        console.error('取消暂停失败:', error);
        showToast(`取消失败: ${error}`, 'error');
    }
}

// 更新暂停状态显示
async function updateTranscodePauseStatus() {
    try {
        const status = await window.go.app.App.GetTranscodePauseStatus();
        transcodeState.pauseStatus = status;
        
        const pauseBtn = document.getElementById('btn-pause-transcode');
        const resumeBtn = document.getElementById('btn-resume-transcode');
        const pauseAfterBtn = document.getElementById('btn-pause-after-current');
        const cancelPauseAfterBtn = document.getElementById('btn-cancel-pause-after');
        const statusText = document.querySelector('#transcode-pause-status .pause-status-text');
        
        if (status.paused) {
            // 已暂停状态
            pauseBtn.style.display = 'none';
            resumeBtn.style.display = 'inline-flex';
            pauseAfterBtn.style.display = 'none';
            cancelPauseAfterBtn.style.display = 'none';
            
            if (statusText) {
                const pausedTimeStr = status.pausedTimeStr ? ` (${status.pausedTimeStr})` : '';
                statusText.textContent = `已暂停${pausedTimeStr}`;
                statusText.className = 'pause-status-text paused';
            }
        } else if (status.pauseAfterCurrent) {
            // 等待当前任务后暂停
            pauseBtn.style.display = 'inline-flex';
            resumeBtn.style.display = 'none';
            pauseAfterBtn.style.display = 'none';
            cancelPauseAfterBtn.style.display = 'inline-flex';
            
            if (statusText) {
                statusText.textContent = '等待当前任务完成后暂停...';
                statusText.className = 'pause-status-text waiting';
            }
        } else {
            // 正常运行状态
            pauseBtn.style.display = 'inline-flex';
            resumeBtn.style.display = 'none';
            pauseAfterBtn.style.display = 'inline-flex';
            cancelPauseAfterBtn.style.display = 'none';
            
            if (statusText) {
                statusText.textContent = '运行中';
                statusText.className = 'pause-status-text running';
            }
        }
    } catch (error) {
        console.error('获取暂停状态失败:', error);
    }
}

// 初始化帧率上限输入框交互
function initMaxFpsInput() {
    const maxFpsInput = document.getElementById('max-fps');
    if (!maxFpsInput) return;
    
    // 监听输入变化，处理"自定义"和"不限制"选项
    maxFpsInput.addEventListener('input', function(e) {
        const value = e.target.value;
        
        // 如果选择了"自定义"，清空输入框并聚焦
        if (value === '自定义') {
            e.target.value = '';
            e.target.placeholder = '请输入帧率值';
            e.target.focus();
        } else if (value === '不限制') {
            e.target.placeholder = '不限制';
        }
    });
    
    // 失去焦点时验证输入
    maxFpsInput.addEventListener('blur', function(e) {
        const value = e.target.value.trim();
        
        // 如果输入为空，恢复为"不限制"
        if (value === '' || value === '自定义') {
            e.target.value = '不限制';
            e.target.placeholder = '不限制';
        } else if (value !== '不限制') {
            // 验证是否为有效数字
            const parsed = parseFloat(value);
            if (isNaN(parsed) || parsed < 0) {
                showToast('帧率必须是正数', 'warning');
                e.target.value = '不限制';
            } else if (parsed === 0) {
                // 0 表示不限制
                e.target.value = '不限制';
            }
        }
    });
}

// ==================== 拖拽导入功能 ====================

// 初始化拖拽区域
function initDropZone() {
    const dropZone = document.getElementById('drop-zone');
    if (!dropZone) return;
    
    // 阻止默认拖拽行为
    ['dragenter', 'dragover', 'dragleave', 'drop'].forEach(eventName => {
        dropZone.addEventListener(eventName, preventDefaults, false);
        document.body.addEventListener(eventName, preventDefaults, false);
    });
    
    // 高亮效果
    ['dragenter', 'dragover'].forEach(eventName => {
        dropZone.addEventListener(eventName, highlight, false);
    });
    
    ['dragleave', 'drop'].forEach(eventName => {
        dropZone.addEventListener(eventName, unhighlight, false);
    });
    
    // 处理拖放
    dropZone.addEventListener('drop', handleDrop, false);
    
    // 点击也可以触发文件夹选择
    dropZone.addEventListener('click', selectTranscodeFolder, false);
}

function preventDefaults(e) {
    e.preventDefault();
    e.stopPropagation();
}

function highlight(e) {
    const dropZone = document.getElementById('drop-zone');
    if (dropZone) {
        dropZone.classList.add('drag-over');
    }
}

function unhighlight(e) {
    const dropZone = document.getElementById('drop-zone');
    if (dropZone) {
        dropZone.classList.remove('drag-over');
    }
}

// 处理拖放的文件/文件夹
async function handleDrop(e) {
    // 清空上次记录
    transcodeState.scannedVideos = [];
    
    // 获取拖放的文件
    const files = e.dataTransfer.files;
    if (files.length === 0) {
        showToast('未检测到拖放的文件', 'warning');
        return;
    }
    
    // 收集所有文件路径
    // 注意：由于浏览器安全限制，无法直接获取文件夹内容
    // 在 Wails 环境中，我们需要通过后端来处理
    const paths = [];
    for (let i = 0; i < files.length; i++) {
        const file = files[i];
        // Wails 中可以获取文件的完整路径
        if (file.path) {
            paths.push(file.path);
        }
    }
    
    if (paths.length === 0) {
        showToast('无法获取文件路径', 'warning');
        return;
    }
    
    showToast(`正在扫描 ${paths.length} 个文件/文件夹...`, 'info');
    
    try {
        // 调用后端扫描多个路径
        const allFiles = await window.go.app.App.ScanMultiplePaths(paths);
        transcodeState.scannedVideos = allFiles || [];
        renderVideoList(transcodeState.scannedVideos);
        
        if (transcodeState.scannedVideos.length > 0) {
            showToast(`扫描完成，找到 ${transcodeState.scannedVideos.length} 个视频文件`, 'success');
        } else {
            showToast('未找到支持的视频文件', 'warning');
        }
    } catch (error) {
        console.error('扫描拖放文件失败:', error);
        showToast(`扫描失败: ${error}`, 'error');
    }
}

// ==================== 自动刷新 ====================

function startAutoRefresh() {
    // 每 1 秒刷新一次当前页面（转封装进度需要更频繁的刷新）
    state.refreshInterval = setInterval(() => {
        if (state.currentPage === 'status') {
            loadStatusPage();
        } else if (state.currentPage === 'tasks') {
            loadTasksPage();
        }
    }, 1000);
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
    
    // 初始化任务列表状态标签
    initTaskStatusTabs();
    
    // 初始化转码事件
    initTranscodeEvents();
    
    // 初始化转封装暂停控制按钮
    initRemuxPauseControls();
    
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

// 页面可见性变化处理（从后台切换回前台时刷新数据）
document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') {
        // 页面从后台切换到前台
        console.log('页面恢复可见，刷新数据...');
        
        // 根据当前页面刷新对应数据
        switch (state.currentPage) {
            case 'status':
                loadStatusPage();
                break;
            case 'tasks':
                loadTasksPage();
                break;
            case 'transcode':
                loadTranscodeTasks();
                // 如果有活跃任务且未在轮询，重新启动轮询
                if (!transcodeState.isPolling) {
                    startTranscodePolling();
                }
                break;
        }
    }
});

// ==================== 退出确认弹窗 ====================

// 显示退出确认弹窗
function showShutdownModal(info) {
    const modal = document.getElementById('shutdown-modal');
    const message = document.getElementById('shutdown-message');
    if (!modal || !message) return;
    
    message.textContent = `当前有 ${info.activeCount} 个任务正在处理中，${info.pendingCount} 个任务等待处理。请选择退出方式：`;
    modal.style.display = 'flex';
}

// 隐藏退出确认弹窗
function hideShutdownModal() {
    const modal = document.getElementById('shutdown-modal');
    if (modal) {
        modal.style.display = 'none';
    }
}

// 监听窗口关闭事件（从 Wails 后端触发）
if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn("shutdown-requested", async () => {
        try {
            const info = await window.go.app.App.RequestShutdown();
            if (info.hasActiveTasks) {
                showShutdownModal(info);
            } else {
                // 没有活动任务，直接退出
                await window.go.app.App.ShutdownNow();
            }
        } catch (error) {
            console.error('处理退出请求失败:', error);
            // 出错时直接退出
            await window.go.app.App.ShutdownNow();
        }
    });
}

// 退出弹窗按钮事件处理
document.getElementById('shutdown-now-btn')?.addEventListener('click', async () => {
    try {
        hideShutdownModal();
        showToast('正在停止任务并退出...', 'info');
        await window.go.app.App.ShutdownNow();
    } catch (error) {
        console.error('立即退出失败:', error);
        showToast('退出失败，请重试', 'error');
    }
});

document.getElementById('shutdown-wait-btn')?.addEventListener('click', async () => {
    try {
        hideShutdownModal();
        state.isShuttingDown = true;
        showToast('正在等待任务完成后退出...', 'info');
        await window.go.app.App.ShutdownAfterCompletion();
    } catch (error) {
        console.error('等待完成后退出失败:', error);
        showToast('操作失败，请重试', 'error');
        state.isShuttingDown = false;
    }
});

document.getElementById('shutdown-cancel-btn')?.addEventListener('click', () => {
    hideShutdownModal();
});
