// lib.tsx 纯函数的回归测试。前端没有测试运行器（web/package.json 只有 tsc + vite），
// 所以这里用 node:test 直接跑：把真实的 lib.tsx 用 tsc 编成 ESM 再去调用，
// 测的是仓库里那份代码本身，不是它的复制品。
//
//   node --test web/src/lib.test.mjs
//
// 编译产物落在系统临时目录，不会进仓库，也不会被 vite 打进 web/dist（只有
// index.html 是入口）。
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {dirname, join} from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const webDir = dirname(here);
// tsc 直接用 node 跑它的 JS 入口：Windows 上 .bin/tsc.cmd 没法被 execFile 直接 spawn。
const tsc = join(webDir, 'node_modules', 'typescript', 'bin', 'tsc');
const outDir = mkdtempSync(join(tmpdir(), 'gw-lib-test-'));
process.on('exit', () => rmSync(outDir, {recursive: true, force: true}));

// 拿一个测试替身顶掉 lib.tsx 里的 react：这里只测纯函数，不渲染组件。
// （编译产物在系统临时目录，解析不到 web/node_modules，所以连加载器一起钉。）
writeFileSync(join(outDir, 'package.json'), JSON.stringify({type: 'module'}));
const stubDir = join(outDir, 'node_modules', 'react');
mkdirSync(stubDir, {recursive: true});
writeFileSync(join(stubDir, 'package.json'), JSON.stringify({name: 'react', type: 'module', main: 'index.js', exports: {
  '.': './index.js',
  './jsx-runtime': './jsx-runtime.js',
  './jsx-dev-runtime': './jsx-runtime.js',
}}));
writeFileSync(join(stubDir, 'jsx-runtime.js'), `
export const Fragment = (props) => props.children;
export const jsx = (type, props) => ({type, props});
export const jsxs = jsx;
export const jsxDEV = jsx;
`);
writeFileSync(join(stubDir, 'index.js'), `
export const createContext = (value) => ({Provider: ({children}) => children, _value: value});
export const useContext = () => undefined;
export const useState = (value) => [typeof value === 'function' ? value() : value, () => {}];
export const useEffect = () => {};
export const useLayoutEffect = () => {};
export const useMemo = (fn) => fn();
export const useCallback = (fn) => fn;
export const useRef = (value) => ({current: value});
export const useId = () => 'id';
export const Fragment = (props) => props.children;
export default {};
`);

execFileSync(process.execPath, [
  tsc,
  '--target', 'es2022', '--module', 'es2022', '--moduleResolution', 'bundler',
  '--jsx', 'react-jsx', '--skipLibCheck', '--ignoreConfig', '--outDir', outDir, join(here, 'lib.tsx'), join(here, 'pollers.ts'), join(here, 'usage_poll.ts'), join(here, 'model_merge.ts')
], {cwd: webDir, stdio: 'inherit'});
const lib = await import(pathToFileURL(join(outDir, 'lib.js')).href);
// 编译产物里的 import './lib' 没有扩展名，ESM 解析不了：把它补成 './lib.js'。
writeFileSync(join(outDir, 'pollers.js'), readFileSync(join(outDir, 'pollers.js'), 'utf8').replace("from './lib'", "from './lib.js'"));
const pollers = await import(pathToFileURL(join(outDir, 'pollers.js')).href);
const usagePoll = await import(pathToFileURL(join(outDir, 'usage_poll.js')).href);
const modelMerge = await import(pathToFileURL(join(outDir, 'model_merge.js')).href);

test('cloudflareAuthPollOutcome: 授权成功就收尾，不再等待', () => {
  assert.equal(lib.cloudflareAuthPollOutcome({authorized: true, elapsedMs: 0, timeoutMs: 600000}), 'authorized');
  // 超时与授权同时成立时，授权优先：用户刚点完授权不该被判超时。
  assert.equal(lib.cloudflareAuthPollOutcome({authorized: true, elapsedMs: 700000, timeoutMs: 600000}), 'authorized');
});

test('cloudflareAuthPollOutcome: 没授权且未超时继续等，超过时限收尾', () => {
  assert.equal(lib.cloudflareAuthPollOutcome({authorized: false, elapsedMs: 0, timeoutMs: 600000}), 'wait');
  assert.equal(lib.cloudflareAuthPollOutcome({authorized: false, elapsedMs: 600000, timeoutMs: 600000}), 'wait');
  assert.equal(lib.cloudflareAuthPollOutcome({authorized: false, elapsedMs: 600001, timeoutMs: 600000}), 'timeout');
});

// 驱动真实的 runCloudflareAuthPoll（pollers.ts）。两条契约：已授权的控制台查一次
// 就收尾、绝不留下定时器（每次 tick 都 setState 重渲染整个控制台）；检查在飞的
// 时候不叠加第二次（每 2 秒一次请求对慢后端会堆起来）。
function fakePollEnv(authorizedAnswers) {
  const timers = new Map();
  let nextId = 1;
  let calls = 0;
  const settledWith = [];
  return {
    timers,
    settledWith,
    calls: () => calls,
    env: {
      timeoutMs: 600000,
      now: () => 0,
      // 检查是异步的（真的要打后端），结果至少晚一个宏任务回来，和浏览器里
      // 一次网络往返一致：同步 resolve 会让「先查后装」和「先装后查」分辨不出来。
      checkStatus: () => {
        calls += 1;
        const answer = authorizedAnswers[calls - 1] ?? false;
        return new Promise((resolve) => setTimeout(() => resolve(answer), 0));
      },
      installTimer: (tick) => { const id = nextId++; timers.set(id, tick); return id; },
      clearTimer: (id) => { timers.delete(id); },
      onSettled: (authorized) => settledWith.push(authorized),
    },
    fire: () => { for (const tick of [...timers.values()]) tick(); },
  };
}
const settle = () => new Promise((r) => setTimeout(r, 0));

