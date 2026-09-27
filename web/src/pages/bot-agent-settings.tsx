import { useEffect, useMemo, useState } from "react";
import { Bot, ChevronDown, CircleStop, Plus, RefreshCw, ShieldCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useConfirm } from "@/components/ui/confirm-dialog";
import { useToast } from "@/hooks/use-toast";
import {
  useAgentProfiles,
  useAgentRun,
  useAgentRuns,
  useAgentSettings,
  useAgentTools,
  useCancelAgentRun,
  useConfirmAgentTool,
  useCreateAgentProfile,
  useResetAgentConversation,
  useUpdateAgentSettings,
} from "@/hooks/use-agent";
import type { AgentRun, BotAgentSettings } from "@/lib/api";

const statusLabel: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  waiting_tool: "等待工具",
  waiting_confirmation: "等待确认",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
  interrupted: "已中断",
};

const effectLabel: Record<string, string> = {
  read: "读取",
  write: "写入",
  destructive: "高风险",
  unknown: "未知",
};
const policyLabel: Record<string, string> = { allow: "允许", confirm: "需确认", deny: "禁止" };
const sourceLabel: Record<string, string> = { app: "应用", installation: "安装配置" };
const activeStatuses = new Set(["queued", "running", "waiting_tool", "waiting_confirmation"]);

function formatTime(timestamp: number) {
  return timestamp ? new Date(timestamp * 1000).toLocaleString() : "-";
}

