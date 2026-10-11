-- VulnHarvest SQLite schema v1. Independent runtime DDL.

-- Generated from the final applied PG shape including every ALTER, plus runtime LLM tables.

-- GIN/GIST become ordinary text indexes; JSON/network membership uses explicit DAO dialects.

CREATE TABLE IF NOT EXISTS portable_sequences(name TEXT PRIMARY KEY NOT NULL, value INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS activity (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    exploration_id INTEGER NOT NULL,
    node_id INTEGER,
    worker TEXT,
    kind TEXT,
    tool TEXT,
    tool_use_id TEXT,
    is_error BOOLEAN NOT NULL DEFAULT false,
    summary TEXT,
    detail TEXT,
    metadata TEXT NOT NULL DEFAULT '{}' CHECK (metadata IS NULL OR (json_valid(metadata))),
    input_tokens INTEGER,
    output_tokens INTEGER,
    cache_read_tokens INTEGER,
    cache_write_tokens INTEGER,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    main_seg INTEGER,
    CONSTRAINT activity_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES explorations(id) ON DELETE CASCADE,
    CONSTRAINT activity_node_id_fkey FOREIGN KEY (node_id) REFERENCES exploration_nodes(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS agent_prompt_vars (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id INTEGER NOT NULL,
    var_name TEXT NOT NULL,
    description TEXT,
    example TEXT,
    source TEXT NOT NULL,
    CONSTRAINT agent_prompt_vars_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
    CONSTRAINT agent_prompt_vars_agent_id_var_name_key UNIQUE (agent_id, var_name),
    CONSTRAINT agent_prompt_vars_source_check CHECK ((source IN ('exploration', 'runtime', 'distilled')))
);

CREATE TABLE IF NOT EXISTS agent_prompts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id INTEGER NOT NULL,
    version INTEGER NOT NULL,
    template_text TEXT NOT NULL,
    note TEXT,
    updated_by TEXT,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT agent_prompts_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
    CONSTRAINT agent_prompts_agent_id_version_key UNIQUE (agent_id, version)
);

CREATE TABLE IF NOT EXISTS agent_skill_visibility (
    agent_id INTEGER NOT NULL,
    skill_name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT agent_skill_visibility_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
    CONSTRAINT agent_skill_visibility_pkey PRIMARY KEY (agent_id, skill_name)
);

CREATE TABLE IF NOT EXISTS agent_triggers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_key TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    interval_sec INTEGER NOT NULL DEFAULT 0,
    on_finding BOOLEAN NOT NULL DEFAULT false,
    on_goal_met BOOLEAN NOT NULL DEFAULT false,
    on_task_timeout BOOLEAN NOT NULL DEFAULT false,
    on_tool_call BOOLEAN NOT NULL DEFAULT false,
    on_task_create BOOLEAN NOT NULL DEFAULT false,
    interval_message TEXT NOT NULL DEFAULT '',
    finding_message TEXT NOT NULL DEFAULT '',
    goal_message TEXT NOT NULL DEFAULT '',
    task_timeout_message TEXT NOT NULL DEFAULT '',
    tool_call_message TEXT NOT NULL DEFAULT '',
    task_create_message TEXT NOT NULL DEFAULT '',
    tool_names TEXT NOT NULL DEFAULT '',
    last_fire DATETIME,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE TABLE IF NOT EXISTS agent_visibility (
    agent_id INTEGER NOT NULL,
    resource_kind TEXT NOT NULL,
    resource_id INTEGER NOT NULL,
    mcp_tool_name TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT agent_visibility_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
    CONSTRAINT agent_visibility_pkey PRIMARY KEY (agent_id, resource_kind, resource_id, mcp_tool_name),
    CONSTRAINT agent_visibility_resource_kind_check CHECK ((resource_kind = 'mcp'))
);

CREATE TABLE IF NOT EXISTS agents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    role TEXT NOT NULL,
    builtin BOOLEAN NOT NULL DEFAULT true,
    enabled BOOLEAN NOT NULL DEFAULT true,
    llm_profile_id INTEGER,
    current_prompt_id INTEGER,
    max_turns INTEGER NOT NULL DEFAULT 0,
    run_seconds INTEGER NOT NULL DEFAULT 1200,
    web_search BOOLEAN NOT NULL DEFAULT false,
    interactive_shell BOOLEAN NOT NULL DEFAULT false,
    wrapup_prompt TEXT NOT NULL DEFAULT '',
    wrapup_max_turns INTEGER NOT NULL DEFAULT 0,
    task_timeout_wrapup_prompt TEXT NOT NULL DEFAULT '',
    task_timeout_wrapup_max_turns INTEGER NOT NULL DEFAULT 0,
    trigger_run_mode TEXT NOT NULL DEFAULT 'serial',
    trigger_merge_mode TEXT NOT NULL DEFAULT 'all',
    trigger_max_parallel INTEGER NOT NULL DEFAULT 5,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT agents_key_check CHECK ((key GLOB '[a-z]*' AND key NOT GLOB '*[^a-z0-9_]*')),
    CONSTRAINT agents_key_key UNIQUE (key),
    CONSTRAINT agents_llm_profile_id_fkey FOREIGN KEY (llm_profile_id) REFERENCES llm_profiles(id) ON DELETE SET NULL,
    CONSTRAINT agents_role_ck CHECK ((role IN ('goals', 'main', 'planner', 'worker', 'assistant'))),
    CONSTRAINT agents_trigger_merge_mode_check CHECK ((trigger_merge_mode IN ('by_task', 'all', 'none'))),
    CONSTRAINT agents_trigger_run_mode_check CHECK ((trigger_run_mode IN ('serial', 'parallel'))),
    CONSTRAINT fk_agents_curprompt FOREIGN KEY (current_prompt_id) REFERENCES agent_prompts(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS asset_intercept_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled BOOLEAN NOT NULL DEFAULT true,
    kind TEXT NOT NULL,
    pattern TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    builtin BOOLEAN NOT NULL DEFAULT false,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT asset_intercept_rules_kind_check CHECK ((kind IN ('exact_domain', 'exact_ip', 'exact_url', 'fuzzy_domain', 'fuzzy_ip', 'fuzzy_url', 'cidr')))
);

CREATE TABLE IF NOT EXISTS assets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    company_id INTEGER,
    company_source TEXT NOT NULL DEFAULT 'explicit',
    task_ids TEXT NOT NULL DEFAULT '[]' CHECK (task_ids IS NULL OR (json_valid(task_ids) AND json_type(task_ids)='array')),
    domain TEXT,
    root_domain TEXT,
    ip TEXT,
    c_segment TEXT,
    port INTEGER,
    icp TEXT,
    bound_domains TEXT NOT NULL DEFAULT '[]' CHECK (bound_domains IS NULL OR (json_valid(bound_domains) AND json_type(bound_domains)='array')),
    open_ports TEXT NOT NULL DEFAULT '[]' CHECK (open_ports IS NULL OR (json_valid(open_ports) AND json_type(open_ports)='array')),
    record_type TEXT,
    record_value TEXT CHECK (record_value IS NULL OR (json_valid(record_value) AND json_type(record_value)='array')),
    bundle_id TEXT,
    app_name TEXT,
    category TEXT,
    app_description TEXT,
    app_icp TEXT,
    url TEXT,
    service_type TEXT,
    service_name TEXT,
    favicon_mmh3 TEXT,
    status_code INTEGER,
    content_length INTEGER,
    page_title TEXT,
    technologies TEXT NOT NULL DEFAULT '[]' CHECK (technologies IS NULL OR (json_valid(technologies) AND json_type(technologies)='array')),
    auth TEXT NOT NULL DEFAULT '[]' CHECK (auth IS NULL OR (json_valid(auth) AND json_type(auth)='array')),
    method TEXT,
    params TEXT NOT NULL DEFAULT '[]' CHECK (params IS NULL OR (json_valid(params) AND json_type(params)='array')),
    extra TEXT NOT NULL DEFAULT '{}' CHECK (extra IS NULL OR (json_valid(extra))),
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    last_seen DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT assets_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE SET NULL,
    CONSTRAINT assets_company_source_check CHECK ((company_source IN ('explicit', 'scope'))),
    CONSTRAINT assets_port_check CHECK (((port >= 1) AND (port <= 65535))),
    CONSTRAINT assets_service_type_check CHECK ((service_type IN ('http', 'other'))),
    CONSTRAINT assets_type_check CHECK ((type IN ('root_domain', 'ip', 'subdomain', 'app', 'service', 'endpoint')))
);

CREATE TABLE IF NOT EXISTS companies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    nkey TEXT NOT NULL,
    logo TEXT,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT companies_nkey_key UNIQUE (nkey)
);