test('已授权：只查一次，收尾为成功，绝不留下定时器', async () => {
  // 这一条同时是旧顺序（先装定时器、再等第一次检查）的反例：那种写法里定时器
  // 在检查回来之前就已装上，收尾时若不在回调里清掉，它就会一直留在表里。
  const f0 = fakePollEnv([true]);
  pollers.runCloudflareAuthPoll(f0.env);
  assert.equal(f0.timers.size, 0, '第一次检查还没回来就装上了定时器：收尾那条路上没人清它');
  await settle();
  const f = fakePollEnv([true]);
  pollers.runCloudflareAuthPoll(f.env);
  await settle();
  assert.equal(f.calls(), 1);
  assert.deepEqual(f.settledWith, [true]);
  assert.equal(f.timers.size, 0, '已授权后不应安装定时器');
  // 即便旧实现装上过一只，收尾后的每一次跳也必须没有任何请求跟出来。
  f.fire();
  f.fire();
  await settle();
  assert.equal(f.calls(), 1, '收尾后定时器仍在校验后端');
});

test('未授权：安装定时器并继续查，授权后停止', async () => {
  const f = fakePollEnv([false, false, true]);
  pollers.runCloudflareAuthPoll(f.env);
  await settle();
  assert.equal(f.timers.size, 1, '未授权时应安装定时器');
  f.fire();
  await settle();
  assert.equal(f.timers.size, 1);
  f.fire();
  await settle();
  assert.equal(f.calls(), 3);
  assert.deepEqual(f.settledWith, [true]);
  assert.equal(f.timers.size, 0, '授权后定时器必须停掉');
});

test('检查还在飞时不叠加第二次，避免慢查询堆积请求', async () => {
  const timers = new Map();
  let nextId = 1;
  let calls = 0;
  let release;
  const gate = new Promise((r) => { release = r; });
  pollers.runCloudflareAuthPoll({
    timeoutMs: 600000,
    now: () => 0,
    checkStatus: () => { calls += 1; return gate.then(() => false); },
    installTimer: (tick) => { const id = nextId++; timers.set(id, tick); return id; },
    clearTimer: (id) => timers.delete(id),
    onSettled: () => {},
  });
  await settle();
  // 第一次还没回来，定时器已经装上：此刻再触发两次，不应再发请求
  for (const tick of [...timers.values()]) tick();
  for (const tick of [...timers.values()]) tick();
  await settle();
  assert.equal(calls, 1, '正在飞的那次没回来之前不应再发请求');
  release();
  await settle();
});

test('超时收尾为失败，并停掉定时器', async () => {
  const timers = new Map();
  let nextId = 1;
  let now = 0;
  const settled = [];
  pollers.runCloudflareAuthPoll({
    timeoutMs: 1000,
    now: () => now,
    checkStatus: async () => false,
    installTimer: (tick) => { const id = nextId++; timers.set(id, tick); return id; },
    clearTimer: (id) => timers.delete(id),
    onSettled: (authorized) => settled.push(authorized),
  });
  await settle();
  now = 1001;
  for (const tick of [...timers.values()]) tick();
  await settle();
  assert.deepEqual(settled, [false]);
  assert.equal(timers.size, 0);
});

test('untilLabel 向下取整：说的剩余时间不会比实际多', () => {
  const now = new Date('2026-10-26T12:00:00Z');
  const at = (minutes) => new Date(now.getTime() + minutes * 60_000);
  assert.equal(lib.untilLabel(at(91), now), '1 小时 31 分钟');
  assert.equal(lib.untilLabel(at(90), now), '1 小时 30 分钟');
  assert.equal(lib.untilLabel(at(60), now), '1 小时');
  assert.equal(lib.untilLabel(at(35 * 60), now), '35 小时');
  assert.equal(lib.untilLabel(at(0.5), now), '不到 1 分钟');
  // 1 小时 31 分不能再显示成「2 小时后」。
  assert.equal(lib.untilLabel(at(49 * 60), now), '2 天');
});

test('resetHint 把向下取整的剩余时间接在「后」前', () => {
  const now = new Date('2026-10-26T12:00:00Z');
  const hint = lib.resetHint(new Date(now.getTime() + 91 * 60_000).toISOString(), now);
  assert.ok(hint && hint.text.includes('1 小时 31 分钟后'), hint && hint.text);
});

// ── Provider 额度面板的轮询调度（usage_poll.ts）────────────────────────────
const MIN = 3 * 60_000;
const pollBase = { fetched: true, lastFinishedAt: 1_000_000, now: 1_000_000 + MIN, active: true, modalOpen: false, pending: false, intervalMs: MIN };

test('额度轮询：到了间隔就发', () => {
  assert.equal(usagePoll.shouldPollUsage(pollBase), 'fetch');
  assert.equal(usagePoll.shouldPollUsage({...pollBase, now: 1_000_000 + MIN + 1}), 'fetch');
});

test('额度轮询：间隔没到不发，在飞不叠发', () => {
  assert.equal(usagePoll.shouldPollUsage({...pollBase, now: 1_000_000 + MIN - 1}), 'skip-not-due');
  assert.equal(usagePoll.shouldPollUsage({...pollBase, pending: true}), 'skip-pending');
});

