// 桌面 App 自更新卡片与浮层。真正的检查、下载和替换都在 Wails 原生 Go
// 进程里完成；页面只读 /__desktop/update，因此刷新页面不会中断下载。
import React, {useCallback, useEffect, useMemo, useState} from 'react';
import {API_BASE, formatBytes} from '../lib';
import {Badge, Switch} from './ui';
import './desktop_update.css';

type UpdateSettings = {
    autoCheck: boolean;
    autoDownload: boolean;
    autoInstall: boolean;
    checkIntervalHours: number;
};

type UpdateStatus = {
    available: boolean;
    state: string;
    current?: string;
    latest?: string;
    releaseUrl?: string;
    checkedAt?: string;
    progress?: number;
    total?: number;
    error?: string;
    message?: string;
    appPath?: string;
    downloaded?: boolean;
    canInstall?: boolean;
    signatureRequired?: boolean;
    signatureVerified?: boolean;
    settings: UpdateSettings;
};

const ENDPOINT = `${API_BASE}/__desktop/update`;

const stateLabels: Record<string, string> = {
    unavailable: '不可用',
    idle: '待检查',
    checking: '正在检查',
    'up-to-date': '已是最新',
    available: '发现新版本',
    downloading: '正在下载',
    ready: '等待安装',
    installing: '正在安装',
    restarting: '正在重启',
    error: '更新失败',
};

function stateTone(state: string): 'green' | 'blue' | 'amber' | 'red' | 'slate' {
    switch (state) {
        case 'up-to-date':
            return 'green';
        case 'available':
        case 'ready':
            return 'blue';
        case 'checking':
        case 'downloading':
        case 'installing':
        case 'restarting':
            return 'amber';
        case 'error':
            return 'red';
        default:
            return 'slate';
    }
}

async function requestStatus(path = '', init?: RequestInit): Promise<UpdateStatus> {
    const response = await fetch(`${ENDPOINT}${path}`, {credentials: 'same-origin', cache: 'no-store', ...init});
    const body = await response.text();
    if (!response.ok) throw new Error(body || `HTTP ${response.status}`);
    return JSON.parse(body) as UpdateStatus;
}

function useDesktopUpdateStatus(): [UpdateStatus | null, () => Promise<void>] {
    const [status, setStatus] = useState<UpdateStatus | null>(null);
    const refresh = useCallback(async () => {
        try {
            setStatus(await requestStatus());
        } catch {
            // A headless gateway has no updater; the card is simply hidden.
        }
    }, []);
    useEffect(() => {
        let cancelled = false;
        const tick = () => {
            void requestStatus().then((next) => {
                if (!cancelled) setStatus(next);
            }).catch(() => {});
        };
        tick();
        const timer = window.setInterval(tick, 2500);
        const onVisible = () => { if (!document.hidden) tick(); };
        document.addEventListener('visibilitychange', onVisible);
        return () => {
            cancelled = true;
            window.clearInterval(timer);
            document.removeEventListener('visibilitychange', onVisible);
        };
    }, []);
    return [status, refresh];
}

function progressText(status: UpdateStatus): string {
    if (!status.total || status.total <= 0) return status.progress ? formatBytes(status.progress) : '';
    const percent = Math.min(100, Math.round((status.progress || 0) * 100 / status.total));
    return `${percent}% · ${formatBytes(status.progress || 0)} / ${formatBytes(status.total)}`;
}