CREATE TABLE IF NOT EXISTS company_scope (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    company_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    domain TEXT,
    net TEXT,
    value TEXT,
    raw TEXT NOT NULL,
    reason TEXT,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT ck_company_scope_payload CHECK ((((kind = 'domain') AND (domain IS NOT NULL) AND (net IS NULL) AND (value IS NULL)) OR ((kind IN ('ip', 'cidr')) AND (domain IS NULL) AND (net IS NOT NULL) AND (value IS NULL)) OR ((kind IN ('icp', 'keyword')) AND (domain IS NULL) AND (net IS NULL) AND (value IS NOT NULL)))),
    CONSTRAINT company_scope_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT company_scope_kind_check CHECK ((kind IN ('domain', 'ip', 'cidr', 'icp', 'keyword'))),
    CONSTRAINT uq_sv2_domain UNIQUE (company_id, domain),
    CONSTRAINT uq_sv2_net UNIQUE (company_id, net)
);

CREATE TABLE IF NOT EXISTS conversation_activities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL,
    worker TEXT,
    kind TEXT,
    tool TEXT,
    tool_use_id TEXT,
    is_error BOOLEAN NOT NULL DEFAULT false,
    summary TEXT,
    detail TEXT,
    input_tokens INTEGER,
    output_tokens INTEGER,
    cache_read_tokens INTEGER,
    cache_write_tokens INTEGER,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT conversation_activities_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_key TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    llm_profile_id INTEGER,
    pinned_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT conversations_llm_profile_id_fkey FOREIGN KEY (llm_profile_id) REFERENCES llm_profiles(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS exploration_anchors (
    node_id INTEGER NOT NULL,
    asset_id INTEGER NOT NULL,
    CONSTRAINT exploration_anchors_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE CASCADE,
    CONSTRAINT exploration_anchors_node_id_fkey FOREIGN KEY (node_id) REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    CONSTRAINT exploration_anchors_pkey PRIMARY KEY (node_id, asset_id)
);

CREATE TABLE IF NOT EXISTS exploration_edges (
    exploration_id INTEGER NOT NULL,
    src_id INTEGER NOT NULL,
    dst_id INTEGER NOT NULL,
    rel TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT ck_edge_noself CHECK ((src_id <> dst_id)),
    CONSTRAINT ck_edge_rel CHECK ((rel IN ('spawns', 'derived_from', 'yields', 'proves', 'covers'))),
    CONSTRAINT exploration_edges_dst_id_fkey FOREIGN KEY (dst_id) REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    CONSTRAINT exploration_edges_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES explorations(id) ON DELETE CASCADE,
    CONSTRAINT exploration_edges_pkey PRIMARY KEY (exploration_id, src_id, rel, dst_id),
    CONSTRAINT exploration_edges_src_id_fkey FOREIGN KEY (src_id) REFERENCES exploration_nodes(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS exploration_nodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    exploration_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}' CHECK (payload IS NULL OR (json_valid(payload))),
    priority INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'open',
    origin TEXT,
    owner TEXT,
    blocked_reason TEXT,
    delete_reason TEXT,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    completed_at DATETIME,
    content_version INTEGER NOT NULL DEFAULT 0,
    cold_since_round INTEGER,
    CONSTRAINT ck_node_kind CHECK ((kind IN ('begin', 'goal', 'intent', 'fact', 'finding', 'hint', 'digest'))),
    CONSTRAINT ck_node_state CHECK ((((kind = 'begin') AND (state = 'open')) OR ((kind = 'intent') AND (state IN ('open', 'running', 'paused', 'done', 'blocked', 'exhausted', 'stopped', 'deleted'))) OR ((kind = 'goal') AND (state IN ('open', 'met', 'abandoned'))) OR ((kind = 'fact') AND (state IN ('confirmed', 'dismissed', 'origin'))) OR ((kind = 'finding') AND (state IN ('confirmed', 'dismissed'))) OR ((kind = 'hint') AND (state IN ('active', 'consumed'))) OR ((kind = 'digest') AND (state IN ('active', 'superseded'))))),
    CONSTRAINT exploration_nodes_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES explorations(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS explorations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    description TEXT,
    goal TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    round_no INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT explorations_status_check CHECK ((status IN ('open', 'achieved', 'failed')))
);

CREATE TABLE IF NOT EXISTS finding_retests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    finding_id INTEGER NOT NULL,
    conversation_id INTEGER,
    status TEXT NOT NULL DEFAULT 'pending',
    verdict TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    snapshot TEXT NOT NULL CHECK (snapshot IS NULL OR (json_valid(snapshot))),
    summary TEXT NOT NULL DEFAULT '',
    evidence TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    started_at DATETIME,
    finished_at DATETIME,
    CONSTRAINT finding_retests_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE SET NULL,
    CONSTRAINT finding_retests_conversation_id_key UNIQUE (conversation_id),
    CONSTRAINT finding_retests_finding_id_fkey FOREIGN KEY (finding_id) REFERENCES findings(id) ON DELETE CASCADE,
    CONSTRAINT finding_retests_status_check CHECK ((status IN ('pending', 'running', 'completed', 'failed', 'stopped'))),
    CONSTRAINT finding_retests_verdict_check CHECK ((verdict IN ('', 'reproduced', 'fixed', 'inconclusive')))
);

CREATE TABLE IF NOT EXISTS finding_traffic_bindings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    finding_id INTEGER NOT NULL,
    snapshot_id TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'supporting',
    note TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT finding_traffic_bindings_finding_id_fkey FOREIGN KEY (finding_id) REFERENCES findings(id) ON DELETE CASCADE,
    CONSTRAINT finding_traffic_bindings_finding_id_snapshot_id_key UNIQUE (finding_id, snapshot_id),
    CONSTRAINT finding_traffic_bindings_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES traffic_evidence_snapshots(id)
);

