import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type BotAgentSettings } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";

export function useAgentProfiles() {
  return useQuery({ queryKey: queryKeys.agentProfiles(), queryFn: api.listAgentProfiles });
}

export function useCreateAgentProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: api.createAgentProfile,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.agentProfiles() }),
  });
}

export function useAgentSettings(botId: string) {
  return useQuery({
    queryKey: queryKeys.bots.agentSettings(botId),
    queryFn: () => api.getAgentSettings(botId),
    enabled: !!botId,
  });
}

export function useUpdateAgentSettings(botId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (
      settings: Pick<
        BotAgentSettings,
        "profile_id" | "routing_mode" | "trigger_policy" | "tool_policy"
      >,
    ) => api.updateAgentSettings(botId, settings),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.bots.agentSettings(botId) });
      queryClient.invalidateQueries({ queryKey: queryKeys.bots.agentTools(botId) });
    },
  });
}

export function useAgentTools(botId: string) {
  return useQuery({
    queryKey: queryKeys.bots.agentTools(botId),
    queryFn: () => api.getAgentTools(botId),
    enabled: !!botId,
    retry: false,
  });
}

export function useAgentRuns(botId: string) {
  return useInfiniteQuery({
    queryKey: queryKeys.bots.agentRuns(botId),
    queryFn: ({ pageParam }) => api.listAgentRuns(botId, 10, pageParam || undefined),
    initialPageParam: "",
    getNextPageParam: (page) => page.next_cursor || undefined,
    enabled: !!botId,
    refetchInterval: 10_000,
  });
}

export function useAgentRun(botId: string, runId: string) {
  return useQuery({
    queryKey: queryKeys.bots.agentRun(botId, runId),
    queryFn: () => api.getAgentRun(botId, runId),
    enabled: !!botId && !!runId,
    refetchInterval: 5_000,
  });
}

function useAgentAction(botId: string, action: (args: any) => Promise<unknown>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: action,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.bots.agentRuns(botId) });
      queryClient.invalidateQueries({ queryKey: queryKeys.bots.agentConversations(botId) });
    },
  });
}

export function useCancelAgentRun(botId: string) {
  return useAgentAction(botId, (runId: string) => api.cancelAgentRun(botId, runId));
}

export function useResetAgentConversation(botId: string) {
  return useAgentAction(botId, (conversationId: string) =>
    api.resetAgentConversation(botId, conversationId),
  );
}

export function useConfirmAgentTool(botId: string) {
  return useAgentAction(
    botId,
    ({
      runId,
      confirmationId,
      decision,
    }: {
      runId: string;
      confirmationId: string;
      decision: "approve" | "deny";
    }) => api.confirmAgentTool(botId, runId, confirmationId, decision),
  );
}
