import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const dist = join(here, 'dist');

mkdirSync(dist, { recursive: true });

// Minimal bootstrap page shown until OnDomReady navigates to the local gateway.
// Do not auto-redirect here — that races with OnDomReady and can cause flicker.
const bootstrap = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>LLM Protocol Gateway</title>
  <style>
    html, body { height: 100%; margin: 0; font-family: ui-sans-serif, system-ui, sans-serif; background: #f6f7f9; color: #1f2937; }
    .wrap { min-height: 100%; display: grid; place-items: center; padding: 24px; }
    .card { max-width: 420px; text-align: center; }
    .muted { color: #6b7280; font-size: 14px; margin-top: 8px; }
  </style>
</head>
<body>
  <div class="wrap">
    <div class="card">
      <h1>LLM Protocol Gateway</h1>
      <p class="muted">正在启动本地网关…</p>
    </div>
  </div>
</body>
</html>
`;

writeFileSync(join(dist, 'index.html'), bootstrap, 'utf8');
console.log('desktop frontend shell ready');