test('额度轮询：弹窗压着时整轮安静——包括还没有数据的账号', () => {
  // 早先的写法里「没有数据就立刻取」排在可见性判断前面，编辑 Provider 的弹窗
  // 开着时背后的卡片照打厂商额度接口。
  assert.equal(usagePoll.shouldPollUsage({...pollBase, modalOpen: true}), 'skip-modal');
  assert.equal(usagePoll.shouldPollUsage({...pollBase, modalOpen: true, fetched: false, lastFinishedAt: null}), 'skip-modal');
});

test('额度轮询：不在前台不发；没有数据（首次）不等间隔', () => {
  assert.equal(usagePoll.shouldPollUsage({...pollBase, active: false}), 'skip-hidden');
  assert.equal(usagePoll.shouldPollUsage({...pollBase, fetched: false, lastFinishedAt: null}), 'fetch');
  // 弹窗关掉以后，还没有数据的账号下一次跳就取。
  assert.equal(usagePoll.shouldPollUsage({...pollBase, fetched: false, lastFinishedAt: null, modalOpen: false, active: true}), 'fetch');
});

test('额度轮询排期：按上次结案时刻 + 间隔，不因重排白等一格', () => {
  assert.equal(usagePoll.usagePollDelay(1_000_000, null, MIN), 0, '没有数据时立刻排上');
  assert.equal(usagePoll.usagePollDelay(1_000_000, 1_000_000, MIN), MIN);
  // 弹窗期间跳过的一格：定时器重排时已过间隔，下一次跳立刻到期。
  assert.equal(usagePoll.usagePollDelay(1_000_000 + MIN + 5, 1_000_000, MIN), 0);
});

test('额度请求键：路径 + 非空查询，顺序稳定', () => {
  assert.equal(usagePoll.usageRequestKey('/__providers/a/claude-oauth/usage'), '/__providers/a/claude-oauth/usage');
  assert.equal(usagePoll.usageRequestKey('/p', {}), '/p');
  assert.equal(usagePoll.usageRequestKey('/p', {account: 'x', skip: undefined, empty: ''}), '/p?account=x');
  assert.equal(usagePoll.usageRequestKey('/p?x=1', {account: 'x'}), '/p?x=1&account=x');
});

// ── 输入 Provider 列表的排序（usage_poll.ts）──────────────────────────────
test('Provider 排序：首次绘制排一次，之后轮询不再重排，显式刷新才重排', () => {
  assert.equal(usagePoll.providerRankOrder({previous: null, ready: false, force: false}), 'wait');
  assert.equal(usagePoll.providerRankOrder({previous: null, ready: true, force: false}), 'rank');
  // 进过这一页、已经排好：统计每次轮询回来都只是数字变了，顺序保持。
  assert.equal(usagePoll.providerRankOrder({previous: ['a', 'b'], ready: true, force: false}), 'keep');
  // 点「获取模型」：按最新计数再排一次。
  assert.equal(usagePoll.providerRankOrder({previous: ['a', 'b'], ready: true, force: true}), 'rank');
});

test('Provider 排序：按计数降序、同数按名字', () => {
  const counts = {a: 3, b: 10, c: 3};
  const names = {a: '乙', b: '甲', c: '丙'};
  assert.deepEqual(usagePoll.rankProviderIDs(['a', 'b', 'c'], (id) => counts[id] ?? 0, (id) => names[id]), ['b', 'c', 'a']);
  assert.deepEqual(usagePoll.rankProviderIDs([], () => 0, (id) => id), []);
});

// ── 服务器时区的「今日」（lib.tsx）────────────────────────────────────────
test('localDateToRFC3339：给了网关时区就按那个时区补偏移', () => {
  assert.equal(lib.localDateToRFC3339('2026-10-26', false, 'Asia/Shanghai'), '2026-10-26T00:00:00+08:00');
  assert.equal(lib.localDateToRFC3339('2026-10-26', true, 'Asia/Shanghai'), '2026-10-26T23:59:59.999+08:00');
  assert.equal(lib.localDateToRFC3339('2026-10-26', false, 'America/Los_Angeles'), '2026-10-26T00:00:00-07:00');
  assert.equal(lib.localDateToRFC3339('not-a-date', false, 'Asia/Shanghai'), 'not-a-date');
});

test('zoneOffsetMinutesAt：按 IANA 名求偏移，未知时区退回 null', () => {
  assert.equal(lib.zoneOffsetMinutesAt(new Date('2026-01-15T00:00:00Z'), 'Asia/Shanghai'), 480);
  assert.equal(lib.zoneOffsetMinutesAt(new Date('2026-01-15T00:00:00Z'), 'America/Los_Angeles'), -480);
  assert.equal(lib.zoneOffsetMinutesAt(new Date('2026-07-15T00:00:00Z'), 'America/Los_Angeles'), -420);
  assert.equal(lib.zoneOffsetMinutesAt(new Date('2026-07-15T00:00:00Z'), 'Asia/Kolkata'), 330);
  assert.equal(lib.zoneOffsetMinutesAt(new Date('2026-07-15T00:00:00Z'), ''), null);
  assert.equal(lib.zoneOffsetMinutesAt(new Date('2026-07-15T00:00:00Z'), 'Not/AZone'), null);
});

