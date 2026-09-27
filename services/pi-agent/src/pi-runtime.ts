import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import {
  createAgentSession,
  ModelRuntime,
  SessionManager,
  SettingsManager,
  type AgentSession,
} from "@earendil-works/pi-coding-agent";
import type { ServiceConfig } from "./config.js";
import { sessionKey } from "./config.js";
import { createHubTools } from "./hub-tools.js";
import { isolatedResourceLoader } from "./resource-loader.js";
import type { AgentRuntime, CreateRunRequest, RuntimeCallbacks, RuntimeHandle } from "./types.js";

export class PiRuntime implements AgentRuntime {
  #modelRuntime?: ModelRuntime;

  constructor(private readonly config: ServiceConfig) {}

  async #initialize(): Promise<ModelRuntime> {
    if (this.#modelRuntime) return this.#modelRuntime;
    await Promise.all([
      mkdir(this.config.agentDir, { recursive: true }),
      mkdir(this.config.sessionDir, { recursive: true }),
      mkdir(this.config.workspaceDir, { recursive: true }),
    ]);
    this.#modelRuntime = await ModelRuntime.create({
      authPath: join(this.config.agentDir, "auth.json"),
      modelsPath: join(this.config.agentDir, "models.json"),
      refreshOnCreate: false,
    });
    return this.#modelRuntime;
  }

  async ready(): Promise<void> {
    const runtime = await this.#initialize();
    for (const [name, profile] of Object.entries(this.config.modelProfiles)) {
      if (!runtime.getModel(profile.provider, profile.model)) throw new Error(`model profile ${name} references an unknown model`);
      if (!runtime.hasConfiguredAuth(profile.provider)) throw new Error(`model profile ${name} has no configured authentication`);
    }
  }

  async start(request: CreateRunRequest, callbacks: RuntimeCallbacks): Promise<RuntimeHandle> {
    const runtime = await this.#initialize();
    const profile = this.config.modelProfiles[request.model_profile];
    if (!profile) throw new Error(`unknown model profile: ${request.model_profile}`);
    const model = runtime.getModel(profile.provider, profile.model);
    if (!model) throw new Error(`unknown model: ${profile.provider}/${profile.model}`);

    const abortController = new AbortController();
    let session: AgentSession | undefined;
    const maxToolCalls = request.limits?.max_tool_calls ?? this.config.defaultMaxToolCalls;
    const tools = createHubTools({
      request,
      hubBaseUrl: this.config.hubBaseUrl,
      serviceToken: this.config.serviceToken,
      timeoutMs: this.config.toolTimeoutMs,
      maxToolCalls,
      onStarted: (callId, name) => callbacks.event("tool.started", { call_id: callId, tool_name: name }),
      onCompleted: (callId, name, isError) => callbacks.event("tool.completed", { call_id: callId, tool_name: name, is_error: isError }),
    });

    const result = (async () => {
      const key = sessionKey(request.conversation_id, request.session_epoch);
      const conversationDir = join(this.config.sessionDir, key);
      await mkdir(conversationDir, { recursive: true });
      const manager = SessionManager.continueRecent(this.config.workspaceDir, conversationDir);
      const created = await createAgentSession({
        cwd: this.config.workspaceDir,
        agentDir: this.config.agentDir,
        modelRuntime: runtime,
        model,
        thinkingLevel: profile.thinkingLevel ?? "off",
        sessionManager: manager,
        settingsManager: SettingsManager.inMemory({
          compaction: { enabled: true },
          retry: { enabled: true, maxRetries: 2 },
        }),
        resourceLoader: isolatedResourceLoader(request.system_prompt_version),
        noTools: "builtin",
        customTools: tools,
      });
      session = created.session;
      if (abortController.signal.aborted) {
        session.dispose();
        throw abortController.signal.reason;
      }
      let settled!: () => void;
      const settledPromise = new Promise<void>((resolve) => (settled = resolve));
      const unsubscribe = session.subscribe((event) => {
        if (event.type === "agent_settled") settled();
        if (event.type === "message_update" && event.assistantMessageEvent.type === "text_delta") {
          callbacks.event("text.delta", { text: event.assistantMessageEvent.delta });
        }
        if (event.type === "turn_end" && "usage" in event.message && event.message.usage) {
          const usage = event.message.usage;
          callbacks.event("usage.updated", {
            input_tokens: usage.input,
            output_tokens: usage.output,
            cache_read_tokens: usage.cacheRead,
            cache_write_tokens: usage.cacheWrite,
          });
        }
      });
      const onAbort = () => void session?.abort();
      abortController.signal.addEventListener("abort", onAbort, { once: true });
      try {
        await session.prompt(request.input.text, { expandPromptTemplates: false, source: "rpc" });
        await settledPromise;
        return session.getLastAssistantText() ?? "";
      } finally {
        abortController.signal.removeEventListener("abort", onAbort);
        unsubscribe();
        session.dispose();
        session = undefined;
      }
    })();

    return {
      result,
      abort: async () => {
        if (!abortController.signal.aborted) abortController.abort(new Error("run cancelled"));
        await session?.abort();
      },
    };
  }
}