CREATE TABLE IF NOT EXISTS findings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER,
    node_id INTEGER,
    vulnclass TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    severity TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    evidence TEXT NOT NULL DEFAULT '',
    worker TEXT NOT NULL DEFAULT '',
    asset_ids TEXT NOT NULL DEFAULT '[]' CHECK (asset_ids IS NULL OR (json_valid(asset_ids))),
    status TEXT NOT NULL DEFAULT 'pending',
    report TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    evidence_version INTEGER NOT NULL DEFAULT 0,
    report_evidence_version INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT findings_node_id_fkey FOREIGN KEY (node_id) REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    CONSTRAINT findings_task_id_fkey FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS intercept_pending (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id INTEGER,
    conversation_id INTEGER,
    task_id TEXT,
    agent_name TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL,
    tool_input TEXT NOT NULL DEFAULT '{}' CHECK (tool_input IS NULL OR (json_valid(tool_input))),
    status TEXT NOT NULL DEFAULT 'pending',
    reason TEXT NOT NULL DEFAULT '',
    decided_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    audit TEXT CHECK (audit IS NULL OR (json_valid(audit))),
    decision_source TEXT NOT NULL DEFAULT '',
    CONSTRAINT intercept_pending_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
    CONSTRAINT intercept_pending_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES intercept_rules(id) ON DELETE SET NULL,
    CONSTRAINT intercept_pending_status_check CHECK ((status IN ('pending', 'allowed', 'denied', 'timeout')))
);

CREATE TABLE IF NOT EXISTS intercept_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    priority INTEGER NOT NULL DEFAULT 0,
    match_target TEXT NOT NULL,
    match_type TEXT NOT NULL,
    pattern TEXT NOT NULL,
    action TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    timeout_enabled BOOLEAN NOT NULL DEFAULT true,
    timeout_seconds INTEGER NOT NULL DEFAULT 60,
    timeout_action TEXT NOT NULL DEFAULT 'deny',
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT intercept_rules_action_check CHECK ((action IN ('allow', 'deny', 'ask'))),
    CONSTRAINT intercept_rules_match_target_check CHECK ((match_target IN ('tool_name', 'tool_input'))),
    CONSTRAINT intercept_rules_match_type_check CHECK ((match_type IN ('string', 'regex')))
);

CREATE TABLE IF NOT EXISTS llm_profile_health (
    profile_id INTEGER NOT NULL,
    fails INTEGER NOT NULL DEFAULT 0,
    trips INTEGER NOT NULL DEFAULT 0,
    open_until DATETIME,
    last_error TEXT NOT NULL DEFAULT '',
    last_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT llm_profile_health_pkey PRIMARY KEY (profile_id),
    CONSTRAINT llm_profile_health_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES llm_profiles(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS llm_profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    format TEXT NOT NULL,
    base_url TEXT,
    proxy TEXT,
    model TEXT NOT NULL,
    api_key TEXT,
    api_key_hint TEXT,
    rate_per_second REAL NOT NULL DEFAULT 0,
    rate_per_minute REAL NOT NULL DEFAULT 0,
    context_window_k INTEGER NOT NULL DEFAULT 0,
    reasoning_effort TEXT NOT NULL DEFAULT '',
    thinking_type TEXT NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT false,
    priority INTEGER NOT NULL DEFAULT 0,
    pool_exclude BOOLEAN NOT NULL DEFAULT false,
    streaming BOOLEAN NOT NULL DEFAULT true,
    max_tokens INTEGER NOT NULL DEFAULT 0,
    max_tokens_field TEXT NOT NULL DEFAULT '',
    session_header_key TEXT NOT NULL DEFAULT '',
    retry_connect_attempts INTEGER NOT NULL DEFAULT 0,
    retry_connect_interval_ms INTEGER NOT NULL DEFAULT 0,
    retry_empty_attempts INTEGER NOT NULL DEFAULT 0,
    retry_empty_interval_ms INTEGER NOT NULL DEFAULT 0,
    retry_stream_attempts INTEGER NOT NULL DEFAULT 0,
    retry_stream_interval_ms INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT llm_profiles_format_check CHECK ((format IN ('openai', 'anthropic', 'openai-responses'))),
    CONSTRAINT llm_profiles_max_tokens_check CHECK ((max_tokens >= 0)),
    CONSTRAINT llm_profiles_max_tokens_field_check CHECK ((max_tokens_field IN ('', 'max_completion_tokens'))),
    CONSTRAINT llm_profiles_name_key UNIQUE (name),
    CONSTRAINT llm_profiles_retry_check CHECK (((retry_connect_attempts >= '-1') AND (retry_empty_attempts >= '-1') AND (retry_stream_attempts >= '-1') AND (retry_connect_interval_ms >= 0) AND (retry_empty_interval_ms >= 0) AND (retry_stream_interval_ms >= 0)))
);

CREATE TABLE IF NOT EXISTS main_sessions (
    exploration_id INTEGER NOT NULL,
    seq INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT main_sessions_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES explorations(id) ON DELETE CASCADE,
    CONSTRAINT main_sessions_pkey PRIMARY KEY (exploration_id, seq)
);

CREATE TABLE IF NOT EXISTS mcp_servers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    transport TEXT NOT NULL,
    command TEXT,
    args TEXT NOT NULL DEFAULT '[]' CHECK (args IS NULL OR (json_valid(args))),
    env TEXT NOT NULL DEFAULT '{}' CHECK (env IS NULL OR (json_valid(env))),
    url TEXT,
    enabled BOOLEAN NOT NULL DEFAULT true,
    insecure BOOLEAN NOT NULL DEFAULT false,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT mcp_servers_name_key UNIQUE (name),
    CONSTRAINT mcp_servers_transport_check CHECK ((transport IN ('stdio', 'http', 'sse')))
);