test('serverDayBounds：「今日」取服务器那一天，而不是浏览器那一天', () => {
  // 北京时间 10 月 27 日 09:00，此刻洛杉矶还是 10 月 26 日。
  const now = new Date('2026-10-27T01:00:00Z');
  assert.equal(lib.formatDateInZone(now, 'Asia/Shanghai'), '2026-10-27');
  assert.equal(lib.formatDateInZone(now, 'America/Los_Angeles'), '2026-10-26');
  assert.deepEqual(lib.serverDayBounds(now, 'America/Los_Angeles'), {
    from: '2026-10-26T00:00:00-07:00',
    to: '2026-10-26T23:59:59.999-07:00',
  });
  assert.deepEqual(lib.serverDayBounds(now, 'Asia/Shanghai'), {
    from: '2026-10-27T00:00:00+08:00',
    to: '2026-10-27T23:59:59.999+08:00',
  });
  // 网关不知道自己的时区（老版本）时退回浏览器本地日期。
  assert.equal(lib.formatDateInZone(now, ''), lib.formatLocalISODate(now));
  assert.equal(lib.formatDateInZone(now, 'Not/AZone'), lib.formatLocalISODate(now));
});

// ── 客户端调用示例（lib.tsx buildApiKeyPublicCurl）──────────────────────────
const geminiKey = {id: 'k1', name: 'gem', key: 'sk-x', routeId: 'r1'};
const geminiRoute = {id: 'r1', name: 'g', outputEndpointId: 'e1', providerId: 'p1', outputProtocol: 'gemini', mode: 'convert', enabled: true};
const geminiEndpoints = [{id: 'e1', name: 'Gemini', protocol: 'gemini', basePath: '/v1beta', listenHost: '127.0.0.1', listenPort: 18093, publicAccessEnabled: false}];

test('Gemini 密钥的示例 curl 用 x-goog-api-key（Google SDK 的认证头），不发 Bearer', () => {
  const out = lib.buildApiKeyPublicCurl(geminiKey, geminiRoute, geminiEndpoints, '', {id: 'p1', name: 'p', protocol: 'gemini', baseUrl: 'https://x', models: [], defaultModel: 'gemini-2.5-pro'});
  assert.ok(out.includes("-H 'x-goog-api-key: sk-x'"), out);
  assert.ok(!out.includes('Authorization'), out);
});

test('Gemini 示例不把 {model} 原样交出去：URL 与请求体都换成模型名', () => {
  const out = lib.buildApiKeyPublicCurl(geminiKey, geminiRoute, geminiEndpoints, '', {id: 'p1', name: 'p', protocol: 'gemini', baseUrl: 'https://x', models: [], defaultModel: 'gemini-2.5-pro'});
  assert.ok(!out.includes('{model}'), out);
  assert.ok(out.includes('/v1beta/models/gemini-2.5-pro:generateContent'), out);
  assert.ok(out.includes('"gemini-2.5-pro"'), out);
  // 模型是示例占位时要明说，别让用户以为写死了就能跑。
  const placeholder = lib.buildApiKeyPublicCurl(geminiKey, geminiRoute, geminiEndpoints, '', undefined);
  assert.ok(!placeholder.includes('{model}'), placeholder);
  assert.ok(placeholder.includes('占位'), placeholder);
});

test('Claude 密钥用 x-api-key，OpenAI 密钥用 Bearer', () => {
  const endpoints = [{id: 'e2', name: 'Claude', protocol: 'claude', basePath: '/anthropic', listenHost: '127.0.0.1', listenPort: 18093, publicAccessEnabled: false}, geminiEndpoints[0]];
  const claudeRoute = {...geminiRoute, outputProtocol: 'claude'};
  const claude = lib.buildApiKeyPublicCurl(geminiKey, claudeRoute, endpoints, '', {id: 'p1', name: 'p', protocol: 'claude', baseUrl: 'https://x'});
  assert.ok(claude.includes("-H 'x-api-key: sk-x'"), claude);
  const openai = lib.buildApiKeyPublicCurl(geminiKey, {...geminiRoute, outputProtocol: 'openai_chat'}, endpoints, '', {id: 'p1', name: 'p', protocol: 'openai_chat', baseUrl: 'https://x', defaultModel: 'gpt-x'});
  assert.ok(openai.includes("-H 'Authorization: Bearer sk-x'"), openai);
});

// ── 登录错误信封（lib.tsx errorMessageFromBody / authRejectionMessage）──────
test('errorMessageFromBody：从控制台错误信封里取人能读的那句话', () => {
  assert.equal(lib.errorMessageFromBody(JSON.stringify({error: {message: 'invalid username or password', type: 'gateway_error'}})), 'invalid username or password');
  assert.equal(lib.errorMessageFromBody(JSON.stringify({error: 'too many attempts'})), 'too many attempts');
  assert.equal(lib.errorMessageFromBody(JSON.stringify({message: '密码至少 8 位'})), '密码至少 8 位');
  assert.equal(lib.errorMessageFromBody('Bad Gateway', '登录失败'), 'Bad Gateway');
  assert.equal(lib.errorMessageFromBody('', '登录失败'), '登录失败');
  assert.equal(lib.errorMessageFromBody('{}', '登录失败'), '登录失败');
});

test('authRejectionMessage：401 不再是原样的 JSON', async () => {
  const body = JSON.stringify({error: {message: 'invalid username or password', type: 'gateway_error'}});
  const response = new Response(body, {status: 401, headers: {'content-type': 'application/json'}});
  const message = await lib.authRejectionMessage(response, '登录失败');
  assert.equal(message, 'invalid username or password');
  assert.ok(!message.includes('{'), message);
  const empty = await lib.authRejectionMessage(new Response('', {status: 403}), '登录失败');
  assert.equal(empty, '登录失败（HTTP 403）');
});

