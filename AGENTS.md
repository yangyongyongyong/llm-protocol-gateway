# Repository Guidelines

## Project Structure & Module Organization
- `cmd/gateway/main.go` — the single Go entry point for the gateway binary.
- `internal/` — all backend logic, one package per concern: `gateway/` (HTTP router, protocol conversion, OAuth providers, usage — the bulk of the code), `store/` + `monitor/` (SQLite persistence, request logs, daily usage), `config/`, `tunnel/` (Cloudflare), `app/`, `domain/`, `netutil/`, `packaged/`.
- `web/` — Vite + React 19 + TypeScript admin UI. The app lives in `web/src/main.tsx`; the build output `web/dist` is served by the gateway for public/admin pages.
- `desktop/` — Wails macOS app with its own `go.mod`; it reuses the same core.
- `scripts/` — dev and ops shell entry points. `docs/` — user docs and screenshots. `deploy/k8s.yaml`, `Dockerfile`, `docker-compose.yml` — deployment.

## Build, Test, and Development Commands
```bash
cd web && npm install && npm run dev  # builds gateway + web/dist, starts watchdog & Vite (:5173)
npm run verify                        # health-check gateway :18093 and Vite proxy, auto-recover
npm run build                         # tsc -b && vite build → web/dist
go run ./cmd/gateway                  # backend only; override with GATEWAY_ADDR=127.0.0.1:18090
go build ./... && go test ./...       # compile and run the full Go suite
bash scripts/build-macos-app.sh       # package the desktop app
```

## Coding Style & Naming Conventions
- Go: run `gofmt -w` (tabs) and `go vet ./...` before committing. Files in `internal/gateway` use topic-prefixed snake_case names, e.g. `convert_claude_openai_stream.go`, `host_metrics_darwin.go` for build-tagged variants.
- TypeScript: 2-space indent, `strict` mode, React function components in PascalCase, hooks/helpers in camelCase.

## Testing Guidelines
- Standard `testing` package only; tests sit beside their source as `<subject>_test.go` with `TestXxx` functions and table-driven cases.
- Fixtures belong in `internal/gateway/testdata/`. Add regression fixtures for upstream bugs (see `bug_thinking_type_400.json`).
- Cover every new protocol path in both directions; run `go test ./internal/gateway/...` for conversion changes.

## Commit & Pull Request Guidelines
- Follow the existing Conventional Commit style with an optional scope and a concise Chinese subject: `fix(sse): 长思考静默时发送心跳`, `feat: 接入 Qoder PAT Provider`.
- PRs should state the problem and behavior change, note affected protocols/providers, list verification (`go test ./...`, `npm run verify`), and attach screenshots for UI changes.
- Releases publish Docker images on `v*` tags; keep `README.md` and `docs/` current when flags or endpoints change.

## Security & Configuration Tips
Never log or return API keys, OAuth tokens, or Tunnel tokens to the frontend. Local state lives in `~/Library/Application Support/llm-protocol-gateway/` (SQLite); scrub secrets from any doc screenshots.
