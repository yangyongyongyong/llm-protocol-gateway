// dialogs.tsx 的回归测试：桌面 App 里 window.confirm 恒为 false、window.alert
// 被静默丢弃（Wails 的 WKUIDelegate 没实现对应方法），删除类按钮因此变成
// 「点了没反应」。这里锁的是「确认框不依赖原生对话框」这件事本身：
//
//   node --test web/src/dialogs.test.mjs
//
// 做法与 lib.test.mjs 一致：把真实的 dialogs.tsx 用 tsc 编成 ESM 再调用。为了
// 不把 React 和整个 UI 拖进来，react / react-dom / ./components/ui 都用测试替身：
// 替身把「渲染出来的树」记下来，测试直接读这棵树判断按钮文案与危险态。
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {mkdtempSync, mkdirSync, writeFileSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {dirname, join} from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const webDir = dirname(here);
const tsc = join(webDir, 'node_modules', 'typescript', 'bin', 'tsc');
const outDir = mkdtempSync(join(tmpdir(), 'gw-dialogs-test-'));
process.on('exit', () => rmSync(outDir, {recursive: true, force: true}));

writeFileSync(join(outDir, 'package.json'), JSON.stringify({type: 'module'}));

// react 替身：DialogHost 只用到 useState / useEffect / React.createElement 的
// 结果，测试不渲染，只要不炸即可。
const reactDir = join(outDir, 'node_modules', 'react');
mkdirSync(reactDir, {recursive: true});
writeFileSync(join(reactDir, 'package.json'), JSON.stringify({
  name: 'react', type: 'module', main: 'index.js',
  exports: {'.': './index.js', './jsx-runtime': './jsx-runtime.js', './jsx-dev-runtime': './jsx-runtime.js'},
}));
writeFileSync(join(reactDir, 'jsx-runtime.js'), `
export const Fragment = (props) => props.children;
export const jsx = (type, props) => ({type, props});
export const jsxs = jsx;
export const jsxDEV = jsx;
`);
writeFileSync(join(reactDir, 'index.js'), `
export const createElement = (type, props, ...children) => ({type, props: {...(props || {}), children}});
export const useState = (value) => [typeof value === 'function' ? value() : value, () => {}];
export const useEffect = () => {};
export const useRef = (value) => ({current: value});
export default {createElement, useState, useEffect, useRef};
`);

// react-dom/client 替身：dialogs.tsx 只在模块顶层 import createRoot，测试直接
// 调 DialogHost() 取渲染树，所以这里只需要让它能被导入。
const reactDomDir = join(outDir, 'node_modules', 'react-dom');
mkdirSync(join(reactDomDir, 'client'), {recursive: true});
writeFileSync(join(reactDomDir, 'package.json'), JSON.stringify({
  name: 'react-dom', type: 'module', main: 'client/index.js',
  exports: {'./client': './client/index.js'},
}));
writeFileSync(join(reactDomDir, 'client', 'index.js'), `
export const createRoot = () => ({render() {}});
`);

// ./components/ui 替身：Modal 把 props 原样交出来即可。
// 注意顺序：tsc 会顺着 import 把真实的 components/ui.tsx 一起编出来，替身必须
// 在编译之后写，否则会被覆盖回真组件（连带把 react-dom 等一堆依赖拖进来）。
const uiDir = join(outDir, 'components');
mkdirSync(uiDir, {recursive: true});

execFileSync(process.execPath, [
  tsc,
  '--target', 'es2022', '--module', 'es2022', '--moduleResolution', 'bundler',
  '--jsx', 'react-jsx', '--skipLibCheck', '--ignoreConfig',
  '--outDir', outDir, join(here, 'dialogs.tsx'),
], {cwd: webDir, stdio: 'inherit'});

writeFileSync(join(uiDir, 'ui.js'), `
export const Modal = (props) => ({type: 'Modal', props});
`);
// 编译产物里的 './components/ui' 没有扩展名，ESM 解析不了。
const dialogsPath = join(outDir, 'dialogs.js');
const {readFileSync} = await import('node:fs');
writeFileSync(dialogsPath, readFileSync(dialogsPath, 'utf8').replace("'./components/ui'", "'./components/ui.js'"));

// document 只有一个 body.appendChild：DialogHost 的挂载点。
const appended = [];
globalThis.document = {
  body: {appendChild: (node) => appended.push(node)},
  createElement: () => ({dataset: {}}),
};

const dialogs = await import(pathToFileURL(dialogsPath).href);

// lastDialog 直接调 DialogHost() 取当前该显示的那个询问。它是个普通函数，
// 每次调用都重新读队首，不需要真的挂一个 React 渲染器。
function lastDialog() {
  const tree = dialogs.DialogHost();
  // jsx-runtime 替身让 type 直接是被引用的 Modal 函数，所以只校验它确实渲染了
  // 一层 Modal（有 title 与 children），不去比对函数身份。
  assert.ok(tree && tree.props && typeof tree.props.title === 'string', 'expected a Modal');
  const children = tree.props.children.filter(Boolean);
  const message = children.find((c) => c.type === 'p');
  const actions = children.find((c) => c.props && c.props.className === 'actions modal-actions');
  const buttons = actions ? actions.props.children.filter(Boolean) : [];
  return {
    title: tree.props.title,
    body: message ? message.props.children : '',
    buttons: buttons.map((b) => ({label: b.props.children, className: b.props.className})),
    // 确认按钮一定是最后一个（取消在前，确认在后）。
    confirm: buttons.length > 0 ? buttons[buttons.length - 1].props : null,
    cancel: buttons.length > 1 ? buttons[0].props : null,
  };
}

// 桌面端的真实条件：window.confirm 永远返回 false，而且一次都不该被调用。
let confirmCalls = 0;
let alertCalls = 0;
globalThis.window = {
  confirm: () => { confirmCalls += 1; return false; },
  alert: () => { alertCalls += 1; },
};

test('confirmDialog 不用 window.confirm，桌面端按钮仍有反应', async () => {
  confirmCalls = 0;
  const answer = dialogs.confirmDialog('确定删除输入 Provider：P1？\n\n绑定到该 Provider 的密钥引用将重置为空。', {danger: true, confirmLabel: '删除'});

  // 关键回归：桌面端 window.confirm 恒 false，若 confirmDialog 走它，这里拿到的
  // 会是 false —— 正是「删除按钮点了没反应」的成因。
  assert.equal(confirmCalls, 0, 'confirmDialog must not call window.confirm');

  const dialog = lastDialog();
  assert.equal(dialog.title, '确定删除输入 Provider：P1？');
  assert.match(dialog.body, /密钥引用将重置为空/);
  assert.deepEqual(dialog.buttons.map((b) => b.label), ['取消', '删除']);
  assert.match(dialog.confirm.className, /danger/);

  dialog.confirm.onClick();
  assert.equal(await answer, true, 'clicking the confirm button must resolve true');
});

test('confirmDialog 取消 / 关闭都解成 false', async () => {
  const cancelled = dialogs.confirmDialog('确定删除？');
  lastDialog().cancel.onClick();
  assert.equal(await cancelled, false, 'cancel must resolve false');

  const closed = dialogs.confirmDialog('确定删除？');
  dialogs.DialogHost().props.onClose();
  assert.equal(await closed, false, 'closing must resolve false');
});

test('alertDialog 不用 window.alert，并给出可关闭的提示', async () => {
  alertCalls = 0;
  const done = dialogs.alertDialog('写入失败：权限不足');
  assert.equal(alertCalls, 0, 'alertDialog must not call window.alert');

  const dialog = lastDialog();
  assert.equal(dialog.title, '写入失败：权限不足');
  assert.deepEqual(dialog.buttons.map((b) => b.label), ['知道了']);
  dialog.confirm.onClick();
  assert.equal(await done, true);
});

test('同时排队的两次询问按顺序逐个处理', async () => {
  const first = dialogs.confirmDialog('第一个？');
  const second = dialogs.confirmDialog('第二个？');
  // 同一时刻只显示一个遮罩，后一个排队等前一个关掉。
  assert.equal(lastDialog().title, '第一个？');

  lastDialog().confirm.onClick();
  assert.equal(await first, true);
  assert.equal(lastDialog().title, '第二个？');

  lastDialog().cancel.onClick();
  assert.equal(await second, false);
});