// ── 对话测试的失败提示（lib.tsx chatTestVerdictFromBody / chatTestToastText）──
test('失败应答的错误信封：提示里是那句话，不是 undefined', () => {
  // 普通用户在管理员未授权的路由上点「对话测试」：403 + 错误信封。
  const verdict = lib.chatTestVerdictFromBody(JSON.stringify({error: {message: 'permission denied', type: 'gateway_error'}}), 403);
  assert.equal(verdict.success, false);
  assert.equal(verdict.error, 'permission denied');
  const toast = lib.chatTestToastText(verdict);
  assert.equal(toast, '对话测试未通过：permission denied');
  assert.ok(!toast.includes('undefined'), toast);
});

test('正常应答按 success 判定，成功提示带模型侧状态码', () => {
  const ok = lib.chatTestVerdictFromBody(JSON.stringify({success: true, status: 200, routeId: 'r1', error: ''}), 200);
  assert.equal(ok.success, true);
  assert.equal(lib.chatTestToastText(ok), '对话测试成功：HTTP 200');
  const failed = lib.chatTestVerdictFromBody(JSON.stringify({success: false, status: 502, error: 'upstream exploded'}), 502);
  assert.equal(lib.chatTestToastText(failed), '对话测试未通过：upstream exploded');
});

test('解析不了的应答退回状态码，同样不出现 undefined', () => {
  const plain = lib.chatTestVerdictFromBody('<html>Bad Gateway</html>', 502);
  assert.ok(lib.chatTestToastText(plain).includes('502'), lib.chatTestToastText(plain));
  const empty = lib.chatTestVerdictFromBody('', 401);
  assert.equal(lib.chatTestToastText(empty), '对话测试未通过：HTTP 401');
  const emptyEnvelope = lib.chatTestVerdictFromBody('{}', 500);
  assert.equal(lib.chatTestToastText(emptyEnvelope), '对话测试未通过：HTTP 500');
});

test('轮询驱动：检查抛错时继续轮询，不会卡在「等待授权」', async () => {
  let timerInstalled = 0;
  let calls = 0;
  let settledWith = null;
  const poll = pollers.runCloudflareAuthPoll({
    timeoutMs: 600000,
    now: () => 0,
    checkStatus: () => {
      calls += 1;
      return calls === 1 ? Promise.reject(new Error('network down')) : Promise.resolve(false);
    },
    installTimer: () => {
      timerInstalled += 1;
      return timerInstalled;
    },
    clearTimer: () => {},
    onSettled: (authorized) => {
      settledWith = authorized;
    },
  });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(timerInstalled, 1, '一次检查抛错后仍要装上定时器（否则停在等待授权）');
  assert.equal(settledWith, null, '抛错不能当作授权成功或失败');
  poll.stop();
});

// 「设为首选」是交换，不是删除：只改 routeId 时后端会把新首选从备选里剔掉、
// 旧首选又不补回来，于是每点一次节点就少一个（R39 用户报告）。
test('promoteApiKeyProvider：首选与备选交换，位次守恒，其余顺序不变', () => {
  const swap = lib.promoteApiKeyProvider(
    'b',
    {providerIds: ['b', 'c', 'd'], modelOverrides: {b: 'm-b', d: 'm-d'}},
    'a',
  );
  assert.equal(swap.providerId, 'b', '被提升的 Provider 成为首选');
  assert.deepEqual(swap.fallbackProviderIds, ['a', 'c', 'd'], '旧首选回到被提升者原来的位次，节点总数不变');
  assert.deepEqual(swap.fallbackModelOverrides, {d: 'm-d'}, '新首选的备选固定模型不再留在备选里');
});

test('promoteApiKeyProvider：旧首选带着自己的固定模型进入备选', () => {
  const swap = lib.promoteApiKeyProvider(
    'b',
    {providerIds: ['b', 'c'], modelOverrides: {a: 'm-a', b: 'm-b'}},
    'a',
  );
  assert.deepEqual(swap.fallbackProviderIds, ['a', 'c']);
  assert.equal(swap.fallbackModelOverrides.a, 'm-a', '旧首选锁定的模型要跟着它搬进备选条目');
  assert.equal(swap.fallbackModelOverrides.b, undefined, '新首选的固定模型交给首选侧');
});

test('promoteApiKeyProvider：没有旧首选时只移除自己，不凭空塞节点', () => {
  const swap = lib.promoteApiKeyProvider('b', {providerIds: ['b', 'c'], modelOverrides: {}}, '');
  assert.deepEqual(swap.fallbackProviderIds, ['c']);
  assert.deepEqual(swap.fallbackModelOverrides, {});
});

test('promoteApiKeyProvider：不是备选就原样交回，别拆掉别人的链', () => {
  const swap = lib.promoteApiKeyProvider('z', {providerIds: ['b', 'c'], modelOverrides: {c: 'm'}}, 'a');
  assert.equal(swap.providerId, 'z');
  assert.deepEqual(swap.fallbackProviderIds, ['b', 'c']);
  assert.deepEqual(swap.fallbackModelOverrides, {c: 'm'});
});

