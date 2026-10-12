package studio

import sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"

var migrations = []sharedsqlite.Migration{
	{
		Version: 1,
		Name:    "create_project_library",
		SQL: `
CREATE TABLE project_library_entries (
    project_id                    TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(project_id)) > 0),
    root_path                     TEXT    NOT NULL UNIQUE CHECK (length(trim(root_path)) > 0),
    display_name_snapshot         TEXT    NOT NULL CHECK (length(trim(display_name_snapshot)) > 0),
    cover_cache_key               TEXT    NULL CHECK (cover_cache_key IS NULL OR length(trim(cover_cache_key)) > 0),
    width_snapshot                INTEGER NOT NULL CHECK (width_snapshot > 0),
    height_snapshot               INTEGER NOT NULL CHECK (height_snapshot > 0),
    episode_count_snapshot        INTEGER NOT NULL CHECK (episode_count_snapshot >= 0),
    project_updated_at_ms_snapshot INTEGER NOT NULL CHECK (project_updated_at_ms_snapshot >= 0),
    added_at_ms                   INTEGER NOT NULL CHECK (added_at_ms >= 0),
    last_opened_at_ms             INTEGER NULL CHECK (last_opened_at_ms IS NULL OR last_opened_at_ms >= 0),
    refreshed_at_ms               INTEGER NOT NULL CHECK (refreshed_at_ms >= 0)
) STRICT;

CREATE INDEX project_library_entries_recent
    ON project_library_entries(last_opened_at_ms DESC, added_at_ms DESC);
`,
	},
	{
		Version: 2,
		Name:    "create_agent_runtime",
		SQL: `
CREATE TABLE agent_runtime_settings (
    singleton_id                  INTEGER PRIMARY KEY NOT NULL CHECK (singleton_id = 1),
    codex_executable_path         TEXT    NULL CHECK (codex_executable_path IS NULL OR length(trim(codex_executable_path)) > 0),
    revision                      INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms)
) STRICT;

INSERT INTO agent_runtime_settings (
    singleton_id, codex_executable_path, revision, created_at_ms, updated_at_ms
) VALUES (1, NULL, 1, 0, 0);

CREATE TABLE agent_sessions (
    id                            TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    project_id                    TEXT    NOT NULL CHECK (length(trim(project_id)) > 0),
    episode_id                    TEXT    NOT NULL CHECK (length(trim(episode_id)) > 0),
    codex_thread_id               TEXT    NOT NULL UNIQUE CHECK (length(trim(codex_thread_id)) > 0),
    codex_session_id              TEXT    NOT NULL CHECK (length(trim(codex_session_id)) > 0),
    model_id                      TEXT    NOT NULL CHECK (length(trim(model_id)) > 0),
    cached_title                  TEXT    NULL CHECK (cached_title IS NULL OR length(trim(cached_title)) > 0),
    web_search_disabled           INTEGER NOT NULL CHECK (web_search_disabled IN (0, 1)),
    status                        TEXT    NOT NULL CHECK (status IN ('active', 'history_unavailable', 'archived')),
    revision                      INTEGER NOT NULL CHECK (revision >= 1),
    archived_at_ms                INTEGER NULL CHECK (archived_at_ms IS NULL OR archived_at_ms >= 0),
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    CHECK ((status = 'archived') = (archived_at_ms IS NOT NULL)),
    CHECK (archived_at_ms IS NULL OR archived_at_ms = updated_at_ms)
) STRICT;

CREATE INDEX agent_sessions_episode_updated
    ON agent_sessions(project_id, episode_id, updated_at_ms DESC, id ASC);
CREATE INDEX agent_sessions_status
    ON agent_sessions(status, updated_at_ms DESC);

CREATE TABLE agent_turns (
    id                            TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    agent_session_id              TEXT    NOT NULL,
    codex_turn_id                 TEXT    NULL UNIQUE CHECK (codex_turn_id IS NULL OR length(trim(codex_turn_id)) > 0),
    client_operation_id           TEXT    NOT NULL CHECK (length(trim(client_operation_id)) > 0),
    focus_scene_id                TEXT    NULL CHECK (focus_scene_id IS NULL OR length(trim(focus_scene_id)) > 0),
    focus_clip_id                 TEXT    NULL CHECK (focus_clip_id IS NULL OR length(trim(focus_clip_id)) > 0),
    focus_asset_id                TEXT    NULL CHECK (focus_asset_id IS NULL OR length(trim(focus_asset_id)) > 0),
    status                        TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'waiting_user', 'completed', 'interrupted', 'failed')),
    dispatch_state                TEXT    NOT NULL CHECK (dispatch_state IN ('queued', 'dispatching', 'inflight', 'reconciling', 'terminal')),
    error_code                    TEXT    NULL CHECK (error_code IS NULL OR length(trim(error_code)) > 0),
    error_summary                 TEXT    NULL CHECK (error_summary IS NULL OR length(trim(error_summary)) > 0),
    revision                      INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    started_at_ms                 INTEGER NULL CHECK (started_at_ms IS NULL OR started_at_ms >= created_at_ms),
    completed_at_ms               INTEGER NULL CHECK (completed_at_ms IS NULL OR completed_at_ms >= created_at_ms),
    FOREIGN KEY (agent_session_id) REFERENCES agent_sessions(id) ON DELETE CASCADE,
    UNIQUE (agent_session_id, client_operation_id),
    UNIQUE (agent_session_id, id),
    CHECK ((error_code IS NULL) = (error_summary IS NULL)),
    CHECK ((status = 'failed') = (error_code IS NOT NULL AND error_summary IS NOT NULL)),
    CHECK (
        (status = 'queued' AND dispatch_state IN ('queued', 'dispatching') AND started_at_ms IS NULL AND completed_at_ms IS NULL) OR
        (status IN ('running', 'waiting_user') AND dispatch_state IN ('inflight', 'reconciling') AND started_at_ms IS NOT NULL AND completed_at_ms IS NULL) OR
        (status IN ('completed', 'interrupted', 'failed') AND dispatch_state = 'terminal' AND completed_at_ms IS NOT NULL)
    )
) STRICT;

CREATE INDEX agent_turns_session_created
    ON agent_turns(agent_session_id, created_at_ms ASC, id ASC);
CREATE INDEX agent_turns_dispatch
    ON agent_turns(dispatch_state, created_at_ms ASC, id ASC);
CREATE UNIQUE INDEX agent_turns_one_active_per_session
    ON agent_turns(agent_session_id)
    WHERE status IN ('running', 'waiting_user');
CREATE UNIQUE INDEX agent_turns_one_queued_per_session
    ON agent_turns(agent_session_id)
    WHERE status = 'queued';

CREATE TABLE agent_input_operations (
    agent_session_id              TEXT    NOT NULL,
    client_operation_id           TEXT    NOT NULL CHECK (length(trim(client_operation_id)) > 0),
    request_fingerprint           TEXT    NOT NULL CHECK (length(request_fingerprint) = 64 AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
    action                        TEXT    NOT NULL CHECK (action IN ('queue_turn', 'steer_turn')),
    agent_turn_id                 TEXT    NOT NULL,
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    PRIMARY KEY (agent_session_id, client_operation_id),
    FOREIGN KEY (agent_session_id) REFERENCES agent_sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (agent_session_id, agent_turn_id) REFERENCES agent_turns(agent_session_id, id) ON DELETE CASCADE
) STRICT;

CREATE INDEX agent_input_operations_turn
    ON agent_input_operations(agent_turn_id, created_at_ms ASC);

CREATE TABLE agent_mcp_bindings (
    id                            TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(id)) > 0),
    agent_session_id              TEXT    NOT NULL UNIQUE,
    workspace_path                TEXT    NOT NULL CHECK (length(trim(workspace_path)) > 0),
    route_key                     TEXT    NOT NULL UNIQUE CHECK (length(trim(route_key)) > 0),
    status                        TEXT    NOT NULL CHECK (status IN ('preparing', 'ready', 'failed')),
    error_code                    TEXT    NULL CHECK (error_code IS NULL OR length(trim(error_code)) > 0),
    error_summary                 TEXT    NULL CHECK (error_summary IS NULL OR length(trim(error_summary)) > 0),
    revision                      INTEGER NOT NULL CHECK (revision >= 1),
    last_verified_at_ms           INTEGER NULL CHECK (last_verified_at_ms IS NULL OR last_verified_at_ms >= 0),
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    FOREIGN KEY (agent_session_id) REFERENCES agent_sessions(id) ON DELETE CASCADE,
    CHECK ((error_code IS NULL) = (error_summary IS NULL)),
    CHECK ((status = 'failed') = (error_code IS NOT NULL AND error_summary IS NOT NULL)),
    CHECK ((status = 'ready') = (last_verified_at_ms IS NOT NULL))
) STRICT;
`,
	},
	{
		Version: 3,
		Name:    "create_generation_provider_profiles",
		SQL: `
CREATE TABLE generation_provider_profiles (
    generator_id                  TEXT    PRIMARY KEY NOT NULL CHECK (generator_id IN (
        'video.seedance-2.5.volcengine-cn',
        'video.seedance-2.5.byteplus-global',
        'video.h3.official',
        'video.wan-3.0.official'
    )),
    endpoint_preset               TEXT    NOT NULL CHECK (length(trim(endpoint_preset)) > 0),
    model_id                      TEXT    NOT NULL CHECK (length(trim(model_id)) > 0),
    resource_id                   TEXT    NULL CHECK (resource_id IS NULL OR length(trim(resource_id)) > 0),
    workspace_id                  TEXT    NULL CHECK (workspace_id IS NULL OR length(trim(workspace_id)) > 0),
    credential_ref                TEXT    NOT NULL UNIQUE CHECK (length(trim(credential_ref)) > 0),
    enabled                       INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    concurrency_limit             INTEGER NOT NULL CHECK (concurrency_limit BETWEEN 1 AND 5),
    capability_version            TEXT    NULL CHECK (capability_version IS NULL OR length(trim(capability_version)) > 0),
    health                        TEXT    NOT NULL CHECK (health IN ('unchecked', 'ready', 'unavailable')),
    failure_code                  TEXT    NULL CHECK (failure_code IS NULL OR length(trim(failure_code)) > 0),
    last_checked_at_ms            INTEGER NULL CHECK (last_checked_at_ms IS NULL OR last_checked_at_ms >= 0),
    revision                      INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    CHECK (
        (health = 'unchecked' AND capability_version IS NULL AND failure_code IS NULL AND last_checked_at_ms IS NULL) OR
        (health = 'ready' AND capability_version IS NOT NULL AND failure_code IS NULL AND last_checked_at_ms IS NOT NULL) OR
        (health = 'unavailable' AND capability_version IS NULL AND failure_code IS NOT NULL AND last_checked_at_ms IS NOT NULL)
    ),
    CHECK (
        (generator_id = 'video.seedance-2.5.volcengine-cn' AND endpoint_preset = 'volcengine_cn_beijing' AND workspace_id IS NULL) OR
        (generator_id = 'video.seedance-2.5.byteplus-global' AND endpoint_preset = 'byteplus_ap_southeast_1' AND workspace_id IS NULL) OR
        (generator_id = 'video.h3.official' AND endpoint_preset = 'minimax_global' AND resource_id IS NULL AND workspace_id IS NULL) OR
        (generator_id = 'video.wan-3.0.official' AND endpoint_preset IN (
            'alibaba_cn_beijing',
            'alibaba_ap_southeast_1',
            'alibaba_ap_northeast_1',
            'alibaba_eu_central_1',
            'alibaba_us_east_1',
            'alibaba_cn_hongkong'
        ) AND resource_id IS NULL AND workspace_id IS NOT NULL)
    )
) STRICT;

CREATE INDEX generation_provider_profiles_enabled
    ON generation_provider_profiles(enabled, generator_id);
`,
	},
	{
		Version: 4,
		Name:    "record_agent_turn_model",
		SQL: `
ALTER TABLE agent_turns ADD COLUMN model_id TEXT NULL CHECK (model_id IS NULL OR length(trim(model_id)) > 0);
UPDATE agent_turns
SET model_id = (
    SELECT agent_sessions.model_id
    FROM agent_sessions
    WHERE agent_sessions.id = agent_turns.agent_session_id
);
`,
	},
	{
		Version: 5,
		Name:    "create_agent_template_preferences",
		SQL: `
CREATE TABLE agent_template_preferences (
    singleton_id                  INTEGER PRIMARY KEY NOT NULL CHECK (singleton_id = 1),
    locale                        TEXT    NOT NULL CHECK (locale IN ('zh-CN', 'en')),
    revision                      INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms)
) STRICT;

CREATE TABLE agent_template_overrides (
    template_key                  TEXT    PRIMARY KEY NOT NULL CHECK (template_key IN (
        'script.extract_assets',
        'asset.add',
        'asset.edit_prompt',
        'storyboard.plan',
        'storyboard.add_clip',
        'prompt.first_frame',
        'prompt.last_frame',
        'prompt.video'
    )),
    content                       TEXT    NOT NULL CHECK (length(trim(content)) > 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= 0)
) STRICT;
`,
	},
	{
		Version: 6,
		Name:    "persist_agent_input_text",
		SQL: `
ALTER TABLE agent_input_operations
ADD COLUMN input_text TEXT NULL
CHECK (input_text IS NULL OR (length(input_text) > 0 AND instr(input_text, char(0)) = 0));
`,
	},
	{
		Version: 7,
		Name:    "create_generation_provider_models",
		SQL: `
CREATE TABLE generation_provider_models (
    generator_id                  TEXT    NOT NULL,
    model_id                      TEXT    NOT NULL CHECK (length(trim(model_id)) > 0),
    is_default                    INTEGER NOT NULL CHECK (is_default IN (0, 1)),
    capability_version            TEXT    NULL CHECK (capability_version IS NULL OR length(trim(capability_version)) > 0),
    health                        TEXT    NOT NULL CHECK (health IN ('unchecked', 'ready', 'unavailable')),
    failure_code                  TEXT    NULL CHECK (failure_code IS NULL OR length(trim(failure_code)) > 0),
    last_checked_at_ms            INTEGER NULL CHECK (last_checked_at_ms IS NULL OR last_checked_at_ms >= 0),
    PRIMARY KEY (generator_id, model_id),
    FOREIGN KEY (generator_id) REFERENCES generation_provider_profiles(generator_id) ON DELETE CASCADE,
    CHECK (
        (health = 'unchecked' AND capability_version IS NULL AND failure_code IS NULL AND last_checked_at_ms IS NULL) OR
        (health = 'ready' AND capability_version IS NOT NULL AND failure_code IS NULL AND last_checked_at_ms IS NOT NULL) OR
        (health = 'unavailable' AND capability_version IS NULL AND failure_code IS NOT NULL AND last_checked_at_ms IS NOT NULL)
    )
) STRICT;

CREATE UNIQUE INDEX generation_provider_models_one_default
    ON generation_provider_models(generator_id) WHERE is_default = 1;

INSERT INTO generation_provider_models (
    generator_id, model_id, is_default, capability_version, health, failure_code, last_checked_at_ms
)
SELECT generator_id, model_id, 1, capability_version, health, failure_code, last_checked_at_ms
FROM generation_provider_profiles;
`,
	},
	{
		Version: 8,
		Name:    "expand_provider_concurrency",
		SQL: `
CREATE TABLE generation_provider_profiles_v8 (
    generator_id                  TEXT    PRIMARY KEY NOT NULL CHECK (generator_id IN (
        'video.seedance-2.5.volcengine-cn',
        'video.seedance-2.5.byteplus-global',
        'video.h3.official',
        'video.wan-3.0.official'
    )),
    endpoint_preset               TEXT    NOT NULL CHECK (length(trim(endpoint_preset)) > 0),
    model_id                      TEXT    NOT NULL CHECK (length(trim(model_id)) > 0),
    resource_id                   TEXT    NULL CHECK (resource_id IS NULL OR length(trim(resource_id)) > 0),
    workspace_id                  TEXT    NULL CHECK (workspace_id IS NULL OR length(trim(workspace_id)) > 0),
    credential_ref                TEXT    NOT NULL UNIQUE CHECK (length(trim(credential_ref)) > 0),
    enabled                       INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    concurrency_limit             INTEGER NOT NULL CHECK (concurrency_limit BETWEEN 1 AND 10),
    capability_version            TEXT    NULL CHECK (capability_version IS NULL OR length(trim(capability_version)) > 0),
    health                        TEXT    NOT NULL CHECK (health IN ('unchecked', 'ready', 'unavailable')),
    failure_code                  TEXT    NULL CHECK (failure_code IS NULL OR length(trim(failure_code)) > 0),
    last_checked_at_ms            INTEGER NULL CHECK (last_checked_at_ms IS NULL OR last_checked_at_ms >= 0),
    revision                      INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    CHECK (
        (health = 'unchecked' AND capability_version IS NULL AND failure_code IS NULL AND last_checked_at_ms IS NULL) OR
        (health = 'ready' AND capability_version IS NOT NULL AND failure_code IS NULL AND last_checked_at_ms IS NOT NULL) OR
        (health = 'unavailable' AND capability_version IS NULL AND failure_code IS NOT NULL AND last_checked_at_ms IS NOT NULL)
    ),
    CHECK (
        (generator_id = 'video.seedance-2.5.volcengine-cn' AND endpoint_preset = 'volcengine_cn_beijing' AND workspace_id IS NULL) OR
        (generator_id = 'video.seedance-2.5.byteplus-global' AND endpoint_preset = 'byteplus_ap_southeast_1' AND workspace_id IS NULL) OR
        (generator_id = 'video.h3.official' AND endpoint_preset = 'minimax_global' AND resource_id IS NULL AND workspace_id IS NULL) OR
        (generator_id = 'video.wan-3.0.official' AND endpoint_preset IN (
            'alibaba_cn_beijing',
            'alibaba_ap_southeast_1',
            'alibaba_ap_northeast_1',
            'alibaba_eu_central_1',
            'alibaba_us_east_1',
            'alibaba_cn_hongkong'
        ) AND resource_id IS NULL AND workspace_id IS NOT NULL)
    )
) STRICT;

INSERT INTO generation_provider_profiles_v8 (
    generator_id, endpoint_preset, model_id, resource_id, workspace_id,
    credential_ref, enabled, concurrency_limit, capability_version, health,
    failure_code, last_checked_at_ms, revision, created_at_ms, updated_at_ms
)
SELECT
    generator_id, endpoint_preset, model_id, resource_id, workspace_id,
    credential_ref, enabled, concurrency_limit, capability_version, health,
    failure_code, last_checked_at_ms, revision, created_at_ms, updated_at_ms
FROM generation_provider_profiles;

CREATE TABLE generation_provider_models_v8 (
    generator_id                  TEXT    NOT NULL,
    model_id                      TEXT    NOT NULL CHECK (length(trim(model_id)) > 0),
    is_default                    INTEGER NOT NULL CHECK (is_default IN (0, 1)),
    capability_version            TEXT    NULL CHECK (capability_version IS NULL OR length(trim(capability_version)) > 0),
    health                        TEXT    NOT NULL CHECK (health IN ('unchecked', 'ready', 'unavailable')),
    failure_code                  TEXT    NULL CHECK (failure_code IS NULL OR length(trim(failure_code)) > 0),
    last_checked_at_ms            INTEGER NULL CHECK (last_checked_at_ms IS NULL OR last_checked_at_ms >= 0),
    PRIMARY KEY (generator_id, model_id),
    FOREIGN KEY (generator_id) REFERENCES generation_provider_profiles_v8(generator_id) ON DELETE CASCADE,
    CHECK (
        (health = 'unchecked' AND capability_version IS NULL AND failure_code IS NULL AND last_checked_at_ms IS NULL) OR
        (health = 'ready' AND capability_version IS NOT NULL AND failure_code IS NULL AND last_checked_at_ms IS NOT NULL) OR
        (health = 'unavailable' AND capability_version IS NULL AND failure_code IS NOT NULL AND last_checked_at_ms IS NOT NULL)
    )
) STRICT;

INSERT INTO generation_provider_models_v8 (
    generator_id, model_id, is_default, capability_version, health, failure_code, last_checked_at_ms
)
SELECT generator_id, model_id, is_default, capability_version, health, failure_code, last_checked_at_ms
FROM generation_provider_models;

DROP TABLE generation_provider_models;
DROP TABLE generation_provider_profiles;
ALTER TABLE generation_provider_profiles_v8 RENAME TO generation_provider_profiles;
ALTER TABLE generation_provider_models_v8 RENAME TO generation_provider_models;

CREATE INDEX generation_provider_profiles_enabled
    ON generation_provider_profiles(enabled, generator_id);
CREATE UNIQUE INDEX generation_provider_models_one_default
    ON generation_provider_models(generator_id) WHERE is_default = 1;
`,
	},
	{
		Version: 9,
		Name:    "pin_agent_session_runtime",
		SQL: `
ALTER TABLE agent_sessions
ADD COLUMN runtime_kind TEXT NOT NULL DEFAULT 'user_codex'
CHECK (runtime_kind IN ('user_codex', 'shotforward'));

CREATE INDEX agent_sessions_runtime_kind
    ON agent_sessions(runtime_kind, updated_at_ms DESC);
`,
	},
	{
		Version: 10,
		Name:    "add_minimax_china_provider",
		SQL: `
CREATE TABLE generation_provider_profiles_v10 (
    generator_id                  TEXT    PRIMARY KEY NOT NULL CHECK (generator_id IN (
        'video.seedance-2.5.volcengine-cn',
        'video.seedance-2.5.byteplus-global',
        'video.h3.minimax-cn',
        'video.h3.official',
        'video.wan-3.0.official'
    )),
    endpoint_preset               TEXT    NOT NULL CHECK (length(trim(endpoint_preset)) > 0),
    model_id                      TEXT    NOT NULL CHECK (length(trim(model_id)) > 0),
    resource_id                   TEXT    NULL CHECK (resource_id IS NULL OR length(trim(resource_id)) > 0),
    workspace_id                  TEXT    NULL CHECK (workspace_id IS NULL OR length(trim(workspace_id)) > 0),
    credential_ref                TEXT    NOT NULL UNIQUE CHECK (length(trim(credential_ref)) > 0),
    enabled                       INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    concurrency_limit             INTEGER NOT NULL CHECK (concurrency_limit BETWEEN 1 AND 10),
    capability_version            TEXT    NULL CHECK (capability_version IS NULL OR length(trim(capability_version)) > 0),
    health                        TEXT    NOT NULL CHECK (health IN ('unchecked', 'ready', 'unavailable')),
    failure_code                  TEXT    NULL CHECK (failure_code IS NULL OR length(trim(failure_code)) > 0),
    last_checked_at_ms            INTEGER NULL CHECK (last_checked_at_ms IS NULL OR last_checked_at_ms >= 0),
    revision                      INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms                 INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    CHECK (
        (health = 'unchecked' AND capability_version IS NULL AND failure_code IS NULL AND last_checked_at_ms IS NULL) OR
        (health = 'ready' AND capability_version IS NOT NULL AND failure_code IS NULL AND last_checked_at_ms IS NOT NULL) OR
        (health = 'unavailable' AND capability_version IS NULL AND failure_code IS NOT NULL AND last_checked_at_ms IS NOT NULL)
    ),
    CHECK (
        (generator_id = 'video.seedance-2.5.volcengine-cn' AND endpoint_preset = 'volcengine_cn_beijing' AND workspace_id IS NULL) OR
        (generator_id = 'video.seedance-2.5.byteplus-global' AND endpoint_preset = 'byteplus_ap_southeast_1' AND workspace_id IS NULL) OR
        (generator_id = 'video.h3.minimax-cn' AND endpoint_preset = 'minimax_cn' AND resource_id IS NULL AND workspace_id IS NULL) OR
        (generator_id = 'video.h3.official' AND endpoint_preset = 'minimax_global' AND resource_id IS NULL AND workspace_id IS NULL) OR
        (generator_id = 'video.wan-3.0.official' AND endpoint_preset IN (
            'alibaba_cn_beijing',
            'alibaba_ap_southeast_1',
            'alibaba_ap_northeast_1',
            'alibaba_eu_central_1',
            'alibaba_us_east_1',
            'alibaba_cn_hongkong'
        ) AND resource_id IS NULL AND workspace_id IS NOT NULL)
    )
) STRICT;

INSERT INTO generation_provider_profiles_v10 (
    generator_id, endpoint_preset, model_id, resource_id, workspace_id,
    credential_ref, enabled, concurrency_limit, capability_version, health,
    failure_code, last_checked_at_ms, revision, created_at_ms, updated_at_ms
)
SELECT
    generator_id, endpoint_preset, model_id, resource_id, workspace_id,
    credential_ref, enabled, concurrency_limit, capability_version, health,
    failure_code, last_checked_at_ms, revision, created_at_ms, updated_at_ms
FROM generation_provider_profiles;

CREATE TABLE generation_provider_models_v10 (
    generator_id                  TEXT    NOT NULL,
    model_id                      TEXT    NOT NULL CHECK (length(trim(model_id)) > 0),
    is_default                    INTEGER NOT NULL CHECK (is_default IN (0, 1)),
    capability_version            TEXT    NULL CHECK (capability_version IS NULL OR length(trim(capability_version)) > 0),
    health                        TEXT    NOT NULL CHECK (health IN ('unchecked', 'ready', 'unavailable')),
    failure_code                  TEXT    NULL CHECK (failure_code IS NULL OR length(trim(failure_code)) > 0),
    last_checked_at_ms            INTEGER NULL CHECK (last_checked_at_ms IS NULL OR last_checked_at_ms >= 0),
    PRIMARY KEY (generator_id, model_id),
    FOREIGN KEY (generator_id) REFERENCES generation_provider_profiles_v10(generator_id) ON DELETE CASCADE,
    CHECK (
        (health = 'unchecked' AND capability_version IS NULL AND failure_code IS NULL AND last_checked_at_ms IS NULL) OR
        (health = 'ready' AND capability_version IS NOT NULL AND failure_code IS NULL AND last_checked_at_ms IS NOT NULL) OR
        (health = 'unavailable' AND capability_version IS NULL AND failure_code IS NOT NULL AND last_checked_at_ms IS NOT NULL)
    )
) STRICT;

INSERT INTO generation_provider_models_v10 (
    generator_id, model_id, is_default, capability_version, health, failure_code, last_checked_at_ms
)
SELECT generator_id, model_id, is_default, capability_version, health, failure_code, last_checked_at_ms
FROM generation_provider_models;

DROP TABLE generation_provider_models;
DROP TABLE generation_provider_profiles;
ALTER TABLE generation_provider_profiles_v10 RENAME TO generation_provider_profiles;
ALTER TABLE generation_provider_models_v10 RENAME TO generation_provider_models;

CREATE INDEX generation_provider_profiles_enabled
    ON generation_provider_profiles(enabled, generator_id);
CREATE UNIQUE INDEX generation_provider_models_one_default
    ON generation_provider_models(generator_id) WHERE is_default = 1;
`,
	},
	{
		Version: 11,
		Name:    "select_active_agent_runtime",
		SQL: `
ALTER TABLE agent_runtime_settings
ADD COLUMN active_runtime_kind TEXT NULL
CHECK (active_runtime_kind IS NULL OR active_runtime_kind IN ('user_codex', 'shotforward'));
`,
	},
	{
		Version: 12,
		Name:    "add_script_creation_agent_template",
		SQL: `
CREATE TABLE agent_template_overrides_v12 (
    template_key                  TEXT    PRIMARY KEY NOT NULL CHECK (template_key IN (
        'script.create',
        'script.extract_assets',
        'asset.add',
        'asset.edit_prompt',
        'storyboard.plan',
        'storyboard.add_clip',
        'prompt.first_frame',
        'prompt.last_frame',
        'prompt.video'
    )),
    content                       TEXT    NOT NULL CHECK (length(trim(content)) > 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= 0)
) STRICT;

INSERT INTO agent_template_overrides_v12 (template_key, content, updated_at_ms)
SELECT template_key, content, updated_at_ms
FROM agent_template_overrides;

DROP TABLE agent_template_overrides;
ALTER TABLE agent_template_overrides_v12 RENAME TO agent_template_overrides;
`,
	},
	{
		Version: 13,
		Name:    "add_video_model_prompt_templates",
		SQL: `
CREATE TABLE agent_template_overrides_v13 (
    template_key                  TEXT    PRIMARY KEY NOT NULL CHECK (template_key IN (
        'script.create',
        'script.extract_assets',
        'asset.add',
        'asset.edit_prompt',
        'storyboard.plan',
        'storyboard.add_clip',
        'prompt.first_frame',
        'prompt.last_frame',
        'prompt.video',
        'prompt.video.adapt.h3',
        'prompt.video.adapt.seedance',
        'prompt.video.adapt.wan3'
    )),
    content                       TEXT    NOT NULL CHECK (length(trim(content)) > 0),
    updated_at_ms                 INTEGER NOT NULL CHECK (updated_at_ms >= 0)
) STRICT;

INSERT INTO agent_template_overrides_v13 (template_key, content, updated_at_ms)
SELECT template_key, content, updated_at_ms
FROM agent_template_overrides;

DROP TABLE agent_template_overrides;
ALTER TABLE agent_template_overrides_v13 RENAME TO agent_template_overrides;
`,
	},
}
