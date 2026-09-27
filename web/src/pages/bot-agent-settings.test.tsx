// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BotAgentSettings, withDefaultToolPolicy } from "./bot-agent-settings";

const updateMutate = vi.fn();
const unavailableSettings = {
  bot_id: "bot-1",
  profile_id: "",
  routing_mode: "off" as const,
  trigger_policy: {},
  tool_policy: {},
};

vi.mock("@/hooks/use-agent", () => ({
  useAgentSettings: () => ({
    data: {
      runtime_available: false,
      settings: unavailableSettings,
    },
    isLoading: false,
  }),
  useAgentProfiles: () => ({ data: [], isLoading: false }),
  useAgentTools: () => ({ data: { tools: [] }, isError: false }),
  useAgentRuns: () => ({
    data: { pages: [{ runs: [], next_cursor: "" }] },
    refetch: vi.fn(),
    hasNextPage: false,
    fetchNextPage: vi.fn(),
    isFetchingNextPage: false,
  }),
  useAgentRun: () => ({ data: undefined }),
  useUpdateAgentSettings: () => ({ mutate: updateMutate, isPending: false }),
  useCreateAgentProfile: () => ({ mutate: vi.fn(), isPending: false }),
  useCancelAgentRun: () => ({ mutate: vi.fn(), isPending: false }),
  useResetAgentConversation: () => ({ mutate: vi.fn(), isPending: false }),
  useConfirmAgentTool: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/use-toast", () => ({ useToast: () => ({ toast: vi.fn() }) }));
vi.mock("@/components/ui/confirm-dialog", () => ({
  useConfirm: () => ({ confirm: vi.fn(), ConfirmDialog: null }),
}));

describe("BotAgentSettings", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it("shows an unconfigured Pi state and disables enablement", async () => {
    await act(async () => root.render(<BotAgentSettings botId="bot-1" />));

    expect(container.textContent).toContain("Pi 运行时未配置或当前不可用");
    expect(container.textContent).toContain("没有可供 Agent 使用的工具");
    expect(container.textContent).toContain("尚无 Agent 运行记录");
    expect(container.textContent).toContain("按工具影响级别");
    const enable = container.querySelector("#agent-enabled-bot-1");
    expect(enable?.getAttribute("data-disabled")).not.toBeNull();
    expect(updateMutate).not.toHaveBeenCalled();
  });

  it("preserves an undefined default when using per-effect policy", () => {
    expect(
      withDefaultToolPolicy({ default: "confirm", tools: { lookup: "allow" } }, "effect-default"),
    ).toEqual({ tools: { lookup: "allow" } });
  });
});