// 双栏选择器的列表操作（R39 用户报告「配置备选界面很难用」后的重做）。
test('fallbackAdd：追加到链尾，重复与已存在的跳过', () => {
  assert.deepEqual(lib.fallbackAdd(['a'], ['b', 'c']), ['a', 'b', 'c'], '新加的一律排在末尾，不改已有位次');
  assert.deepEqual(lib.fallbackAdd(['a', 'b'], ['b', 'c']), ['a', 'b', 'c'], '已在链上的不重复加');
  assert.deepEqual(lib.fallbackAdd(['a'], []), ['a']);
  assert.deepEqual(lib.fallbackAdd([], ['x', 'x', '']), ['x'], '空 id 与批内重复都要挡掉');
});

test('fallbackRemove：批量移除，其余顺序不变', () => {
  assert.deepEqual(lib.fallbackRemove(['a', 'b', 'c', 'd'], ['b', 'd']), ['a', 'c']);
  assert.deepEqual(lib.fallbackRemove(['a'], ['z']), ['a'], '不在链上的 id 不影响结果');
  const ids = ['a', 'b'];
  assert.equal(lib.fallbackRemove(ids, []), ids, '没有要移除的直接返回原引用，调用方可据此跳过保存');
});

test('fallbackMove：上移/下移与拖到指定行前后', () => {
  assert.deepEqual(lib.fallbackMove(['a', 'b', 'c'], 'c', 'a', false), ['c', 'a', 'b'], '拖到首行之前');
  assert.deepEqual(lib.fallbackMove(['a', 'b', 'c'], 'a', 'c', true), ['b', 'c', 'a'], '拖到末行之后');
  assert.deepEqual(lib.fallbackMove(['a', 'b', 'c'], 'b', 'c', true), ['a', 'c', 'b'], '下移一位');
  const ids = ['a', 'b', 'c'];
  assert.equal(lib.fallbackMove(ids, 'a', 'b', false), ids, '位置没变返回原引用，避免无谓保存');
  assert.equal(lib.fallbackMove(ids, 'z', 'a', false), ids, '不在链上的拖动一律忽略');
});

test('providerMatchesQuery：空格分词按 AND 收窄，账号也能搜到', () => {
  const provider = {
    id: 'prov-1', name: 'Qoder CN 账号-dengmluan', protocol: 'openai_chat', authType: 'qoder_cn',
    qoderPat: {accountLabel: '邓丽霞', email: 'deng@example.com'},
  };
  assert.equal(lib.providerMatchesQuery(provider, ''), true);
  assert.equal(lib.providerMatchesQuery(provider, 'qoder'), true, '名字命中');
  assert.equal(lib.providerMatchesQuery(provider, '邓丽霞'), true, '账号命中');
  assert.equal(lib.providerMatchesQuery(provider, 'qoder 邓丽霞'), true, '多个词按 AND 收窄');
  assert.equal(lib.providerMatchesQuery(provider, 'qoder workbuddy'), false, '任一词不命中就过滤掉');
});

test('上游网关成员行：按所属上游分组，行内只写成员自己的名字', () => {
  const deepseek = {
    id: 'sub-gw-ds', name: 'vps1 · DeepSeek Chat', protocol: 'openai_chat', authType: 'peer',
    baseUrl: 'https://parent.example',
    peerGrants: {grantedProviderIds: ['ds'], upstreamKey: 'gw_parent|https://parent.example', upstreamName: 'vps1', grantedProviderId: 'ds'},
  };
  const qwen = {
    id: 'sub-gw-qw', name: 'vps1 · Qwen Max', protocol: 'openai_chat', authType: 'peer',
    baseUrl: 'https://parent.example',
    peerGrants: {grantedProviderIds: ['ds'], upstreamKey: 'gw_parent|https://parent.example', upstreamName: 'vps1', grantedProviderId: 'qw'},
  };
  // 同一次注册落下来的多条成员行共用一个组：标题是上游名，不是成员名。
  assert.equal(lib.providerGroupLabel(deepseek), lib.providerGroupLabel(qwen));
  assert.equal(lib.peerUpstreamName(deepseek), 'vps1');
  assert.equal(lib.peerProviderMemberName(deepseek), 'DeepSeek Chat', '名字里的上游前缀在分组内是重复信息');
  // 账号位：认人用成员名，不是那条对整组都一样的上游名。
  assert.equal(lib.providerAccountLabel(deepseek), 'vps1');
  assert.equal(lib.providerShortLabel(deepseek), 'DeepSeek Chat');
  assert.equal(lib.providerDisplayLabel(deepseek), 'vps1 · DeepSeek Chat', '完整署名不再重复上游名');
  assert.deepEqual(lib.providerUsageParts('sub-gw-ds', [deepseek, qwen]), {label: 'DeepSeek Chat', sub: 'vps1'});

  // 升级前的老记录：没有 upstreamKey / upstreamName，各自成组，名字原样保留。
  const legacy = {id: 'peer-old', name: '老上游', protocol: 'openai_chat', authType: 'peer', baseUrl: 'https://old.example'};
  assert.equal(lib.peerUpstreamName(legacy), '老上游');
  assert.equal(lib.peerProviderMemberName(legacy), '老上游');
  assert.equal(lib.providerGroupLabel(legacy), lib.providerGroupLabel(legacy), '同一行永远落在同一组');
  assert.notEqual(lib.providerGroupLabel(legacy), lib.providerGroupLabel(deepseek));
});

