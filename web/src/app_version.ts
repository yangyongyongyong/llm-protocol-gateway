// 前端版本与升级提示。
//
// 发布新版本后，已经打开控制台的标签页仍跑着旧 dist（旧 JS、旧接口假设）。
// 这里让每个标签页记住自己加载时的版本，并轮询后端当前的版本：
// 一旦后端换了版本，就提示用户升级（刷新），确认后带版本参数重新加载，
// 绕开浏览器与 CDN 对 index.html 的缓存。
//
// 版本号在构建时注入（__APP_VERSION__，见 scripts/build-version.sh），
// 未注入时为空串，视为开发构建——此时永不提示升级。
import {fetchWithTimeout} from './lib';
import {API_BASE} from './lib';

/** 本页加载时对应的版本；开发构建为空串。 */
export const CURRENT_APP_VERSION: string =
    typeof __APP_VERSION__ === 'string' ? __APP_VERSION__ : '';

/** 控制台里那次“稍后再说”记录的版本，跨标签页共享，避免反复打扰。 */
const DISMISSED_KEY = 'gateway:upgrade-dismissed-for';
/** 同一版本最多自动重载一次的标记，防止刷新后仍提示导致循环。 */
const RELOADED_KEY = 'gateway:upgraded-to';

/** 后端当前运行的版本。 */
export async function fetchLatestAppVersion(): Promise<string> {
    const response = await fetchWithTimeout(`${API_BASE}/__app/version?t=${Date.now()}`, {
        credentials: 'same-origin',
        cache: 'no-store',
    });
    if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
    }
    const data = await response.json() as { version?: unknown };
    return typeof data.version === 'string' ? data.version.trim() : '';
}

type ParsedVersion = { nums: [number, number, number]; pre: string };

function parseVersion(value: string): ParsedVersion | null {
    let v = value.trim().replace(/^v/, '');
    v = v.split('+')[0];
    const dash = v.indexOf('-');
    const core = dash >= 0 ? v.slice(0, dash) : v;
    const pre = dash >= 0 ? v.slice(dash + 1) : '';
    const parts = core.split('.');
    if (parts.length !== 3) return null;
    const nums = parts.map((part) => Number(part));
    if (nums.some((n) => !Number.isInteger(n) || n < 0)) return null;
    return {nums: [nums[0], nums[1], nums[2]], pre};
}

function comparePrerelease(a: string, b: string): number {
    if (a === b) return 0;
    if (!a) return 1;
    if (!b) return -1;
    const as = a.split('.');
    const bs = b.split('.');
    for (let i = 0; i < Math.min(as.length, bs.length); i += 1) {
        const an = /^\d+$/.test(as[i]) ? Number(as[i]) : null;
        const bn = /^\d+$/.test(bs[i]) ? Number(bs[i]) : null;
        if (an != null && bn != null) {
            if (an !== bn) return an > bn ? 1 : -1;
        } else if (an != null) {
            return -1;
        } else if (bn != null) {
            return 1;
        } else if (as[i] !== bs[i]) {
            return as[i] > bs[i] ? 1 : -1;
        }
    }
    return as.length === bs.length ? 0 : as.length > bs.length ? 1 : -1;
}

/** 版本是否严格更新。构建时间戳不是合法 semver 时保持旧行为。 */
export function isNewerVersion(latest: string, current: string): boolean {
    if (!latest || !current) return false;
    const a = parseVersion(latest);
    const b = parseVersion(current);
    if (!a || !b) return latest !== current && latest > current;
    for (let i = 0; i < 3; i += 1) {
        if (a.nums[i] !== b.nums[i]) return a.nums[i] > b.nums[i];
    }
    return comparePrerelease(a.pre, b.pre) > 0;
}

/** 用户已经对这个版本点过“稍后再说”。 */
export function isUpgradeDismissed(version: string): boolean {
    try {
        return window.localStorage.getItem(DISMISSED_KEY) === version;
    } catch {
        return false;
    }
}

export function dismissUpgrade(version: string): void {
    try {
        window.localStorage.setItem(DISMISSED_KEY, version);
    } catch {
        /* 隐私模式下 localStorage 不可用：本次不再提示即可，不必报错 */
    }
}

/**
 * 带着新版本号重新加载页面。
 * 版本号放在查询参数上，让浏览器与 CDN 都把它当成另一个 URL，
 * 从而拿到新的 index.html 与新的 assets。
 */
export function reloadToVersion(version: string): void {
    const target = version.trim();
    if (!target) return;
    try {
        window.sessionStorage.setItem(RELOADED_KEY, target);
    } catch {
        /* 同 dismissUpgrade */
    }
    const url = new URL(window.location.href);
    url.searchParams.set('_v', target);
    window.location.replace(url.toString());
}