export function DesktopUpdateCard({onToast}: {onToast: (message: string) => void}) {
    const [status, refresh] = useDesktopUpdateStatus();
    const [busy, setBusy] = useState('');
    const settings = status?.settings;

    const updateSettings = async (patch: Partial<UpdateSettings>) => {
        if (!settings || busy) return;
        setBusy('settings');
        try {
            const next = await requestStatus('/settings', {
                method: 'PATCH',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({...settings, ...patch}),
            });
            await refresh();
            onToast(next.settings.autoCheck ? '更新设置已保存' : '已关闭自动检查；你仍可手动检查');
        } catch (error) {
            onToast(`保存更新设置失败：${String(error)}`);
        } finally {
            setBusy('');
        }
    };

    const openRelease = async () => {
        try {
            const response = await fetch(`${ENDPOINT}/open-release`, {
                method: 'POST',
                credentials: 'same-origin',
            });
            if (!response.ok) throw new Error(await response.text());
            onToast('已在系统浏览器打开 Release');
        } catch (error) {
            onToast(`打开 Release 失败：${String(error)}`);
        }
    };

    const run = async (action: 'check' | 'download' | 'install') => {
        if (busy) return;
        setBusy(action);
        try {
            await requestStatus(`/${action}`, {method: 'POST'});
            await refresh();
            onToast(action === 'check' ? '已开始检查更新' : action === 'download' ? '已开始下载更新' : '更新已安装，App 正在重启');
        } catch (error) {
            onToast(`${action === 'check' ? '检查' : action === 'download' ? '下载' : '安装'}更新失败：${String(error)}`);
            await refresh();
        } finally {
            setBusy('');
        }
    };

    if (!status?.available) return null;

    const percent = status.total && status.total > 0
        ? Math.min(100, Math.round((status.progress || 0) * 100 / status.total))
        : 0;
    const hasNewer = Boolean(status.latest && status.latest !== status.current);

    return (
        <div className="card panel desktop-update-card">
            <div className="panel-header">
                <div>
                    <h2 className="panel-title">App 自动更新</h2>
                    <p className="panel-desc">从 GitHub Release 检查、下载并校验桌面 App；自动安装会等待网关空闲；手动安装会立即重启。</p>
                </div>
                <Badge tone={stateTone(status.state)}>{stateLabels[status.state] || status.state}</Badge>
            </div>

            <div className="desktop-update-versions">
                <span>当前 <strong>{status.current || '—'}</strong></span>
                <span className="desktop-update-version-sep">→</span>
                <span>最新 <strong>{status.latest || '—'}</strong></span>
                {status.checkedAt ? <span className="desktop-update-checked">检查于 {new Date(status.checkedAt).toLocaleString()}</span> : null}
            </div>

            <div className="mon-set-switches desktop-update-switches">
                <Switch checked={settings?.autoCheck ?? true} label="自动检查更新" disabled={Boolean(busy) || !settings} onChange={(v) => void updateSettings({autoCheck: v})} />
                <Switch checked={settings?.autoDownload ?? true} label="发现更新后自动下载" disabled={Boolean(busy) || !settings} onChange={(v) => void updateSettings({autoDownload: v})} />
                <Switch checked={settings?.autoInstall ?? true} label="网关空闲时自动重启安装" disabled={Boolean(busy) || !settings} onChange={(v) => void updateSettings({autoInstall: v})} />
            </div>
            <label className="field desktop-update-interval">
                <span>检查间隔</span>
                <select
                    value={settings?.checkIntervalHours ?? 1}
                    disabled={Boolean(busy) || !settings}
                    onChange={(event) => void updateSettings({checkIntervalHours: Number(event.target.value)})}
                >
                    <option value={1}>每 1 小时</option>
                    <option value={6}>每 6 小时</option>
                    <option value={24}>每天</option>
                </select>
            </label>

            {status.state === 'downloading' ? (
                <div className="desktop-update-progress-wrap">
                    <div className="desktop-update-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent}>
                        <span style={{width: `${percent}%`}} />
                    </div>
                    <div className="hint-text">{progressText(status)}</div>
                </div>
            ) : null}

            {status.error ? <div className="desktop-update-error">{status.error}</div> : null}
            {status.message ? <div className="hint-text">{status.message}</div> : null}

            <div className="desktop-update-actions">
                <button className="btn" type="button" disabled={Boolean(busy)} onClick={() => void run('check')}>检查更新</button>
                {hasNewer && status.state !== 'downloading' && status.state !== 'ready' && status.state !== 'installing' && status.state !== 'restarting' ? (
                    <button className="btn primary" type="button" disabled={Boolean(busy)} onClick={() => void run('download')}>下载更新</button>
                ) : null}
                {status.state === 'ready' ? (
                    <button className="btn primary" type="button" disabled={Boolean(busy) || !status.canInstall} onClick={() => void run('install')}>
                        重启并安装 {status.latest}
                    </button>
                ) : null}
                {status.releaseUrl ? (
                    <button className="btn" type="button" onClick={() => void openRelease()}>查看 Release</button>
                ) : null}
            </div>
        </div>
    );
}

