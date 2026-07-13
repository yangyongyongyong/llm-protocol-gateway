const vscode = require('vscode');
const path = require('path');
const os = require('os');
const { execFileSync } = require('child_process');

const TOGGLE_CMD = 'aiSettings.usingOpenAIKey.toggle';
const STORAGE_KEY =
  'src.vs.platform.reactivestorage.browser.reactiveStorageServiceImpl.persistentStorage.applicationUser';

function stateDbPath() {
  const home = os.homedir();
  switch (process.platform) {
    case 'darwin':
      return path.join(home, 'Library/Application Support/Cursor/User/globalStorage/state.vscdb');
    case 'win32':
      return path.join(process.env.APPDATA || '', 'Cursor/User/globalStorage/state.vscdb');
    default:
      return path.join(home, '.config/Cursor/User/globalStorage/state.vscdb');
  }
}

/** @returns {boolean|null} */
function readUseOpenAIKey() {
  try {
    const db = stateDbPath();
    // 只读查询，不写库
    const raw = execFileSync(
      'sqlite3',
      [db, `SELECT value FROM ItemTable WHERE key='${STORAGE_KEY}';`],
      { encoding: 'utf8', timeout: 3000 },
    ).trim();
    if (!raw) return null;
    const data = JSON.parse(raw);
    return !!data.useOpenAIKey;
  } catch {
    return null;
  }
}

function label(on) {
  if (on === true) return '自定义 API Key：开';
  if (on === false) return '自定义 API Key：关（自带模型）';
  return '状态未知';
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

async function toggleOnce() {
  await vscode.commands.executeCommand(TOGGLE_CMD);
}

async function setDesired(desired) {
  // 先读盘上的状态；若与内存不一致，最多 toggle 一次
  let cur = readUseOpenAIKey();
  if (cur === null) {
    // 读不到就直接 toggle（仅当想打开时）
    if (desired) await toggleOnce();
    await sleep(250);
    return readUseOpenAIKey();
  }
  if (cur !== desired) {
    await toggleOnce();
    await sleep(250);
    cur = readUseOpenAIKey();
  }
  return cur;
}

/**
 * @param {vscode.ExtensionContext} context
 */
function activate(context) {
  context.subscriptions.push(
    vscode.commands.registerCommand('local.openaiKey.toggle', async () => {
      try {
        await toggleOnce();
        await sleep(250);
        const cur = readUseOpenAIKey();
        vscode.window.showInformationMessage(`已切换 → ${label(cur)}`);
      } catch (e) {
        vscode.window.showErrorMessage(`切换失败: ${e}`);
      }
    }),

    vscode.commands.registerCommand('local.openaiKey.enable', async () => {
      try {
        const cur = await setDesired(true);
        vscode.window.showInformationMessage(label(cur));
      } catch (e) {
        vscode.window.showErrorMessage(`开启失败: ${e}`);
      }
    }),

    vscode.commands.registerCommand('local.openaiKey.disable', async () => {
      try {
        const cur = await setDesired(false);
        vscode.window.showInformationMessage(label(cur));
      } catch (e) {
        vscode.window.showErrorMessage(`关闭失败: ${e}`);
      }
    }),

    vscode.commands.registerCommand('local.openaiKey.status', async () => {
      const cur = readUseOpenAIKey();
      vscode.window.showInformationMessage(label(cur));
    }),
  );
}

function deactivate() {}

module.exports = { activate, deactivate };
