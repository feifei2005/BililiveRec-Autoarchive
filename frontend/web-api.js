// Browser transport for the same Go methods that Wails injects on desktop.
(() => {
    if (window.go?.app?.App) return;

    async function call(method, args) {
        const response = await fetch(`/api/app/${encodeURIComponent(method)}`, {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ args })
        });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(payload.error || `HTTP ${response.status}`);
        return payload.result;
    }

    window.go = { app: { App: new Proxy({}, {
        get: (_, method) => (...args) => call(String(method), args)
    }) } };
    window.runtime = window.runtime || { EventsOn: () => {} };
    document.documentElement.classList.add('web-mode');
    document.getElementById('btn-logout')?.addEventListener('click', async () => {
        await fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin' });
        location.replace('/login');
    });

    fetch('/api/auth/profile', { credentials: 'same-origin' })
        .then(response => response.ok ? response.json() : null)
        .then(profile => {
            if (profile) document.getElementById('auth-username').value = profile.username;
        });

    document.getElementById('btn-change-credentials')?.addEventListener('click', async () => {
        const username = document.getElementById('auth-username').value.trim();
        const currentPassword = document.getElementById('auth-current-password').value;
        const newPassword = document.getElementById('auth-new-password').value;
        const confirmation = document.getElementById('auth-confirm-password').value;
        if (!currentPassword) return showToast('请输入当前密码', 'error');
        if (newPassword !== confirmation) return showToast('两次输入的新密码不一致', 'error');

        const response = await fetch('/api/auth/change-credentials', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username, currentPassword, newPassword })
        });
        const result = await response.json().catch(() => ({}));
        if (!response.ok) return showToast(result.error || '修改失败', 'error');
        location.replace('/login');
    });
})();
