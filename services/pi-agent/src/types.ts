export type RunStatus = "queued" | "running" | "completed" | "failed" | "cancelled";

export interface ToolSpec {
  name: string;
  description: string;
  parameters: Record<string, unknown>;
}

export interface CreateRunRequest {
  protocol_version: 1;
  run_id: string;
  conversation_id: string;
  session_epoch: number;
  input: { message_id: string; text: string };
  model_profile: string;
  system_prompt_version?: string;
  catalog_version: string;
  tools: ToolSpec[];
  tool_capability?: string;
  limits?: { max_tool_calls?: number; timeout_ms?: number };
}

export interface RunEvent {
  seq: number;
  run_id: string;
  type:
    | "run.started"
    | "text.delta"
    | "tool.started"
    | "tool.completed"
    | "usage.updated"
    | "run.completed"
    | "run.failed"
    | "run.cancelled";
  timestamp: string;
  data: Record<string, unknown>;
}

export interface RunRecord {
  id: string;
  request: CreateRunRequest;
  requestHash: string;
  status: RunStatus;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  result?: { text: string };
  error?: { code: string; message: string };
  events: RunEvent[];
}

export interface RuntimeCallbacks {
  event(type: RunEvent["type"], data?: Record<string, unknown>): void;
}

export interface RuntimeHandle {
  result: Promise<string>;
  abort(): Promise<void>;
}

export interface AgentRuntime {
  ready(): Promise<void>;
  start(request: CreateRunRequest, callbacks: RuntimeCallbacks): Promise<RuntimeHandle>;
}