test('retargetSelfRegisterBaseURL：占位地址跟着协议走，真实隧道地址一个字不动', () => {
  // 还没被脚本注册过：占位主机的地址要跟着协议换成对应的路径，
  // 否则表单会带着一个跟协议对不上的占位值提交。
  assert.equal(
    lib.retargetSelfRegisterBaseURL('https://pending-self-registration.example/v1/chat/completions', 'claude'),
    'https://pending-self-registration.example/v1/messages',
  );
  assert.equal(
    lib.retargetSelfRegisterBaseURL('https://pending-self-registration.example/v1/messages', 'openai_responses'),
    'https://pending-self-registration.example/v1/responses',
  );
  // 脚本已经回填过真实地址：改协议只是改网关对上游的预期，地址原样保留
  // （真路径要等对方脚本下次注册时自己声明，这里拼一个只会把能用的地址改坏）。
  const registered = 'https://quiet-river-1234.trycloudflare.com/v1/messages';
  assert.equal(lib.retargetSelfRegisterBaseURL(registered, 'openai_chat'), registered);
  // 克隆出来的空值：给新协议的占位地址。
  assert.equal(
    lib.retargetSelfRegisterBaseURL('', 'claude'),
    'https://pending-self-registration.example/v1/messages',
  );
  // 解析不出主机的脏值按占位处理，不把脏值当真实地址留住。
  assert.equal(
    lib.retargetSelfRegisterBaseURL('not a url', 'claude'),
    'https://pending-self-registration.example/v1/messages',
  );
});

test('buildSelfRegistrationPrompt：以控制台选中的协议为主，不再让脚本四选一', () => {
  const provider = {id: 'prov-sr', name: '内网穿透自助注册-x', protocol: 'claude'};
  const text = lib.buildSelfRegistrationPrompt(provider, 'sr_token', 'https://gw.example.com');
  assert.ok(text.includes('POST /v1/messages'), '主协议路径要出现');
  // 别的协议只能出现在「手上已有现成实现」的备选清单里，主协议段落必须只讲 Claude。
  const primary = text.slice(0, text.indexOf('接口路径'));
  assert.ok(!primary.includes('openai_chat'), '主协议段不该提别的协议');
  assert.ok(text.includes('protocol="openai_chat"'), '其它协议作为备选列出，但注明要回控制台改协议');
  assert.ok(text.includes('x-api-key: <SHARED_SECRET>'), 'Claude 的鉴权头要按惯例推导');
  assert.ok(text.includes('"protocol": "claude"'), '注册示例带上控制台选的那个协议');
  assert.ok(text.includes('原样透传'), '要说明网关不做协议转换');

  const chat = lib.buildSelfRegistrationPrompt({...provider, protocol: 'openai_chat'}, '', 'https://gw.example.com');
  assert.ok(chat.includes('POST /v1/chat/completions'));
  assert.ok(chat.includes('Authorization: Bearer <SHARED_SECRET>'));
  assert.ok(chat.includes('生成注册令牌'), '没有原始令牌时用占位提示，不编一个');
});

test('Claude Code 一键接入：模型钉死 default，四个档位与子 agent 全部跟随', () => {
  const env = lib.buildClaudeCodeSwitcherEnv({baseURL: 'https://gw.example.com/anthropic', token: 'sk-gw-x'});
  assert.equal(env.ANTHROPIC_MODEL, 'default[1m]', '默认要 1M 变体（客户端按 1M 算窗口）');
  // 内置子 agent 的模型是硬编码的（claude-code-guide→haiku、statusline-setup→sonnet），
  // 只钉 ANTHROPIC_MODEL 它们仍会带着 haiku/sonnet 打上来。
  for (const tier of ['ANTHROPIC_DEFAULT_OPUS_MODEL', 'ANTHROPIC_DEFAULT_SONNET_MODEL', 'ANTHROPIC_DEFAULT_FABLE_MODEL']) {
    assert.equal(env[tier], 'default[1m]', `${tier} 必须一起钉死`);
  }
  assert.equal(env.ANTHROPIC_DEFAULT_HAIKU_MODEL, 'default', 'haiku 档不带 1M：只做摘要分类');
  assert.equal(env.ANTHROPIC_SMALL_FAST_MODEL, 'default');
  assert.equal(env.CLAUDE_CODE_SUBAGENT_MODEL, 'default', '自定义子 agent 也要跟随');
  assert.equal(env.ANTHROPIC_BASE_URL, 'https://gw.example.com/anthropic');
  assert.equal(env.ANTHROPIC_AUTH_TOKEN, 'sk-gw-x');

  // 关掉 1M 时所有档位都退回不带后缀的 default。
  const plain = lib.buildClaudeCodeSwitcherEnv({baseURL: 'https://x/anthropic', token: 't', oneMillionContext: false});
  assert.equal(plain.ANTHROPIC_MODEL, 'default');
  assert.equal(plain.ANTHROPIC_DEFAULT_OPUS_MODEL, 'default');
});

test('Claude Code 一键接入脚本：只动 env、先备份、不覆盖用户自己选的 model', () => {
  const key = {id: 'k1', name: 'k', key: 'sk-gw-x', enabled: true};
  const endpoints = [{id: 'e2', name: 'Claude', protocol: 'claude', basePath: '/anthropic', listenHost: '127.0.0.1', listenPort: 18093, publicAccessEnabled: false}];

  const text = lib.buildClaudeCodeSwitcherConfig(key, endpoints, '');
  const parsed = JSON.parse(text);
  assert.equal(parsed.env.ANTHROPIC_MODEL, 'default[1m]');
  assert.equal(parsed.model, 'default[1m]', '顶层 model 同步，/config 里显示的就是它');

  const script = lib.buildClaudeCodeSwitcherScript(key, endpoints, '');
  assert.ok(script.includes('.claude" / "settings.json'), script);
  assert.ok(script.includes('_backup(FILE)'), '必须先备份');
  assert.ok(script.includes('setdefault("model"'), '用户自己在 /config 里选的模型不能被覆盖');
  assert.ok(script.includes('重开一次'), '要提示 env 只在启动时读取');
  const b64 = script.match(/_b64\("([A-Za-z0-9+/=]+)"\)/);
  assert.ok(b64, '配置正文要 base64 内嵌，避免引号坑');
  const embedded = JSON.parse(Buffer.from(b64[1], 'base64').toString('utf8'));
  assert.equal(embedded.env.CLAUDE_CODE_SUBAGENT_MODEL, 'default');
});

