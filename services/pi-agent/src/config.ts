import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

export interface ServiceConfig {
  host: string;
  port: number;
  serviceToken: string;
  hubBaseUrl: string;
  agentDir: string;
  sessionDir: string;
  workspaceDir: string;
  modelProfiles: Record<string, ModelProfile>;
  maxConcurrency: number;
  maxQueue: number;
  defaultTimeoutMs: number;
  maxTimeoutMs: number;
  defaultMaxToolCalls: number;
  toolTimeoutMs: number;
  eventLimit: number;
}

export interface ModelProfile {
  provider: string;
  model: string;
  thinkingLevel?: "off" | "minimal" | "low" | "medium" | "high" | "xhigh";
}

function positiveInt(value: string | undefined, fallback: number, name: string): number {
  const result = value === undefined ? fallback : Number(value);
  if (!Number.isSafeInteger(result) || result <= 0) throw new Error(`${name} must be a positive integer`);
  return result;
}

async function secret(env: NodeJS.ProcessEnv): Promise<string> {
  const direct = env.AGENT_SERVICE_TOKEN?.trim();
  if (direct) return direct;
  const file = env.AGENT_SERVICE_TOKEN_FILE;
  if (!file) throw new Error("AGENT_SERVICE_TOKEN or AGENT_SERVICE_TOKEN_FILE is required");
  const value = (await readFile(file, "utf8")).trim();
  if (!value) throw new Error("agent service token is empty");
  return value;
}

export async function loadConfig(env: NodeJS.ProcessEnv = process.env): Promise<ServiceConfig> {
  const rawProfiles = env.PI_AGENT_MODEL_PROFILES;
  if (!rawProfiles) throw new Error("PI_AGENT_MODEL_PROFILES is required");
  const profiles = JSON.parse(rawProfiles) as Record<string, ModelProfile>;
  if (!profiles.default && Object.keys(profiles).length === 0) throw new Error("at least one model profile is required");
  for (const [name, profile] of Object.entries(profiles)) {
    if (!name || !profile.provider || !profile.model) throw new Error(`invalid model profile: ${name}`);
  }
  return {
    host: env.HOST ?? "0.0.0.0",
    port: positiveInt(env.PORT, 8080, "PORT"),
    serviceToken: await secret(env),
    hubBaseUrl: (env.HUB_AGENT_BASE_URL ?? "http://hub:9800/internal/agent/v1").replace(/\/$/, ""),
    agentDir: env.PI_AGENT_DIR ?? "/var/lib/pi-agent/config",
    sessionDir: env.PI_AGENT_SESSION_DIR ?? "/var/lib/pi-agent/sessions",
    workspaceDir: env.PI_AGENT_WORKSPACE_DIR ?? "/var/lib/pi-agent/workspace",
    modelProfiles: profiles,
    maxConcurrency: positiveInt(env.PI_AGENT_MAX_CONCURRENCY, 4, "PI_AGENT_MAX_CONCURRENCY"),
    maxQueue: positiveInt(env.PI_AGENT_MAX_QUEUE, 20, "PI_AGENT_MAX_QUEUE"),
    defaultTimeoutMs: positiveInt(env.PI_AGENT_DEFAULT_TIMEOUT_MS, 90_000, "PI_AGENT_DEFAULT_TIMEOUT_MS"),
    maxTimeoutMs: positiveInt(env.PI_AGENT_MAX_TIMEOUT_MS, 300_000, "PI_AGENT_MAX_TIMEOUT_MS"),
    defaultMaxToolCalls: positiveInt(env.PI_AGENT_DEFAULT_MAX_TOOL_CALLS, 8, "PI_AGENT_DEFAULT_MAX_TOOL_CALLS"),
    toolTimeoutMs: positiveInt(env.PI_AGENT_TOOL_TIMEOUT_MS, 15_000, "PI_AGENT_TOOL_TIMEOUT_MS"),
    eventLimit: positiveInt(env.PI_AGENT_EVENT_LIMIT, 2_000, "PI_AGENT_EVENT_LIMIT"),
  };
}

export function sessionKey(conversationId: string, epoch: number): string {
  return createHash("sha256").update(`${conversationId}\0${epoch}`).digest("hex");
}