export function BotAgentSettings({ botId }: { botId: string }) {
  const { toast } = useToast();
  const { confirm, ConfirmDialog } = useConfirm();
  const settingsQuery = useAgentSettings(botId);
  const profilesQuery = useAgentProfiles();
  const toolsQuery = useAgentTools(botId);
  const runsQuery = useAgentRuns(botId);
  const updateSettings = useUpdateAgentSettings(botId);
  const createProfile = useCreateAgentProfile();
  const cancelRun = useCancelAgentRun(botId);
  const resetConversation = useResetAgentConversation(botId);
  const confirmTool = useConfirmAgentTool(botId);

  const serverSettings = settingsQuery.data?.settings;
  const [settings, setSettings] = useState<BotAgentSettings | null>(null);
  const [newModel, setNewModel] = useState("");
  const [selectedRun, setSelectedRun] = useState("");
  const runDetail = useAgentRun(botId, selectedRun);

  useEffect(() => {
    if (serverSettings) setSettings(serverSettings);
  }, [serverSettings]);

  const runs = useMemo(
    () => runsQuery.data?.pages.flatMap((page) => page.runs) ?? [],
    [runsQuery.data],
  );

  const saveSettings = (next: BotAgentSettings) => {
    setSettings(next);
    updateSettings.mutate(next, {
      onSuccess: () => toast({ title: "Agent 设置已保存" }),
      onError: (error) => {
        if (serverSettings) setSettings(serverSettings);
        toast({ variant: "destructive", title: "保存失败", description: error.message });
      },
    });
  };

  const updatePolicy = (patch: Partial<BotAgentSettings>) => {
    if (!settings) return;
    saveSettings({ ...settings, ...patch });
  };

  const createNewProfile = () => {
    const model = newModel.trim();
    if (!model) return;
    createProfile.mutate(
      { model_profile: model, enabled: true },
      {
        onSuccess: (profile) => {
          setNewModel("");
          if (settings) saveSettings({ ...settings, profile_id: profile.id });
        },
        onError: (error) =>
          toast({ variant: "destructive", title: "创建配置失败", description: error.message }),
      },
    );
  };

  const requestReset = async (conversationId: string) => {
    const accepted = await confirm({
      title: "重置对话",
      description: "后续消息将启动新的 Agent 会话。已有运行记录仍会保留。",
      confirmText: "重置",
    });
    if (!accepted) return;
    resetConversation.mutate(conversationId, {
      onSuccess: () => toast({ title: "对话已重置" }),
      onError: (error) =>
        toast({ variant: "destructive", title: "重置失败", description: error.message }),
    });
  };

  if (settingsQuery.isLoading || profilesQuery.isLoading || !settings) {
    return (
      <div className="border-y py-8 text-sm text-muted-foreground">正在加载 Agent 设置...</div>
    );
  }

  const runtimeAvailable = settingsQuery.data?.runtime_available === true;
  const profiles = profilesQuery.data ?? [];
  const tools = toolsQuery.data?.tools ?? [];
  const detail = runDetail.data;
  const pendingConfirmation = detail?.tool_calls.find(
    (call) => call.status === "awaiting_confirmation" && call.confirmation_id,
  );
  const confirmationId = pendingConfirmation?.confirmation_id;

  return (
    <section
      className="space-y-6 border-y border-border/60 py-6"
      aria-labelledby="agent-settings-title"
    >
      {ConfirmDialog}
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <Bot className="h-5 w-5 text-muted-foreground" />
          <div>
            <h2 id="agent-settings-title" className="text-sm font-semibold">
              Bot Agent
            </h2>
            <p className="text-xs text-muted-foreground">
              {runtimeAvailable ? "Pi 运行时可用" : "Pi 运行时未配置或当前不可用"}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`agent-enabled-${botId}`} className="text-xs">
            启用
          </Label>
          <Switch
            id={`agent-enabled-${botId}`}
            checked={settings.routing_mode === "agent"}
            disabled={!runtimeAvailable || !settings.profile_id || updateSettings.isPending}
            onCheckedChange={(enabled) => updatePolicy({ routing_mode: enabled ? "agent" : "off" })}
          />
        </div>
      </div>

      <div className="grid gap-5 lg:grid-cols-2">
        <div className="space-y-3">
          <Label className="text-xs text-muted-foreground">运行配置与模型</Label>
          <Select
            value={settings.profile_id || undefined}
            onValueChange={(profileId) => updatePolicy({ profile_id: profileId })}
          >
            <SelectTrigger>
              <SelectValue placeholder="选择 Agent 配置" />
            </SelectTrigger>
            <SelectContent>
              {profiles.map((profile) => (
                <SelectItem key={profile.id} value={profile.id} disabled={!profile.enabled}>
                  {profile.model_profile}
                  {profile.enabled ? "" : "（已停用）"}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <div className="flex gap-2">
            <Input
              value={newModel}
              onChange={(event) => setNewModel(event.target.value)}
              placeholder="新模型配置名称"
              aria-label="新模型配置名称"
            />
            <Button
              variant="outline"
              size="icon"
              onClick={createNewProfile}
              disabled={!newModel.trim() || createProfile.isPending}
            >
              <Plus className="h-4 w-4" />
              <span className="sr-only">创建配置</span>
            </Button>
          </div>
        </div>

        <div className="space-y-3">
          <Label className="text-xs text-muted-foreground">触发与工具策略</Label>
          <div className="flex items-center justify-between h-9">
            <span className="text-sm">私聊消息</span>
            <Switch
              checked={settings.trigger_policy?.private !== false}
              disabled={!settings.profile_id}
              onCheckedChange={(value) =>
                updatePolicy({ trigger_policy: { ...settings.trigger_policy, private: value } })
              }
            />
          </div>
          <div className="flex items-center justify-between h-9">
            <span className="text-sm">群聊消息</span>
            <Switch
              checked={settings.trigger_policy?.groups === true}
              disabled={!settings.profile_id}
              onCheckedChange={(value) =>
                updatePolicy({ trigger_policy: { ...settings.trigger_policy, groups: value } })
              }
            />
          </div>
          <Select
            value={settings.tool_policy?.default || "confirm"}
            disabled={!settings.profile_id}
            onValueChange={(value: "allow" | "confirm" | "deny") =>
              updatePolicy({ tool_policy: { ...settings.tool_policy, default: value } })
            }
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="allow">默认允许</SelectItem>
              <SelectItem value="confirm">默认需确认</SelectItem>
              <SelectItem value="deny">默认禁止</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-xs font-semibold text-muted-foreground">有效工具</h3>
          <span className="text-xs text-muted-foreground">{tools.length} 个</span>
        </div>
        {toolsQuery.isError ? (
          <p className="py-4 text-xs text-muted-foreground">运行时未配置，无法解析工具状态。</p>
        ) : tools.length === 0 ? (
          <p className="py-4 text-xs text-muted-foreground">没有可供 Agent 使用的工具。</p>
        ) : (
          <div className="divide-y border-y">
            {tools.map((tool) => (
              <div
                key={tool.name}
                className="grid gap-2 py-3 text-xs sm:grid-cols-[minmax(0,1fr)_auto_auto_auto] sm:items-center"
              >
                <div className="min-w-0">
                  <p className="truncate font-medium text-sm">{tool.original_name}</p>
                  <p className="truncate text-muted-foreground">{tool.description || "无描述"}</p>
                </div>
                <Badge variant="outline">{sourceLabel[tool.source] || tool.source}</Badge>
                <Badge variant="outline">{effectLabel[tool.execution.effect] || "未知"}</Badge>
                <Badge variant={tool.policy === "deny" ? "destructive" : "secondary"}>
                  {policyLabel[tool.policy]}
                </Badge>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <h3 className="text-xs font-semibold text-muted-foreground">最近运行</h3>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => runsQuery.refetch()}
            aria-label="刷新运行记录"
          >
            <RefreshCw className="h-3.5 w-3.5" />
          </Button>
        </div>
        {runs.length === 0 ? (
          <p className="py-4 text-xs text-muted-foreground">尚无 Agent 运行记录。</p>
        ) : (
          <div className="divide-y border-y">
            {runs.map((run: AgentRun) => (
              <div key={run.id}>
                <button
                  type="button"
                  className="flex w-full items-center gap-3 py-3 text-left"
                  onClick={() => setSelectedRun(selectedRun === run.id ? "" : run.id)}
                >
                  <Badge variant={run.status === "failed" ? "destructive" : "outline"}>
                    {statusLabel[run.status] || run.status}
                  </Badge>
                  <span className="flex-1 truncate text-xs font-mono">{run.id.slice(0, 12)}</span>
                  <span className="text-xs text-muted-foreground">
                    {formatTime(run.created_at)}
                  </span>
                  <ChevronDown
                    className={`h-4 w-4 transition-transform ${selectedRun === run.id ? "rotate-180" : ""}`}
                  />
                </button>
                {selectedRun === run.id && detail?.run.id === run.id ? (
                  <div className="space-y-4 pb-4 pl-2 sm:pl-6">
                    <div className="flex flex-wrap gap-2">
                      {run.error_code ? (
                        <Badge variant="destructive">{run.error_code}</Badge>
                      ) : null}
                      {activeStatuses.has(run.status) ? (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => cancelRun.mutate(run.id)}
                          disabled={cancelRun.isPending}
                        >
                          <CircleStop className="h-3.5 w-3.5" />
                          取消运行
                        </Button>
                      ) : null}
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => void requestReset(detail.conversation.id)}
                        disabled={resetConversation.isPending}
                      >
                        <RefreshCw className="h-3.5 w-3.5" />
                        重置对话
                      </Button>
                    </div>
                    {run.status === "waiting_confirmation" && confirmationId ? (
                      <div className="flex max-w-sm gap-2">
                        <Button
                          size="sm"
                          disabled={confirmTool.isPending}
                          onClick={() =>
                            confirmTool.mutate({
                              runId: run.id,
                              confirmationId,
                              decision: "approve",
                            })
                          }
                        >
                          <ShieldCheck className="h-3.5 w-3.5" />
                          允许
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={confirmTool.isPending}
                          onClick={() =>
                            confirmTool.mutate({ runId: run.id, confirmationId, decision: "deny" })
                          }
                        >
                          拒绝
                        </Button>
                      </div>
                    ) : null}
                    {detail.tool_calls.length > 0 ? (
                      <div className="space-y-1">
                        {detail.tool_calls.map((call) => (
                          <div key={call.id} className="flex flex-wrap items-center gap-2 text-xs">
                            <span className="font-medium">{call.tool_name}</span>
                            <Badge variant="outline">{effectLabel[call.effect] || "未知"}</Badge>
                            <span className="text-muted-foreground">
                              {statusLabel[call.status] || call.status}
                            </span>
                            {call.error_code ? (
                              <span className="text-destructive">{call.error_code}</span>
                            ) : null}
                          </div>
                        ))}
                      </div>
                    ) : null}
                    <div className="space-y-1">
                      {detail.events.map((event) => (
                        <div key={event.seq} className="flex gap-3 text-xs text-muted-foreground">
                          <span className="w-8 font-mono">#{event.seq}</span>
                          <span>{event.event_type}</span>
                          <span className="ml-auto">{formatTime(event.created_at)}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}
              </div>
            ))}
          </div>
        )}
        {runsQuery.hasNextPage ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => runsQuery.fetchNextPage()}
            disabled={runsQuery.isFetchingNextPage}
          >
            加载更多
          </Button>
        ) : null}
      </div>
    </section>
  );
}