export function DesktopUpdateToast({onToast, onOpenSettings}: {onToast: (message: string) => void; onOpenSettings: () => void}) {
    const [status, refresh] = useDesktopUpdateStatus();
    const [dismissed, setDismissed] = useState(() => {
        try { return window.localStorage.getItem('gateway:desktop-update-dismissed-for') || ''; } catch { return ''; }
    });
    const [busy, setBusy] = useState(false);
    const latest = status?.latest || '';
    const visible = Boolean(status?.available && latest && (
        status.state === 'available' || status.state === 'downloading' || status.state === 'ready'
    ) && dismissed !== latest);

    useEffect(() => {
        if (latest && dismissed && dismissed !== latest) setDismissed('');
    }, [latest, dismissed]);

    const dismiss = () => {
        try { window.localStorage.setItem('gateway:desktop-update-dismissed-for', latest); } catch { /* ignore */ }
        setDismissed(latest);
    };
    const install = async () => {
        setBusy(true);
        try {
            await requestStatus('/install', {method: 'POST'});
            onToast('更新已安装，App 正在重启');
        } catch (error) {
            onToast(`安装更新失败：${String(error)}`);
            await refresh();
        } finally {
            setBusy(false);
        }
    };
    const download = async () => {
        setBusy(true);
        try {
            await requestStatus('/download', {method: 'POST'});
            onToast('已开始下载更新');
            await refresh();
        } catch (error) {
            onToast(`下载更新失败：${String(error)}`);
            await refresh();
        } finally {
            setBusy(false);
        }
    };

    if (!status || !visible) return null;

    const percent = status.total && status.total > 0 ? Math.min(100, Math.round((status.progress || 0) * 100 / status.total)) : 0;
    return (
        <div className="upgrade-toast desktop-update-toast" role="status" aria-live="polite">
            <div className="upgrade-toast-head">
                <span className="upgrade-toast-icon" aria-hidden="true">↻</span>
                <span className="upgrade-toast-title">{status.state === 'ready' ? '更新已准备好' : '发现 App 新版本'}</span>
                <button type="button" className="upgrade-toast-close" aria-label="稍后再说" title="稍后再说" onClick={dismiss}>×</button>
            </div>
            <div className="upgrade-toast-versions">
                <span className="upgrade-toast-version">{status.current}</span>
                <span className="upgrade-toast-arrow">→</span>
                <span className="upgrade-toast-version is-latest">{status.latest}</span>
            </div>
            {status.state === 'downloading' ? (
                <>
                    <div className="desktop-update-progress"><span style={{width: `${percent}%`}} /></div>
                    <div className="upgrade-toast-hint">{progressText(status) || '正在下载更新…'}</div>
                </>
            ) : (
                <div className="upgrade-toast-hint">{status.state === 'ready' ? '点击后立即重启；自动安装会等待当前请求完成。' : '更新会在后台下载；你仍可继续使用网关。'}</div>
            )}
            <div className="upgrade-toast-actions">
                {status.state === 'ready' ? (
                    <button type="button" className="upgrade-toast-primary" disabled={busy || !status.canInstall} onClick={() => void install()}>重启并安装</button>
                ) : null}
                {status.state === 'available' && !status.settings.autoDownload ? (
                    <button type="button" className="upgrade-toast-primary" disabled={busy} onClick={() => void download()}>下载更新</button>
                ) : null}
                <button type="button" className="upgrade-toast-secondary" onClick={onOpenSettings}>更新设置</button>
                <button type="button" className="upgrade-toast-secondary" onClick={dismiss}>稍后</button>
            </div>
        </div>
    );
}
