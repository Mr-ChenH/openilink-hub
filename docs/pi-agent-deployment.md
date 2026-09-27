# Deploy the Pi Agent Sidecar

The Pi runtime is optional. The base [`docker-compose.yml`](../docker-compose.yml) remains the Hub, PostgreSQL, and MinIO stack; [`docker-compose.pi.yml`](../docker-compose.pi.yml) adds the internal Pi sidecar without changing the existing `pgdata` or `miniodata` volumes.

Use an agent-enabled Hub release. The Hub and sidecar must have matching internal protocol support; pin both images in production and do not use `latest` in a deployment manifest.

## Prepare secrets

Create a random service token shared only by Hub and the sidecar:

```bash
install -d -m 0700 secrets/pi-agent
openssl rand -hex 32 > secrets/agent_service_token
chmod 0600 secrets/agent_service_token
```

Create `secrets/pi-agent/auth.json` with the model provider credential. For example:

```json
{
  "anthropic": {
    "type": "api_key",
    "key": "sk-ant-replace-me"
  }
}
```

Then restrict it:

```bash
chmod 0600 secrets/pi-agent/auth.json
```

`auth.json`, OAuth tokens, `models.json` credentials, and the service token must not be committed. The overlay mounts the Pi configuration directory read-only and does not mount a home directory, SSH keys, or the Docker socket. Prefer a secret-manager-generated file in `PI_AGENT_CONFIG_DIR` for production rather than keeping long-lived keys in a shell environment.

For an OpenAI-compatible or other custom endpoint, add `secrets/pi-agent/models.json`. This example defines an internal OpenAI-compatible service:

```json
{
  "providers": {
    "internal": {
      "baseUrl": "https://models.example.com/v1",
      "api": "openai-completions",
      "apiKey": "${INTERNAL_MODEL_API_KEY}",
      "models": [{ "id": "production-chat" }]
    }
  }
}
```

Environment interpolation in `models.json` is resolved inside the sidecar, so the referenced variable must be injected into that service by the operator. Avoid putting credentials in `PI_AGENT_MODEL_PROFILES`, prompts, or run payloads.

## Configure model profiles

`PI_AGENT_MODEL_PROFILES` maps the Hub's `model_profile` value to a Pi provider and exact model ID. It is configuration, not a secret:

```bash
export PI_AGENT_MODEL_PROFILES='{
  "default":{"provider":"anthropic","model":"claude-sonnet-4-5","thinkingLevel":"off"},
  "deep":{"provider":"anthropic","model":"claude-sonnet-4-5","thinkingLevel":"high"}
}'
```

Allowed thinking levels are `off`, `minimal`, `low`, `medium`, `high`, and `xhigh`; the selected model must support the chosen level. Startup readiness fails when a profile references an unknown model or its provider has no configured authentication. Keep profile names stable because Hub agent profiles store the name, not the provider credential.

## Enable Pi

Select an immutable sidecar image. The Docker workflow publishes `sha-<commit>` tags to `ghcr.io/<owner>/<repository>-pi-agent`; a digest gives the strongest production pin:

```bash
export PI_AGENT_IMAGE='ghcr.io/openilink/openilink-hub-pi-agent@sha256:<digest>'
docker compose -f docker-compose.yml -f docker-compose.pi.yml config --quiet
docker compose -f docker-compose.yml -f docker-compose.pi.yml pull pi-agent
docker compose -f docker-compose.yml -f docker-compose.pi.yml up -d
```

The sidecar port is exposed only to the Compose network. No host port is published. Conversation transcripts persist in the `pi_agent_sessions` named volume; the working directory is an ephemeral `tmpfs`, and the container root filesystem is read-only.

Verify both process health and model readiness:

```bash
docker compose -f docker-compose.yml -f docker-compose.pi.yml ps
docker compose -f docker-compose.yml -f docker-compose.pi.yml exec pi-agent \
  node -e "fetch('http://127.0.0.1:8080/healthz').then(async r=>{console.log(r.status,await r.text());process.exit(r.ok?0:1)})"
docker compose -f docker-compose.yml -f docker-compose.pi.yml exec pi-agent \
  node -e "fetch('http://127.0.0.1:8080/readyz').then(async r=>{console.log(r.status,await r.text());process.exit(r.ok?0:1)})"
```

A successful `/healthz` proves the process is serving HTTP. A successful `/readyz` additionally proves every configured profile resolves to a known model with usable authentication.

## Upgrade

1. Read the Hub and sidecar release notes and confirm their protocol compatibility.
2. Back up PostgreSQL and the `pi_agent_sessions` volume. Do not remove `pgdata` or `miniodata`.
3. Update the Hub version and `PI_AGENT_IMAGE` to immutable tags or digests.
4. Pull the sidecar, deploy the compatible Hub first, then recreate the sidecar.
5. Wait for `/readyz`, then enable Pi only for a test Bot before widening rollout.

```bash
docker compose -f docker-compose.yml -f docker-compose.pi.yml pull pi-agent
docker compose -f docker-compose.yml -f docker-compose.pi.yml up -d hub
docker compose -f docker-compose.yml -f docker-compose.pi.yml up -d --no-deps pi-agent
```

Never run package installation during container startup. The published sidecar image contains the lockfile-resolved Pi SDK version and production dependencies.

## Roll back or disable

Disable Pi for Bots first and cancel or record in-flight runs. Do not automatically replay a run through another runtime: a tool call may already have caused an external side effect.

To roll back, restore the previous compatible Hub and sidecar image references, deploy Hub first, then recreate `pi-agent`. Database migrations are additive; do not delete or roll back the existing database volumes.

To remove the optional runtime while leaving Hub and data services running:

```bash
docker compose -f docker-compose.yml -f docker-compose.pi.yml stop pi-agent
docker compose -f docker-compose.yml -f docker-compose.pi.yml rm -f pi-agent
docker compose -f docker-compose.yml up -d
```

Keep `pi_agent_sessions` until its retention and backup requirements are satisfied. Removing the volume permanently deletes conversation transcripts but does not remove Hub, PostgreSQL, or MinIO data.
