import { spawn } from 'node:child_process';
import type { Connect } from 'vite';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';

const gatewayTarget = 'http://127.0.0.1:18093';
const gatewayBind = process.env.GATEWAY_ADDR || '0.0.0.0:18093';
const rootDir = dirname(fileURLToPath(new URL('.', import.meta.url)));
const repoDir = dirname(rootDir);
const ensureGatewayScript = join(repoDir, 'scripts', 'ensure-gateway.sh');

async function gatewayIsHealthy() {
  try {
    const response = await fetch(`${gatewayTarget}/__health`);
    if (!response.ok) return false;
    const body = await response.text();
    return body.includes('"status":"ok"');
  } catch {
    return false;
  }
}

function runEnsureGateway(): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn('bash', [ensureGatewayScript], {
      cwd: repoDir,
      env: { ...process.env, GATEWAY_ADDR: gatewayBind },
      stdio: 'inherit',
    });
    child.on('error', reject);
    child.on('exit', (code) => {
      if (code === 0) resolve();
      else reject(new Error(`ensure-gateway.sh exited with code ${code ?? 'null'}`));
    });
  });
}

function gatewayRecoveryMiddleware(): Connect.NextHandleFunction {
  let recovering = false;
  return (req, _res, next) => {
    const url = req.url ?? '';
    if (!url.startsWith('/__')) {
      next();
      return;
    }
    void (async () => {
      if (await gatewayIsHealthy()) {
        next();
        return;
      }
      if (recovering) {
        next();
        return;
      }
      recovering = true;
      try {
        console.warn(`[gateway-dev] ${url} requested while gateway down, recovering...`);
        await runEnsureGateway();
      } catch (error) {
        console.error(`[gateway-dev] recovery failed: ${String(error)}`);
      } finally {
        recovering = false;
        next();
      }
    })();
  };
}

function gatewayDevPlugin(): Plugin {
  let watchdogTimer: NodeJS.Timeout | undefined;
  let ensuring = false;

  async function ensureGatewayRunning(reason: string) {
    if (ensuring) return;
    if (await gatewayIsHealthy()) return;
    ensuring = true;
    try {
      console.warn(`[gateway-dev] ${reason}, running ensure-gateway.sh`);
      await runEnsureGateway();
      console.log(`[gateway-dev] gateway ready at ${gatewayTarget}`);
    } catch (error) {
      console.error(`[gateway-dev] failed to start gateway: ${String(error)}`);
    } finally {
      ensuring = false;
    }
  }

  function startWatchdog() {
    if (watchdogTimer) return;
    watchdogTimer = setInterval(() => {
      void ensureGatewayRunning('gateway unhealthy');
    }, 2000);
  }

  return {
    name: 'gateway-dev',
    apply: 'serve',
    configureServer(server) {
      if (process.env.GATEWAY_DEV_SKIP === '1') return;
      server.middlewares.use(gatewayRecoveryMiddleware());
      void ensureGatewayRunning('dev server starting');
      startWatchdog();
    },
  };
}

export default defineConfig({
  plugins: [react(), gatewayDevPlugin()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/__': { target: gatewayTarget, changeOrigin: true },
    },
  },
  preview: {
    host: '127.0.0.1',
    port: 4173,
    proxy: {
      '/__': { target: gatewayTarget, changeOrigin: true },
    },
  },
});
