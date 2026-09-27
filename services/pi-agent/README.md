# Pi agent sidecar

Internal protocol v1 service that embeds `@earendil-works/pi-coding-agent` `0.87.1`. The service exposes only Hub-provided custom tools; Pi built-in filesystem/shell tools, extensions, skills, prompt templates, themes, and context-file discovery are disabled.

## Configuration

Required:

- `AGENT_SERVICE_TOKEN` or `AGENT_SERVICE_TOKEN_FILE`: inbound bearer token and outbound Hub service identity.
- `PI_AGENT_MODEL_PROFILES`: JSON object keyed by request `model_profile`, for example `{"default":{"provider":"anthropic","model":"claude-sonnet-4-5","thinkingLevel":"off"}}`.

Optional:

- `HUB_AGENT_BASE_URL` (default `http://hub:9800/internal/agent/v1`)
- `HOST` / `PORT` (defaults `0.0.0.0` / `8080`)
- `PI_AGENT_DIR`, `PI_AGENT_SESSION_DIR`, `PI_AGENT_WORKSPACE_DIR`
- `PI_AGENT_MAX_CONCURRENCY` (4), `PI_AGENT_MAX_QUEUE` (20)
- `PI_AGENT_DEFAULT_TIMEOUT_MS` (90000), `PI_AGENT_MAX_TIMEOUT_MS` (300000)
- `PI_AGENT_DEFAULT_MAX_TOOL_CALLS` (8), `PI_AGENT_TOOL_TIMEOUT_MS` (15000)
- `PI_AGENT_EVENT_LIMIT` (2000 retained events per live run)

Pi reads model credentials from `PI_AGENT_DIR/auth.json` and optional custom models from `PI_AGENT_DIR/models.json`. `/healthz` and `/readyz` are probe endpoints; all `/v1/*` routes require `Authorization: Bearer <AGENT_SERVICE_TOKEN>`. Runs containing tools must include `tool_capability`; it is forwarded only as `X-Agent-Run-Capability` to the fixed Hub broker URL.

## Commands

```sh
npm ci
npm run typecheck
npm test
npm start
```

Run state and SSE replay are process-local in this first implementation. Pi conversation transcripts persist under a SHA-256-derived per-conversation-and-epoch directory; arbitrary conversation IDs never become filesystem paths.