CREATE TABLE IF NOT EXISTS mcp_tools_cache (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id INTEGER NOT NULL,
    tool_name TEXT NOT NULL,
    description TEXT,
    schema TEXT CHECK (schema IS NULL OR (json_valid(schema))),
    discovered_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT mcp_tools_cache_server_id_fkey FOREIGN KEY (server_id) REFERENCES mcp_servers(id) ON DELETE CASCADE,
    CONSTRAINT mcp_tools_cache_server_id_tool_name_key UNIQUE (server_id, tool_name)
);

CREATE TABLE IF NOT EXISTS notification_channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    config TEXT NOT NULL DEFAULT '{}' CHECK (config IS NULL OR (json_valid(config))),
    mode TEXT NOT NULL DEFAULT 'realtime',
    filter TEXT NOT NULL DEFAULT '{}' CHECK (filter IS NULL OR (json_valid(filter))),
    rate_per_min INTEGER NOT NULL DEFAULT 20,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL,
    channel_id INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    last_error TEXT NOT NULL DEFAULT '',
    batch_id INTEGER,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    sent_at DATETIME,
    CONSTRAINT notification_deliveries_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES notification_channels(id) ON DELETE CASCADE,
    CONSTRAINT notification_deliveries_event_id_fkey FOREIGN KEY (event_id) REFERENCES notification_events(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS notification_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    finding_id INTEGER NOT NULL,
    snapshot TEXT NOT NULL CHECK (snapshot IS NULL OR (json_valid(snapshot))),
    fanned_out BOOLEAN NOT NULL DEFAULT false,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now'))
);

CREATE TABLE IF NOT EXISTS scheduler_state (
    key TEXT NOT NULL,
    value TEXT NOT NULL DEFAULT '',
    CONSTRAINT scheduler_state_pkey PRIMARY KEY (key)
);

CREATE TABLE IF NOT EXISTS server_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    level TEXT NOT NULL DEFAULT 'info',
    tag TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT settings_pkey PRIMARY KEY (key)
);

CREATE TABLE IF NOT EXISTS side_question_requests (
    id TEXT NOT NULL,
    ordinal INTEGER NOT NULL DEFAULT 0,
    session_key TEXT NOT NULL,
    generation INTEGER NOT NULL,
    client_id TEXT NOT NULL,
    question TEXT NOT NULL,
    answer TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL CHECK (model IS NULL OR (json_valid(model))),
    snapshot_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    sequence INTEGER NOT NULL DEFAULT 0,
    usage TEXT NOT NULL DEFAULT '{}' CHECK (usage IS NULL OR (json_valid(usage))),
    context_info TEXT NOT NULL DEFAULT '{}' CHECK (context_info IS NULL OR (json_valid(context_info))),
    CONSTRAINT side_question_requests_ordinal_key UNIQUE (ordinal),
    CONSTRAINT side_question_requests_pkey PRIMARY KEY (id),
    CONSTRAINT side_question_requests_session_key_fkey FOREIGN KEY (session_key) REFERENCES side_question_sessions(session_key) ON DELETE CASCADE,
    CONSTRAINT side_question_requests_session_key_generation_client_id_key UNIQUE (session_key, generation, client_id),
    CONSTRAINT side_question_requests_status_check CHECK ((status IN ('running', 'completed', 'failed', 'cancelled', 'interrupted')))
);