test('写进本机配置的网关地址：回环 http 或公网 https，绝不写内网 http', () => {
  // 两个理由叠在一起：
  //   1. 本机配置指向的是跑网关的这台机器，网卡 IP 会随 Wi-Fi 切换 / DHCP 续租
  //      而变，写死进去过几天就不通了；127.0.0.1 永远通。
  //   2. Desktop 的 3p 托管配置校验是 "must use https (or http on loopback)"，
  //      写内网 http 进去不会当场报错，它会在启动时丢掉整条配置并说
  //      「Couldn't load your organization's model list」。
  const lan = [{id: 'e1', protocol: 'openai_chat', basePath: '/v1', listenHost: '192.168.1.10', listenPort: 18093}];
  assert.equal(lib.localAgentGatewayRoot(lan, ''), 'http://127.0.0.1:18093', '没有公网地址时退回回环，不能写内网 IP');

  const loopback = [{id: 'e1', protocol: 'openai_chat', basePath: '/v1', listenHost: '127.0.0.1', listenPort: 18093}];
  assert.equal(lib.localAgentGatewayRoot(loopback, ''), 'http://127.0.0.1:18093');

  // 公网 https 优先。
  assert.equal(lib.localAgentGatewayRoot(lan, 'https://gw.example.com'), 'https://gw.example.com');
  assert.equal(lib.localAgentGatewayRoot(lan, 'https://gw.example.com/'), 'https://gw.example.com', '去掉尾部斜杠');

  // 公网但只有 http：Desktop 不接受，仍退回回环，而不是把一个会被拒的地址写进去。
  assert.equal(lib.localAgentGatewayRoot(lan, 'http://gw.example.com'), 'http://127.0.0.1:18093');
});

// 运维手动设的上下文窗口是「对外宣称的窗口」：模型页的数字、1M 标签、长上下文
// 筛选和跨账号合并都得按它走。按自动值（contextLength）判的话，一个手动标成
// 1M、底下的自动值还是 128K 的模型在界面上完全看不出被设过，Claude Desktop
// 那边却已经按 1M 列出了它。
test('declaredModelWindow: 设过手动窗口就以它为准', () => {
  const plain = {id: 'm1', providerId: 'p1', protocol: 'openai_chat', contextLength: 128000, inMenu: true};
  assert.equal(lib.declaredModelWindow(plain), 128000);
  assert.equal(lib.modelContextLabel(plain), '', '128K 不该出 1M 标签');

  const pinned = {...plain, contextOverride: 1000000};
  assert.equal(lib.declaredModelWindow(pinned), 1000000, '手动值压过自动值');
  assert.equal(lib.modelContextLabel(pinned), '1M', '手动标成 1M 就要出 1M 标签');

  // 名字里已经写了 M 的不再重复标（沿用既有规则）。
  assert.equal(lib.modelContextLabel({...pinned, id: 'gpt-5.5-1m'}), '');
  // 清空手动值后回到自动值。
  assert.equal(lib.declaredModelWindow({...pinned, contextOverride: 0}), 128000);
});

test('mergeModelsAcrossAccounts: 同名模型按宣称窗口取大的那个', () => {
  const models = [
    // 先出现的这个自动值是 128K，但运维手动把它标成了 1M。
    {id: 'm1', providerId: 'p1', protocol: 'openai_chat', contextLength: 128000, contextOverride: 1000000, inMenu: true},
    {id: 'm1', providerId: 'p2', protocol: 'openai_chat', contextLength: 200000, inMenu: true},
  ];
  const providers = new Map([
    ['p1', {id: 'p1', name: 'P1'}],
    ['p2', {id: 'p2', name: 'P2'}],
  ]);
  const merged = modelMerge.mergeModelsAcrossAccounts(models, providers);
  assert.equal(merged.length, 1);
  assert.equal(lib.declaredModelWindow(merged[0]), 1000000, '手动设的 1M 不能被 200K 的账号盖掉');
  assert.equal(merged[0].providers.length, 2);

  // 后出现的账号窗口更大：整组值一起搬过去，不能只搬 contextLength 而
  // 把 contextOverride 留在原地（那会变成「自动值 1M、手动值 128K」）。
  const swapped = modelMerge.mergeModelsAcrossAccounts(
    [
      {id: 'm2', providerId: 'p1', protocol: 'openai_chat', contextLength: 128000, contextOverride: 200000, inMenu: true},
      {id: 'm2', providerId: 'p2', protocol: 'openai_chat', contextLength: 900000, inMenu: true},
    ],
    providers,
  );
  assert.equal(swapped.length, 1);
  assert.equal(swapped[0].contextLength, 900000);
  assert.equal(swapped[0].contextOverride, 0, '换过来的账号没有手动值，不能留着上一个的');
});
