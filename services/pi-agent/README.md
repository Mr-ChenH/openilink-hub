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

Pi reads model credentials from `PI_AGENT_DIR/auth.json` and optional custom models from `PI_AGENT_DIR/models.json`. `/healthz` and `/readyz` are probe endpoints; all `/v1/*` routes require `Authorization: Bearer <AGENT_SERVICE_TOKEN>`. Runs containing tools must include `tool_capability`; it is forwarded only as `X-Agent-Run-Capability` to the fixed Hub broker URL. `system_prompt_version` selects an embedded allowlisted prompt (`messaging-v1` by default); it is never interpreted as a path and unknown versions fail the run.

## Commands

```sh
npm ci
npm run typecheck
npm test
npm start
```

## Security and retention

- The sidecar requires outbound access to the configured model providers and to the fixed `HUB_AGENT_BASE_URL`. Deployment policy must deny other egress if that boundary is required; the process does not enforce a network sandbox itself.
- Runtime event replay is process-local, event-type allowlisted, field rebuilt, and size bounded. Terminal cleanup removes input text, tool schemas, and the run capability from retained run requests. Final public text remains available in the terminal run result and bounded replay event.
- Pi conversation transcripts persist under `PI_AGENT_SESSION_DIR` for model continuation and may contain prompts and tool content. Treat that directory as protected application state, exclude it from user APIs and backups unless encrypted, and delete the epoch directory when the corresponding conversation retention period expires.
- Hub tool-call arguments and results are retained only in internal Agent tables for idempotency and recovery. Agent user APIs expose status metadata but not those fields; application delivery/API logs redact agent tool requests and all response bodies.
- npm dependencies are exact-versioned and `npm ci` verifies lockfile integrity hashes. The image uses the CI-pinned Node `22.19.0` base tag and CI emits SBOM/provenance; operators requiring immutable base identity must additionally enforce an approved registry digest in their build policy.