CREATE TABLE IF NOT EXISTS side_question_sessions (
    session_key TEXT NOT NULL,
    conversation_id INTEGER,
    task_id INTEGER,
    exploration_id INTEGER,
    intent_id INTEGER,
    run_id INTEGER NOT NULL,
    version INTEGER NOT NULL,
    snapshot TEXT NOT NULL CHECK (snapshot IS NULL OR (json_valid(snapshot))),
    generation INTEGER NOT NULL DEFAULT 0,
    memory TEXT NOT NULL DEFAULT '{}' CHECK (memory IS NULL OR (json_valid(memory))),
    CONSTRAINT side_question_sessions_check CHECK ((((conversation_id IS NOT NULL) AND (task_id IS NULL) AND (exploration_id IS NULL) AND (intent_id IS NULL)) OR ((conversation_id IS NULL) AND (task_id IS NOT NULL) AND (exploration_id IS NOT NULL)))),
    CONSTRAINT side_question_sessions_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE,
    CONSTRAINT side_question_sessions_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES explorations(id) ON DELETE CASCADE,
    CONSTRAINT side_question_sessions_intent_id_fkey FOREIGN KEY (intent_id) REFERENCES exploration_nodes(id) ON DELETE CASCADE,
    CONSTRAINT side_question_sessions_pkey PRIMARY KEY (session_key),
    CONSTRAINT side_question_sessions_task_id_fkey FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS skill_usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    skill TEXT NOT NULL,
    agent_key TEXT,
    task_id INTEGER,
    exploration_id INTEGER,
    intent_id INTEGER,
    session_id TEXT,
    args_len INTEGER NOT NULL DEFAULT 0,
    found BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS task_archives (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'archive_queued',
    phase TEXT NOT NULL DEFAULT 'queued',
    progress INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    warnings TEXT NOT NULL DEFAULT '[]' CHECK (warnings IS NULL OR (json_valid(warnings))),
    format_version INTEGER NOT NULL DEFAULT 2,
    archive_path TEXT NOT NULL DEFAULT '',
    sha256 TEXT NOT NULL DEFAULT '',
    original_size INTEGER NOT NULL DEFAULT 0,
    compressed_size INTEGER NOT NULL DEFAULT 0,
    task_name TEXT NOT NULL DEFAULT '',
    task_description TEXT NOT NULL DEFAULT '',
    task_goal TEXT NOT NULL DEFAULT '',
    original_status TEXT NOT NULL DEFAULT '',
    category_id_snapshot INTEGER,
    category_name_snapshot TEXT NOT NULL DEFAULT '',
    source_task_ids TEXT NOT NULL DEFAULT '[]' CHECK (source_task_ids IS NULL OR (json_valid(source_task_ids) AND json_type(source_task_ids)='array')),
    remaining_timeout_seconds INTEGER NOT NULL DEFAULT 0,
    data_counts TEXT NOT NULL DEFAULT '{}' CHECK (data_counts IS NULL OR (json_valid(data_counts))),
    aggregate_stats TEXT NOT NULL DEFAULT '{}' CHECK (aggregate_stats IS NULL OR (json_valid(aggregate_stats))),
    archived_at DATETIME,
    requested_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT task_archives_progress_check CHECK (((progress >= 0) AND (progress <= 100))),
    CONSTRAINT task_archives_state_check CHECK ((state IN ('archive_queued', 'archiving', 'archive_failed', 'ready', 'restore_queued', 'restoring', 'restore_failed', 'delete_queued', 'deleting', 'delete_failed'))),
    CONSTRAINT task_archives_task_id_fkey FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    CONSTRAINT task_archives_task_id_key UNIQUE (task_id)
);

CREATE TABLE IF NOT EXISTS task_asset_links (
    task_id INTEGER NOT NULL,
    asset_id INTEGER NOT NULL,
    source TEXT NOT NULL DEFAULT 'system',
    source_summary TEXT NOT NULL DEFAULT '',
    source_node_id INTEGER,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT task_asset_links_asset_id_fkey FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE CASCADE,
    CONSTRAINT task_asset_links_pkey PRIMARY KEY (task_id, asset_id),
    CONSTRAINT task_asset_links_source_node_id_fkey FOREIGN KEY (source_node_id) REFERENCES exploration_nodes(id) ON DELETE SET NULL,
    CONSTRAINT task_asset_links_task_id_fkey FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS task_categories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    nkey TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT task_categories_nkey_key UNIQUE (nkey)
);

CREATE TABLE IF NOT EXISTS task_constraints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    exploration_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    text TEXT NOT NULL,
    origin TEXT,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT task_constraints_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES explorations(id) ON DELETE CASCADE,
    CONSTRAINT task_constraints_kind_check CHECK ((kind IN ('allow', 'deny')))
);

CREATE TABLE IF NOT EXISTS task_intercept_rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    action TEXT NOT NULL DEFAULT 'block',
    kind TEXT NOT NULL,
    pattern TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT task_intercept_rules_action_check CHECK ((action IN ('block', 'allow'))),
    CONSTRAINT task_intercept_rules_kind_check CHECK ((kind IN ('exact_domain', 'exact_ip', 'exact_url', 'fuzzy_domain', 'fuzzy_ip', 'fuzzy_url', 'cidr'))),
    CONSTRAINT task_intercept_rules_task_id_fkey FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS task_llm_profiles (
    task_id INTEGER NOT NULL,
    profile_id INTEGER NOT NULL,
    position INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'ready',
    last_error TEXT,
    exhausted_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT task_llm_profiles_pkey PRIMARY KEY (task_id, profile_id),
    CONSTRAINT task_llm_profiles_position_check CHECK (("position" >= 0)),
    CONSTRAINT task_llm_profiles_profile_id_fkey FOREIGN KEY (profile_id) REFERENCES llm_profiles(id) ON DELETE CASCADE,
    CONSTRAINT task_llm_profiles_status_check CHECK ((status IN ('ready', 'quota_exhausted'))),
    CONSTRAINT task_llm_profiles_task_id_fkey FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    CONSTRAINT task_llm_profiles_task_id_position_key UNIQUE (task_id, "position")
);

