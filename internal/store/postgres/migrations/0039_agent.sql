-- +goose Up

CREATE TABLE agent_profiles (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    runtime TEXT NOT NULL,
    model_profile TEXT NOT NULL DEFAULT '',
    prompt_version TEXT NOT NULL DEFAULT '',
    limits TEXT NOT NULL DEFAULT '{}',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    updated_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT)
);
CREATE INDEX idx_agent_profiles_owner ON agent_profiles(owner_id);

CREATE TABLE bot_agent_settings (
    bot_id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    routing_mode TEXT NOT NULL DEFAULT 'disabled',
    trigger_policy TEXT NOT NULL DEFAULT '{}',
    tool_policy TEXT NOT NULL DEFAULT '{}',
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    updated_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT)
);

CREATE TABLE agent_conversations (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    bot_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    group_id TEXT NOT NULL DEFAULT '',
    session_ref TEXT NOT NULL DEFAULT '',
    epoch BIGINT NOT NULL DEFAULT 1,
    last_completed_run_id TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    updated_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    UNIQUE (tenant_id, bot_id, provider, sender_id, group_id)
);
CREATE INDEX idx_agent_conversations_bot ON agent_conversations(bot_id, updated_at);

CREATE TABLE agent_runs (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES agent_conversations(id) ON DELETE CASCADE,
    bot_id TEXT NOT NULL,
    inbound_message_id TEXT NOT NULL,
    run_kind TEXT NOT NULL DEFAULT 'message',
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','waiting_tool','waiting_confirmation','completed','failed','cancelled','interrupted')),
    runtime TEXT NOT NULL,
    catalog_version TEXT NOT NULL DEFAULT '',
    deadline BIGINT NOT NULL DEFAULT 0,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until BIGINT NOT NULL DEFAULT 0,
    fence BIGINT NOT NULL DEFAULT 0,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    updated_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    UNIQUE (bot_id, inbound_message_id, run_kind)
);
CREATE INDEX idx_agent_runs_conversation ON agent_runs(conversation_id, created_at);
CREATE INDEX idx_agent_runs_status ON agent_runs(status, created_at);

CREATE TABLE agent_tool_calls (
    id TEXT NOT NULL,
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    installation_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    arguments TEXT NOT NULL DEFAULT '{}',
    args_hash TEXT NOT NULL,
    schema_hash TEXT NOT NULL,
    effect TEXT NOT NULL DEFAULT 'unknown',
    status TEXT NOT NULL DEFAULT 'created' CHECK (status IN ('created','awaiting_confirmation','authorized','dispatched','succeeded','failed','timed_out','unknown')),
    attempt INTEGER NOT NULL DEFAULT 0,
    result TEXT NOT NULL DEFAULT '{}',
    result_ref TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    confirmation_id TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    updated_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    PRIMARY KEY (run_id, id)
);
CREATE INDEX idx_agent_tool_calls_status ON agent_tool_calls(status, updated_at);

CREATE TABLE agent_confirmations (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    call_id TEXT NOT NULL,
    owner_id TEXT NOT NULL DEFAULT '',
    sender_id TEXT NOT NULL,
    code_hash TEXT NOT NULL,
    args_hash TEXT NOT NULL,
    decision TEXT NOT NULL DEFAULT '' CHECK (decision IN ('','approve','deny')),
    expires_at BIGINT NOT NULL,
    used_at BIGINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT)
);
CREATE INDEX idx_agent_confirmations_call ON agent_confirmations(call_id);
CREATE INDEX idx_agent_confirmations_expiry ON agent_confirmations(expires_at) WHERE used_at = 0;

CREATE TABLE agent_run_events (
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    sanitized_payload TEXT NOT NULL DEFAULT '{}',
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    PRIMARY KEY (run_id, seq)
);

CREATE TABLE agent_outbox (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    kind TEXT NOT NULL DEFAULT 'final',
    recipient TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '{}',
    content_ref TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','sent','failed','delivery_blocked')),
    provider_client_id TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    updated_at BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM NOW())::BIGINT),
    UNIQUE (run_id, kind)
);
CREATE INDEX idx_agent_outbox_pending ON agent_outbox(status, created_at);

-- +goose Down
DROP TABLE IF EXISTS agent_outbox;
DROP TABLE IF EXISTS agent_run_events;
DROP TABLE IF EXISTS agent_confirmations;
DROP TABLE IF EXISTS agent_tool_calls;
DROP TABLE IF EXISTS agent_runs;
DROP TABLE IF EXISTS agent_conversations;
DROP TABLE IF EXISTS bot_agent_settings;
DROP TABLE IF EXISTS agent_profiles;