CREATE TABLE IF NOT EXISTS task_relations (
    task_id INTEGER NOT NULL,
    source_task_id INTEGER NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT ck_task_relation_not_self CHECK ((task_id <> source_task_id)),
    CONSTRAINT task_relations_pkey PRIMARY KEY (task_id, source_task_id),
    CONSTRAINT task_relations_source_task_id_fkey FOREIGN KEY (source_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    CONSTRAINT task_relations_task_id_fkey FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS task_scope (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    company_id INTEGER,
    domain TEXT,
    net TEXT,
    value TEXT,
    source TEXT NOT NULL DEFAULT 'auto',
    reason TEXT,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT task_scope_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE,
    CONSTRAINT task_scope_kind_check CHECK ((kind IN ('company', 'root_domain', 'subdomain', 'ip', 'cidr', 'icp', 'keyword'))),
    CONSTRAINT task_scope_source_check CHECK ((source IN ('auto', 'agent', 'manual'))),
    CONSTRAINT task_scope_task_id_fkey FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS task_templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    nkey TEXT NOT NULL,
    description TEXT NOT NULL,
    goal TEXT NOT NULL,
    category_id INTEGER,
    intercept_rules TEXT NOT NULL DEFAULT '[]' CHECK (intercept_rules IS NULL OR (json_valid(intercept_rules))),
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT task_templates_category_id_fkey FOREIGN KEY (category_id) REFERENCES task_categories(id) ON DELETE SET NULL,
    CONSTRAINT task_templates_nkey_key UNIQUE (nkey)
);

CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT '',
    category_id INTEGER,
    description TEXT NOT NULL,
    goal TEXT NOT NULL,
    exploration_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'created',
    paused BOOLEAN NOT NULL DEFAULT false,
    queued BOOLEAN NOT NULL DEFAULT false,
    queued_at DATETIME,
    queue_mode TEXT NOT NULL DEFAULT '',
    llm_profile_id INTEGER,
    active_llm_profile_id INTEGER,
    llm_chain_revision INTEGER NOT NULL DEFAULT 0,
    company_id INTEGER,
    parent_ref TEXT,
    timeout_seconds INTEGER NOT NULL DEFAULT 0,
    plan_heartbeat_seconds INTEGER NOT NULL DEFAULT 300,
    coverage_enabled BOOLEAN NOT NULL DEFAULT true,
    pinned_at DATETIME,
    first_run_at DATETIME,
    deadline_at DATETIME,
    archived_at DATETIME,
    deleted_at DATETIME,
    completed_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT tasks_active_llm_profile_id_fkey FOREIGN KEY (active_llm_profile_id) REFERENCES llm_profiles(id) ON DELETE SET NULL,
    CONSTRAINT tasks_category_id_fkey FOREIGN KEY (category_id) REFERENCES task_categories(id) ON DELETE SET NULL,
    CONSTRAINT tasks_company_id_fkey FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE SET NULL,
    CONSTRAINT tasks_exploration_id_fkey FOREIGN KEY (exploration_id) REFERENCES explorations(id) ON DELETE RESTRICT,
    CONSTRAINT tasks_exploration_id_key UNIQUE (exploration_id),
    CONSTRAINT tasks_llm_profile_id_fkey FOREIGN KEY (llm_profile_id) REFERENCES llm_profiles(id) ON DELETE SET NULL,
    CONSTRAINT tasks_status_check CHECK ((status IN ('created', 'running', 'paused', 'done', 'failed', 'timeout')))
);

CREATE TABLE IF NOT EXISTS tool_usage (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    tool_key TEXT NOT NULL,
    agent_key TEXT,
    task_id INTEGER,
    exploration_id INTEGER,
    intent_id INTEGER,
    session_id TEXT
);

CREATE TABLE IF NOT EXISTS tools (
    key TEXT NOT NULL,
    system BOOLEAN NOT NULL DEFAULT true,
    description TEXT NOT NULL DEFAULT '',
    schema TEXT NOT NULL DEFAULT '{}' CHECK (schema IS NULL OR (json_valid(schema))),
    agents TEXT NOT NULL DEFAULT '[]' CHECK (agents IS NULL OR (json_valid(agents))),
    enabled BOOLEAN NOT NULL DEFAULT true,
    kind TEXT NOT NULL DEFAULT 'builtin',
    exec TEXT NOT NULL DEFAULT '{}' CHECK (exec IS NULL OR (json_valid(exec))),
    deferred BOOLEAN NOT NULL DEFAULT false,
    updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT tools_pkey PRIMARY KEY (key)
);

CREATE TABLE IF NOT EXISTS traffic_evidence_snapshots (
    id TEXT NOT NULL,
    source_traffic_id TEXT NOT NULL,
    captured_at INTEGER NOT NULL,
    url TEXT NOT NULL,
    method TEXT NOT NULL,
    status INTEGER NOT NULL,
    content_type TEXT NOT NULL DEFAULT '',
    req_head TEXT NOT NULL,
    resp_head TEXT NOT NULL,
    req_hash TEXT NOT NULL,
    resp_hash TEXT NOT NULL,
    req_len INTEGER NOT NULL,
    resp_len INTEGER NOT NULL,
    unreferenced_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    CONSTRAINT traffic_evidence_snapshots_pkey PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS idx_act_latest ON activity (exploration_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_act_main_seg ON activity (exploration_id, main_seg, id) WHERE (worker = 'mainagent');

CREATE INDEX IF NOT EXISTS idx_act_node ON activity (exploration_id, node_id, id);

CREATE INDEX IF NOT EXISTS idx_act_result_usage ON activity (exploration_id) WHERE (kind = 'result');

CREATE INDEX IF NOT EXISTS idx_act_since ON activity (exploration_id, id);

CREATE INDEX IF NOT EXISTS idx_act_tool_call ON activity (exploration_id, tool_use_id, id) WHERE (kind IN ('tool_use', 'tool_result'));

CREATE INDEX IF NOT EXISTS idx_act_worker ON activity (exploration_id, worker, id);

CREATE INDEX IF NOT EXISTS idx_askv_skill ON agent_skill_visibility (skill_name);

CREATE INDEX IF NOT EXISTS idx_agent_triggers_agent ON agent_triggers (agent_key);

CREATE INDEX IF NOT EXISTS idx_vis_resource ON agent_visibility (resource_kind, resource_id);

CREATE INDEX IF NOT EXISTS idx_agents_llm_profile ON agents (llm_profile_id) WHERE (llm_profile_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_asset_intercept_enabled ON asset_intercept_rules (enabled);

CREATE INDEX IF NOT EXISTS idx_av2_bound_domains ON assets (bound_domains) WHERE (type = 'ip');

CREATE INDEX IF NOT EXISTS idx_av2_c_segment ON assets (c_segment ) WHERE (c_segment IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_av2_company ON assets (company_id) WHERE (company_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_av2_company_type ON assets (company_id, type) WHERE (company_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_av2_domain ON assets (domain) WHERE (domain IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_av2_ip ON assets (ip) WHERE (ip IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_av2_last_seen ON assets (last_seen DESC);

CREATE INDEX IF NOT EXISTS idx_av2_open_ports ON assets (open_ports) WHERE (type = 'ip');

CREATE INDEX IF NOT EXISTS idx_av2_root_domain ON assets (root_domain) WHERE (root_domain IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_av2_task_ids ON assets (task_ids);

CREATE INDEX IF NOT EXISTS idx_av2_technologies ON assets (technologies) WHERE (type = 'service');

CREATE INDEX IF NOT EXISTS idx_av2_type_seen ON assets (type, last_seen DESC);

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_app_bundle ON assets (bundle_id) WHERE ((type = 'app') AND (bundle_id IS NOT NULL));

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_app_name ON assets (app_name) WHERE ((type = 'app') AND (bundle_id IS NULL));

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_endpoint ON assets (url, method) WHERE (type = 'endpoint');

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_ip ON assets (ip) WHERE (type = 'ip');

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_root_domain ON assets (domain) WHERE (type = 'root_domain');

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_service_http ON assets (url) WHERE ((type = 'service') AND (service_type = 'http'));

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_service_other ON assets (COALESCE(domain, ''), COALESCE(ip, ''), port, service_name) WHERE ((type = 'service') AND (service_type = 'other'));

CREATE UNIQUE INDEX IF NOT EXISTS uq_av2_subdomain ON assets (domain, COALESCE(record_type, '')) WHERE (type = 'subdomain');

CREATE INDEX IF NOT EXISTS idx_companies_nkey ON companies (nkey);

CREATE INDEX IF NOT EXISTS idx_sv2_company ON company_scope (company_id);

CREATE INDEX IF NOT EXISTS idx_sv2_domain ON company_scope (domain) WHERE (kind = 'domain');

CREATE INDEX IF NOT EXISTS idx_sv2_icp ON company_scope (value) WHERE (kind = 'icp');

CREATE INDEX IF NOT EXISTS idx_sv2_net ON company_scope (net ) WHERE (kind IN ('ip', 'cidr'));

CREATE UNIQUE INDEX IF NOT EXISTS uq_sv2_value ON company_scope (company_id, kind, value) WHERE (kind IN ('icp', 'keyword'));

CREATE INDEX IF NOT EXISTS idx_conv_act ON conversation_activities (conversation_id, id);

CREATE INDEX IF NOT EXISTS idx_conv_act_tool_call ON conversation_activities (conversation_id, tool_use_id, id) WHERE (kind IN ('tool_use', 'tool_result'));

CREATE INDEX IF NOT EXISTS idx_conversations_llm_profile ON conversations (llm_profile_id) WHERE (llm_profile_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_conversations_pinned ON conversations (pinned_at DESC) WHERE (pinned_at IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_anchor_asset ON exploration_anchors (asset_id);

CREATE INDEX IF NOT EXISTS idx_expedges_dst ON exploration_edges (dst_id, rel);

CREATE INDEX IF NOT EXISTS idx_expedges_src ON exploration_edges (src_id, rel);

CREATE INDEX IF NOT EXISTS idx_expnodes_frontier ON exploration_nodes (exploration_id, priority DESC) WHERE ((kind = 'intent') AND (state = 'open'));

CREATE INDEX IF NOT EXISTS idx_expnodes_part ON exploration_nodes (exploration_id, kind);

CREATE UNIQUE INDEX IF NOT EXISTS idx_finding_retests_active ON finding_retests (finding_id) WHERE (status IN ('pending', 'running'));

CREATE INDEX IF NOT EXISTS idx_finding_retests_history ON finding_retests (finding_id, id DESC);

CREATE INDEX IF NOT EXISTS idx_finding_traffic_order ON finding_traffic_bindings (finding_id, "position", id);

CREATE INDEX IF NOT EXISTS idx_finding_traffic_snapshot ON finding_traffic_bindings (snapshot_id);

CREATE INDEX IF NOT EXISTS idx_findings_asset_ids ON findings (asset_ids );

CREATE INDEX IF NOT EXISTS idx_findings_status ON findings (status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_findings_task ON findings (task_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_findings_time ON findings (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_intercept_pending_status ON intercept_pending (status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_intercept_pending_task ON intercept_pending (task_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_one_default ON llm_profiles (is_default) WHERE is_default;

CREATE INDEX IF NOT EXISTS idx_notification_deliveries_batch ON notification_deliveries (batch_id) WHERE (batch_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_notification_deliveries_channel ON notification_deliveries (channel_id, id DESC);

CREATE INDEX IF NOT EXISTS idx_notification_deliveries_due ON notification_deliveries (next_attempt_at) WHERE (state = 'pending');

CREATE INDEX IF NOT EXISTS idx_notification_deliveries_history ON notification_deliveries (id DESC);

CREATE INDEX IF NOT EXISTS idx_notification_events_pending ON notification_events (id) WHERE (NOT fanned_out);

CREATE INDEX IF NOT EXISTS idx_server_logs_id ON server_logs (id DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_side_request_running ON side_question_requests (session_key) WHERE (status = 'running');

CREATE INDEX IF NOT EXISTS idx_side_requests_history ON side_question_requests (session_key, ordinal DESC);

CREATE INDEX IF NOT EXISTS idx_side_sessions_conv ON side_question_sessions (conversation_id);

CREATE INDEX IF NOT EXISTS idx_side_sessions_exp ON side_question_sessions (exploration_id);

CREATE INDEX IF NOT EXISTS idx_side_sessions_intent ON side_question_sessions (intent_id);

CREATE INDEX IF NOT EXISTS idx_side_sessions_task ON side_question_sessions (task_id);

CREATE INDEX IF NOT EXISTS idx_skill_usage_skill ON skill_usage (skill, ts DESC);

CREATE INDEX IF NOT EXISTS idx_skill_usage_task ON skill_usage (task_id);

CREATE INDEX IF NOT EXISTS idx_task_archives_archived ON task_archives (archived_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_task_archives_sources ON task_archives (source_task_ids);

CREATE INDEX IF NOT EXISTS idx_task_archives_state ON task_archives (state, requested_at, id);

CREATE INDEX IF NOT EXISTS idx_task_asset_links_asset ON task_asset_links (asset_id, task_id);

CREATE INDEX IF NOT EXISTS idx_task_asset_links_node ON task_asset_links (source_node_id) WHERE (source_node_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_task_categories_name ON task_categories (name, id);

CREATE INDEX IF NOT EXISTS idx_task_constraints_exp ON task_constraints (exploration_id);

CREATE INDEX IF NOT EXISTS idx_task_intercept_task ON task_intercept_rules (task_id);

CREATE INDEX IF NOT EXISTS idx_task_llm_profiles_order ON task_llm_profiles (task_id, "position");

CREATE INDEX IF NOT EXISTS idx_task_llm_profiles_profile ON task_llm_profiles (profile_id, task_id);

CREATE INDEX IF NOT EXISTS idx_task_relations_source ON task_relations (source_task_id);

CREATE INDEX IF NOT EXISTS idx_ts_company ON task_scope (company_id) WHERE (kind = 'company');

CREATE INDEX IF NOT EXISTS idx_ts_domain ON task_scope (domain) WHERE (kind IN ('root_domain', 'subdomain'));

CREATE INDEX IF NOT EXISTS idx_ts_net ON task_scope (net ) WHERE (kind IN ('ip', 'cidr'));

CREATE UNIQUE INDEX IF NOT EXISTS uq_task_scope_v2 ON task_scope (task_id, kind, COALESCE(domain, ''), COALESCE((net), ''), COALESCE(company_id, (0)), COALESCE(value, ''));

CREATE INDEX IF NOT EXISTS idx_task_templates_updated ON task_templates (updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_tasks_active_llm_profile ON tasks (active_llm_profile_id) WHERE (active_llm_profile_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_tasks_alive ON tasks (created_at DESC) WHERE (deleted_at IS NULL);

CREATE INDEX IF NOT EXISTS idx_tasks_archived ON tasks (archived_at DESC) WHERE (archived_at IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_tasks_category ON tasks (category_id, created_at DESC) WHERE ((deleted_at IS NULL) AND (category_id IS NOT NULL));

CREATE INDEX IF NOT EXISTS idx_tasks_llm_profile ON tasks (llm_profile_id) WHERE (llm_profile_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_tasks_pinned ON tasks (pinned_at DESC) WHERE ((deleted_at IS NULL) AND (pinned_at IS NOT NULL));

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks (status) WHERE (deleted_at IS NULL);

CREATE INDEX IF NOT EXISTS idx_tool_usage_task ON tool_usage (task_id);

CREATE INDEX IF NOT EXISTS idx_tool_usage_tool ON tool_usage (tool_key, ts DESC);

CREATE TRIGGER IF NOT EXISTS trg_agent_triggers_upd AFTER UPDATE ON agent_triggers BEGIN UPDATE agent_triggers SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_agents_upd AFTER UPDATE ON agents BEGIN UPDATE agents SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_asset_intercept_rules_upd AFTER UPDATE ON asset_intercept_rules BEGIN UPDATE asset_intercept_rules SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_av2_upd AFTER UPDATE ON assets BEGIN UPDATE assets SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_companies_upd AFTER UPDATE ON companies BEGIN UPDATE companies SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_conversations_upd AFTER UPDATE ON conversations BEGIN UPDATE conversations SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_expnodes_upd AFTER UPDATE ON exploration_nodes BEGIN UPDATE exploration_nodes SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_exp_upd AFTER UPDATE ON explorations BEGIN UPDATE explorations SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_intercept_rules_upd AFTER UPDATE ON intercept_rules BEGIN UPDATE intercept_rules SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_llm_upd AFTER UPDATE ON llm_profiles BEGIN UPDATE llm_profiles SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_mcp_upd AFTER UPDATE ON mcp_servers BEGIN UPDATE mcp_servers SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_notification_channels_upd AFTER UPDATE ON notification_channels BEGIN UPDATE notification_channels SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_task_archives_upd AFTER UPDATE ON task_archives BEGIN UPDATE task_archives SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_task_asset_links_upd AFTER UPDATE ON task_asset_links BEGIN UPDATE task_asset_links SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_task_categories_upd AFTER UPDATE ON task_categories BEGIN UPDATE task_categories SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_task_intercept_rules_upd AFTER UPDATE ON task_intercept_rules BEGIN UPDATE task_intercept_rules SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_task_llm_profiles_upd AFTER UPDATE ON task_llm_profiles BEGIN UPDATE task_llm_profiles SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_task_templates_upd AFTER UPDATE ON task_templates BEGIN UPDATE task_templates SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_tasks_upd AFTER UPDATE ON tasks BEGIN UPDATE tasks SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_tools_upd AFTER UPDATE ON tools BEGIN UPDATE tools SET updated_at=strftime('%Y-%m-%d %H:%M:%f','now') WHERE rowid=NEW.rowid; END;

CREATE TRIGGER IF NOT EXISTS trg_assets_task_links_insert AFTER INSERT ON assets BEGIN
    INSERT INTO task_asset_links(task_id,asset_id,source,source_summary)
    SELECT t.id,NEW.id,'system','任务执行期间自动关联' FROM json_each(NEW.task_ids) j JOIN tasks t ON t.id=j.value AND t.deleted_at IS NULL
    WHERE true ON CONFLICT(task_id,asset_id) DO NOTHING;
    DELETE FROM task_asset_links WHERE asset_id=NEW.id AND task_id NOT IN (SELECT value FROM json_each(NEW.task_ids));
END;

CREATE TRIGGER IF NOT EXISTS trg_assets_task_links_update AFTER UPDATE OF task_ids ON assets BEGIN
    INSERT INTO task_asset_links(task_id,asset_id,source,source_summary)
    SELECT t.id,NEW.id,'system','任务执行期间自动关联' FROM json_each(NEW.task_ids) j JOIN tasks t ON t.id=j.value AND t.deleted_at IS NULL
    WHERE true ON CONFLICT(task_id,asset_id) DO NOTHING;
    DELETE FROM task_asset_links WHERE asset_id=NEW.id AND task_id NOT IN (SELECT value FROM json_each(NEW.task_ids));
END;

CREATE TRIGGER IF NOT EXISTS trg_conversation_retest_delete BEFORE DELETE ON conversations BEGIN
    UPDATE finding_retests SET status='stopped',error='复测会话已删除',finished_at=strftime('%Y-%m-%d %H:%M:%f','now')
    WHERE conversation_id=OLD.id AND status IN ('pending','running');
END;

CREATE TRIGGER IF NOT EXISTS trg_side_request_ordinal AFTER INSERT ON side_question_requests WHEN NEW.ordinal=0 BEGIN
    INSERT INTO portable_sequences(name,value) VALUES('side_request_ordinal',1)
    ON CONFLICT(name) DO UPDATE SET value=MAX(value,(SELECT COALESCE(MAX(ordinal),0) FROM side_question_requests))+1;
    UPDATE side_question_requests SET ordinal=(SELECT value FROM portable_sequences WHERE name='side_request_ordinal') WHERE id=NEW.id;
END;

CREATE TABLE IF NOT EXISTS llm_records (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ts            DATETIME DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    model         TEXT,
    profile_name  TEXT,
    session_id    TEXT,
    task_id       TEXT,
    worker        TEXT,
    latency_ms    INTEGER,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cache_read    INTEGER,
    cache_write   INTEGER,
    status        TEXT,
    error         TEXT,
    request_body  TEXT,
    response_body TEXT,
    raw_request   TEXT,
    raw_response  TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_records_ts ON llm_records(ts);
CREATE INDEX IF NOT EXISTS idx_llm_records_session ON llm_records(session_id);

CREATE TABLE IF NOT EXISTS llm_usage (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    ts             DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
    task_id        TEXT,
    exploration_id BIGINT,
    worker         TEXT,
    model          TEXT,
    profile_name   TEXT,
    latency_ms     INTEGER,
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read     INTEGER NOT NULL DEFAULT 0,
    cache_write    INTEGER NOT NULL DEFAULT 0,
    status         TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_usage_task  ON llm_usage(task_id);
CREATE INDEX IF NOT EXISTS idx_llm_usage_model ON llm_usage(task_id, model);
CREATE INDEX IF NOT EXISTS idx_llm_usage_exp   ON llm_usage(exploration_id);
