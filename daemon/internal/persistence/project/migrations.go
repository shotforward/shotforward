package projectstore

import sharedsqlite "github.com/shotforward/shotforward/daemon/internal/persistence/sqlite"

var migrations = []sharedsqlite.Migration{
	{
		Version: 1,
		Name:    "create_project_and_episodes",
		SQL: `
CREATE TABLE projects (
    project_id      TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(project_id)) > 0),
    singleton_key   INTEGER NOT NULL DEFAULT 1 UNIQUE CHECK (singleton_key = 1),
    name            TEXT    NOT NULL CHECK (length(trim(name)) > 0),
    description     TEXT    NOT NULL DEFAULT '',
    output_width    INTEGER NOT NULL,
    output_height   INTEGER NOT NULL,
    revision        INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms   INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms   INTEGER NOT NULL CHECK (updated_at_ms >= 0),
    CHECK (
        (output_width = 480  AND output_height = 854)  OR
        (output_width = 720  AND output_height = 1280) OR
        (output_width = 1080 AND output_height = 1920) OR
        (output_width = 854  AND output_height = 480)  OR
        (output_width = 1280 AND output_height = 720)  OR
        (output_width = 1920 AND output_height = 1080) OR
        (output_width = 480  AND output_height = 480)  OR
        (output_width = 720  AND output_height = 720)  OR
        (output_width = 1080 AND output_height = 1080)
    )
) STRICT;

CREATE TABLE episodes (
    episode_id          TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(episode_id)) > 0),
    project_id          TEXT    NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    sequence_number     INTEGER NOT NULL CHECK (sequence_number >= 1),
    title               TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    target_duration_ms  INTEGER NOT NULL CHECK (target_duration_ms > 0),
    script_markdown     TEXT    NOT NULL DEFAULT '',
    script_revision     INTEGER NOT NULL CHECK (script_revision >= 1),
    script_updated_at_ms INTEGER NOT NULL CHECK (script_updated_at_ms >= 0),
    revision            INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms       INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms       INTEGER NOT NULL CHECK (updated_at_ms >= 0),
    UNIQUE (project_id, sequence_number)
) STRICT;
`,
	},
	{
		Version: 2,
		Name:    "create_episode_assets",
		SQL: `
CREATE TABLE asset_identities (
    asset_identity_id TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(asset_identity_id)) > 0),
    project_id        TEXT    NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    kind              TEXT    NOT NULL CHECK (kind IN ('character', 'location', 'prop', 'style')),
    name              TEXT    NOT NULL CHECK (
        length(trim(name)) > 0 AND
        name = trim(name) AND
        length(name) <= 100 AND
        instr(name, char(10)) = 0 AND
        instr(name, char(13)) = 0 AND
        instr(name, char(0)) = 0
    ),
    revision          INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms     INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms     INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    UNIQUE (project_id, kind, name)
) STRICT;

CREATE TABLE assets (
    asset_id              TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(asset_id)) > 0),
    project_id            TEXT    NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id            TEXT    NOT NULL REFERENCES episodes(episode_id) ON DELETE CASCADE,
    asset_identity_id     TEXT    NOT NULL REFERENCES asset_identities(asset_identity_id) ON DELETE RESTRICT,
    derived_from_asset_id TEXT    REFERENCES assets(asset_id) ON DELETE RESTRICT,
    prompt                TEXT    NOT NULL DEFAULT '' CHECK (
        length(prompt) <= 20000 AND
        instr(prompt, char(13)) = 0 AND
        instr(prompt, char(0)) = 0
    ),
    revision              INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms         INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms         INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    deleted_at_ms         INTEGER CHECK (deleted_at_ms IS NULL OR deleted_at_ms >= created_at_ms)
) STRICT;

CREATE UNIQUE INDEX assets_active_episode_identity
ON assets (episode_id, asset_identity_id)
WHERE deleted_at_ms IS NULL;

CREATE INDEX assets_episode_active
ON assets (episode_id, deleted_at_ms);

CREATE INDEX assets_identity_history
ON assets (asset_identity_id, episode_id, created_at_ms);
`,
	},
	{
		Version: 3,
		Name:    "create_storyboards_scenes_and_clips",
		SQL: `
ALTER TABLE episodes
ADD COLUMN storyboard_revision INTEGER NOT NULL DEFAULT 1 CHECK (storyboard_revision >= 1);

CREATE TABLE scenes (
    scene_id                   TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(scene_id)) > 0),
    project_id                 TEXT    NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id                 TEXT    NOT NULL REFERENCES episodes(episode_id) ON DELETE CASCADE,
    position                   INTEGER NOT NULL CHECK (position >= 1),
    title                      TEXT    NOT NULL CHECK (
        length(trim(title)) > 0 AND title = trim(title) AND length(title) <= 200 AND
        instr(title, char(10)) = 0 AND instr(title, char(13)) = 0 AND instr(title, char(0)) = 0
    ),
    description                TEXT    NOT NULL DEFAULT '' CHECK (
        length(description) <= 20000 AND instr(description, char(13)) = 0 AND instr(description, char(0)) = 0
    ),
    source_script_revision     INTEGER,
    source_start_byte          INTEGER,
    source_end_byte            INTEGER,
    source_excerpt             TEXT,
    revision                   INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms              INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms              INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    deleted_at_ms              INTEGER CHECK (deleted_at_ms IS NULL OR deleted_at_ms >= created_at_ms),
    CHECK (
        (source_script_revision IS NULL AND source_start_byte IS NULL AND source_end_byte IS NULL AND source_excerpt IS NULL) OR
        (source_script_revision >= 1 AND source_start_byte >= 0 AND source_end_byte > source_start_byte AND
         source_excerpt IS NOT NULL AND length(source_excerpt) > 0 AND
         instr(source_excerpt, char(13)) = 0 AND instr(source_excerpt, char(0)) = 0)
    )
) STRICT;

CREATE UNIQUE INDEX scenes_active_episode_position
ON scenes (episode_id, position)
WHERE deleted_at_ms IS NULL;

CREATE INDEX scenes_episode_history
ON scenes (episode_id, deleted_at_ms, position, scene_id);

CREATE TABLE clips (
    clip_id                     TEXT    PRIMARY KEY NOT NULL CHECK (length(trim(clip_id)) > 0),
    project_id                  TEXT    NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id                  TEXT    NOT NULL REFERENCES episodes(episode_id) ON DELETE CASCADE,
    scene_id                    TEXT    NOT NULL REFERENCES scenes(scene_id) ON DELETE RESTRICT,
    position                    INTEGER NOT NULL CHECK (position >= 1),
    title                       TEXT    NOT NULL CHECK (
        length(trim(title)) > 0 AND title = trim(title) AND length(title) <= 200 AND
        instr(title, char(10)) = 0 AND instr(title, char(13)) = 0 AND instr(title, char(0)) = 0
    ),
    description                 TEXT    NOT NULL DEFAULT '' CHECK (
        length(description) <= 20000 AND instr(description, char(13)) = 0 AND instr(description, char(0)) = 0
    ),
    dialogue                    TEXT    NOT NULL DEFAULT '' CHECK (
        length(dialogue) <= 20000 AND instr(dialogue, char(13)) = 0 AND instr(dialogue, char(0)) = 0
    ),
    creative_intent             TEXT    NOT NULL DEFAULT '' CHECK (
        length(creative_intent) <= 20000 AND instr(creative_intent, char(13)) = 0 AND instr(creative_intent, char(0)) = 0
    ),
    planned_duration_seconds    INTEGER NOT NULL CHECK (planned_duration_seconds >= 1),
    first_image_prompt          TEXT    NOT NULL DEFAULT '' CHECK (
        length(first_image_prompt) <= 20000 AND instr(first_image_prompt, char(13)) = 0 AND instr(first_image_prompt, char(0)) = 0
    ),
    last_image_prompt           TEXT    NOT NULL DEFAULT '' CHECK (
        length(last_image_prompt) <= 20000 AND instr(last_image_prompt, char(13)) = 0 AND instr(last_image_prompt, char(0)) = 0
    ),
    video_prompt                TEXT    NOT NULL DEFAULT '' CHECK (
        length(video_prompt) <= 20000 AND instr(video_prompt, char(13)) = 0 AND instr(video_prompt, char(0)) = 0
    ),
    generation_mode             TEXT    NOT NULL CHECK (generation_mode IN ('first_image', 'first_last_image', 'asset_reference')),
    revision                    INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms               INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms               INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    deleted_at_ms               INTEGER CHECK (deleted_at_ms IS NULL OR deleted_at_ms >= created_at_ms),
    deleted_with_scene_id       TEXT    REFERENCES scenes(scene_id) ON DELETE RESTRICT,
    CHECK (deleted_with_scene_id IS NULL OR (deleted_at_ms IS NOT NULL AND deleted_with_scene_id = scene_id))
) STRICT;

CREATE UNIQUE INDEX clips_active_scene_position
ON clips (scene_id, position)
WHERE deleted_at_ms IS NULL;

CREATE INDEX clips_episode_history
ON clips (episode_id, deleted_at_ms, scene_id, position, clip_id);

CREATE TABLE clip_asset_references (
    clip_id     TEXT    NOT NULL REFERENCES clips(clip_id) ON DELETE RESTRICT,
    asset_id    TEXT    NOT NULL REFERENCES assets(asset_id) ON DELETE RESTRICT,
    position    INTEGER NOT NULL CHECK (position >= 1),
    PRIMARY KEY (clip_id, asset_id),
    UNIQUE (clip_id, position)
) STRICT;

CREATE INDEX clip_asset_references_asset
ON clip_asset_references (asset_id, clip_id);
`,
	},
	{
		Version: 4,
		Name:    "create_media_artifacts_slots_and_versions",
		SQL: `
CREATE TABLE audio_reference_assets (
    audio_reference_asset_id TEXT PRIMARY KEY NOT NULL CHECK (length(trim(audio_reference_asset_id)) > 0),
    project_id               TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id               TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE CASCADE,
    name                     TEXT NOT NULL CHECK (length(trim(name)) > 0 AND name = trim(name) AND length(name) <= 200 AND instr(name, char(10)) = 0 AND instr(name, char(13)) = 0 AND instr(name, char(0)) = 0),
    character_asset_id       TEXT REFERENCES assets(asset_id) ON DELETE RESTRICT,
    revision                 INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms            INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms            INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    deleted_at_ms            INTEGER CHECK (deleted_at_ms IS NULL OR deleted_at_ms >= created_at_ms)
) STRICT;

CREATE TABLE artifacts (
    artifact_id          TEXT PRIMARY KEY NOT NULL CHECK (length(trim(artifact_id)) > 0),
    project_id           TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    media_kind           TEXT NOT NULL CHECK (media_kind IN ('image', 'video', 'audio')),
    relative_path        TEXT NOT NULL UNIQUE CHECK (length(relative_path) > 0 AND substr(relative_path, 1, 1) <> '/' AND instr(relative_path, char(92)) = 0),
    sha256               TEXT NOT NULL UNIQUE CHECK (length(sha256) = 64 AND sha256 = lower(sha256)),
    byte_size            INTEGER NOT NULL CHECK (byte_size > 0),
    mime_type            TEXT NOT NULL CHECK (length(trim(mime_type)) > 0),
    original_filename    TEXT NOT NULL CHECK (length(original_filename) > 0),
    width                 INTEGER NOT NULL DEFAULT 0 CHECK (width >= 0),
    height                INTEGER NOT NULL DEFAULT 0 CHECK (height >= 0),
    duration_ms           INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    fps_numerator         INTEGER NOT NULL DEFAULT 0 CHECK (fps_numerator >= 0),
    fps_denominator       INTEGER NOT NULL DEFAULT 0 CHECK (fps_denominator >= 0),
    audio_sample_rate     INTEGER NOT NULL DEFAULT 0 CHECK (audio_sample_rate >= 0),
    audio_channels        INTEGER NOT NULL DEFAULT 0 CHECK (audio_channels >= 0),
    created_at_ms         INTEGER NOT NULL CHECK (created_at_ms >= 0)
) STRICT;

CREATE TABLE media_slots (
    media_slot_id            TEXT PRIMARY KEY NOT NULL CHECK (length(trim(media_slot_id)) > 0),
    project_id               TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id               TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE CASCADE,
    owner_kind               TEXT NOT NULL CHECK (owner_kind IN ('asset', 'clip', 'audio_reference_asset')),
    owner_id                 TEXT NOT NULL,
    slot_kind                TEXT NOT NULL CHECK (slot_kind IN ('primary_image', 'first_image', 'last_image', 'video', 'primary_audio')),
    current_media_version_id TEXT,
    revision                 INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms            INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms            INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    UNIQUE (owner_kind, owner_id, slot_kind)
) STRICT;

CREATE TABLE media_versions (
    media_version_id    TEXT PRIMARY KEY NOT NULL CHECK (length(trim(media_version_id)) > 0),
    media_slot_id       TEXT NOT NULL REFERENCES media_slots(media_slot_id) ON DELETE RESTRICT,
    artifact_id         TEXT NOT NULL REFERENCES artifacts(artifact_id) ON DELETE RESTRICT,
    sequence_number     INTEGER NOT NULL CHECK (sequence_number >= 1),
    trim_in_ms          INTEGER NOT NULL DEFAULT 0 CHECK (trim_in_ms >= 0),
    trim_out_ms         INTEGER NOT NULL DEFAULT 0 CHECK (trim_out_ms >= 0),
    revision            INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms       INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms       INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    deleted_at_ms       INTEGER CHECK (deleted_at_ms IS NULL OR deleted_at_ms >= created_at_ms),
    UNIQUE (media_slot_id, sequence_number)
) STRICT;

CREATE UNIQUE INDEX media_versions_active_slot_artifact
ON media_versions (media_slot_id, artifact_id)
WHERE deleted_at_ms IS NULL;

CREATE INDEX media_versions_slot_history
ON media_versions (media_slot_id, deleted_at_ms, sequence_number DESC);

CREATE TABLE clip_audio_references (
    clip_id                  TEXT PRIMARY KEY NOT NULL REFERENCES clips(clip_id) ON DELETE RESTRICT,
    audio_reference_asset_id TEXT NOT NULL REFERENCES audio_reference_assets(audio_reference_asset_id) ON DELETE RESTRICT
) STRICT;

INSERT INTO media_slots (media_slot_id, project_id, episode_id, owner_kind, owner_id, slot_kind, revision, created_at_ms, updated_at_ms)
SELECT 'msl_A' || substr(asset_id, 5), project_id, episode_id, 'asset', asset_id, 'primary_image', 1, created_at_ms, updated_at_ms FROM assets;

INSERT INTO media_slots (media_slot_id, project_id, episode_id, owner_kind, owner_id, slot_kind, revision, created_at_ms, updated_at_ms)
SELECT 'msl_B' || substr(clip_id, 5), project_id, episode_id, 'clip', clip_id, 'first_image', 1, created_at_ms, updated_at_ms FROM clips;

INSERT INTO media_slots (media_slot_id, project_id, episode_id, owner_kind, owner_id, slot_kind, revision, created_at_ms, updated_at_ms)
SELECT 'msl_C' || substr(clip_id, 5), project_id, episode_id, 'clip', clip_id, 'last_image', 1, created_at_ms, updated_at_ms FROM clips;

INSERT INTO media_slots (media_slot_id, project_id, episode_id, owner_kind, owner_id, slot_kind, revision, created_at_ms, updated_at_ms)
SELECT 'msl_D' || substr(clip_id, 5), project_id, episode_id, 'clip', clip_id, 'video', 1, created_at_ms, updated_at_ms FROM clips;
`,
	},
	{
		Version: 5,
		Name:    "create_generation_batches_and_jobs",
		SQL: `
CREATE TABLE generation_batches (
    generation_batch_id TEXT PRIMARY KEY NOT NULL CHECK (length(trim(generation_batch_id)) > 0),
    project_id          TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id          TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    operation_key       TEXT NOT NULL CHECK (length(operation_key) > 0 AND length(operation_key) <= 200 AND operation_key = trim(operation_key) AND instr(operation_key, char(10)) = 0 AND instr(operation_key, char(13)) = 0 AND instr(operation_key, char(0)) = 0),
    request_hash        TEXT NOT NULL CHECK (length(request_hash) = 64 AND request_hash = lower(request_hash)),
    origin              TEXT NOT NULL CHECK (origin IN ('ui','agent','mcp','retry')),
    session_id          TEXT CHECK (session_id IS NULL OR (length(session_id) > 0 AND length(session_id) <= 200)),
    turn_id             TEXT CHECK (turn_id IS NULL OR (length(turn_id) > 0 AND length(turn_id) <= 200)),
    created_at_ms       INTEGER NOT NULL CHECK (created_at_ms >= 0),
    UNIQUE (project_id, operation_key),
    CHECK ((session_id IS NULL) = (turn_id IS NULL)),
    CHECK (origin <> 'agent' OR session_id IS NOT NULL)
) STRICT;

CREATE TABLE generation_jobs (
    generation_job_id       TEXT PRIMARY KEY NOT NULL CHECK (length(trim(generation_job_id)) > 0),
    generation_batch_id     TEXT NOT NULL REFERENCES generation_batches(generation_batch_id) ON DELETE RESTRICT,
    project_id              TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id              TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    target_media_slot_id    TEXT NOT NULL REFERENCES media_slots(media_slot_id) ON DELETE RESTRICT,
    retry_of_job_id         TEXT REFERENCES generation_jobs(generation_job_id) ON DELETE RESTRICT,
    state                   TEXT NOT NULL CHECK (state IN ('queued','running','stop_requested','succeeded','failed','cancelled','stopped')),
    snapshot_json           TEXT NOT NULL CHECK (length(snapshot_json) > 0 AND json_valid(snapshot_json)),
    provider_id             TEXT NOT NULL CHECK (length(provider_id) > 0),
    model_id                TEXT NOT NULL CHECK (length(model_id) > 0),
    target_slot_revision    INTEGER NOT NULL CHECK (target_slot_revision >= 1),
    stop_supported          INTEGER NOT NULL CHECK (stop_supported IN (0,1)),
    provider_operation_id   TEXT,
    progress_stage          TEXT NOT NULL DEFAULT '',
    progress_message        TEXT NOT NULL DEFAULT '',
    result_artifact_id      TEXT REFERENCES artifacts(artifact_id) ON DELETE RESTRICT,
    result_media_version_id TEXT REFERENCES media_versions(media_version_id) ON DELETE RESTRICT,
    failure_code            TEXT NOT NULL DEFAULT '',
    failure_message         TEXT NOT NULL DEFAULT '',
    actual_credits          INTEGER NOT NULL DEFAULT 0 CHECK (actual_credits >= 0),
    revision                INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms           INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms           INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    started_at_ms           INTEGER CHECK (started_at_ms IS NULL OR started_at_ms >= created_at_ms),
    finished_at_ms          INTEGER CHECK (finished_at_ms IS NULL OR finished_at_ms >= created_at_ms),
    CHECK (retry_of_job_id IS NULL OR retry_of_job_id <> generation_job_id),
    CHECK ((result_artifact_id IS NULL) = (result_media_version_id IS NULL))
) STRICT;

CREATE UNIQUE INDEX generation_jobs_one_active_per_slot
ON generation_jobs (target_media_slot_id)
WHERE state IN ('queued','running','stop_requested');

CREATE INDEX generation_jobs_batch_order
ON generation_jobs (generation_batch_id, created_at_ms, generation_job_id);

CREATE INDEX generation_jobs_queue
ON generation_jobs (state, created_at_ms, generation_job_id);

CREATE TABLE generation_job_artifact_references (
    generation_job_id TEXT NOT NULL REFERENCES generation_jobs(generation_job_id) ON DELETE RESTRICT,
    role              TEXT NOT NULL CHECK (role IN ('first_image','last_image','asset_reference','audio_reference')),
    position          INTEGER NOT NULL CHECK (position >= 1),
    artifact_id       TEXT NOT NULL REFERENCES artifacts(artifact_id) ON DELETE RESTRICT,
    PRIMARY KEY (generation_job_id, role, position)
) STRICT;

CREATE INDEX generation_job_references_artifact
ON generation_job_artifact_references (artifact_id, generation_job_id);
`,
	},
	{
		Version: 6,
		Name:    "replace_provider_jobs_with_generator_routes",
		SQL: `
ALTER TABLE generation_job_artifact_references RENAME TO generation_job_artifact_references_v5;
ALTER TABLE generation_jobs RENAME TO generation_jobs_v5;
ALTER TABLE generation_batches RENAME TO generation_batches_v5;

DROP INDEX generation_jobs_one_active_per_slot;
DROP INDEX generation_jobs_batch_order;
DROP INDEX generation_jobs_queue;
DROP INDEX generation_job_references_artifact;

CREATE TABLE generation_batches (
    generation_batch_id TEXT PRIMARY KEY NOT NULL CHECK (length(trim(generation_batch_id)) > 0),
    project_id          TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id          TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    operation_key       TEXT NOT NULL CHECK (length(operation_key) > 0 AND length(operation_key) <= 200 AND operation_key = trim(operation_key) AND instr(operation_key, char(10)) = 0 AND instr(operation_key, char(13)) = 0 AND instr(operation_key, char(0)) = 0),
    request_hash        TEXT NOT NULL CHECK (length(request_hash) = 64 AND request_hash = lower(request_hash)),
    created_at_ms       INTEGER NOT NULL CHECK (created_at_ms >= 0),
    UNIQUE (project_id, operation_key)
) STRICT;

INSERT INTO generation_batches (generation_batch_id,project_id,episode_id,operation_key,request_hash,created_at_ms)
SELECT generation_batch_id,project_id,episode_id,operation_key,request_hash,created_at_ms
FROM generation_batches_v5;

CREATE TABLE generation_jobs (
    generation_job_id       TEXT PRIMARY KEY NOT NULL CHECK (length(trim(generation_job_id)) > 0),
    generation_batch_id     TEXT NOT NULL REFERENCES generation_batches(generation_batch_id) ON DELETE RESTRICT,
    project_id              TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id              TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    target_media_slot_id    TEXT NOT NULL REFERENCES media_slots(media_slot_id) ON DELETE RESTRICT,
    retry_of_job_id         TEXT REFERENCES generation_jobs(generation_job_id) ON DELETE RESTRICT,
    state                   TEXT NOT NULL CHECK (state IN ('queued','running','stop_requested','succeeded','failed','cancelled','stopped')),
    snapshot_json           TEXT NOT NULL CHECK (length(snapshot_json) > 0 AND json_valid(snapshot_json)),
    generator_id            TEXT NOT NULL CHECK (generator_id LIKE 'image.%' OR generator_id LIKE 'video.%'),
    target_slot_revision    INTEGER NOT NULL CHECK (target_slot_revision >= 1),
    stop_supported          INTEGER NOT NULL CHECK (stop_supported IN (0,1)),
    generator_operation_id  TEXT,
    progress_stage          TEXT NOT NULL DEFAULT '',
    progress_message        TEXT NOT NULL DEFAULT '',
    result_artifact_id      TEXT REFERENCES artifacts(artifact_id) ON DELETE RESTRICT,
    result_media_version_id TEXT REFERENCES media_versions(media_version_id) ON DELETE RESTRICT,
    failure_code            TEXT NOT NULL DEFAULT '',
    failure_message         TEXT NOT NULL DEFAULT '',
    revision                INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms           INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms           INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    started_at_ms           INTEGER CHECK (started_at_ms IS NULL OR started_at_ms >= created_at_ms),
    finished_at_ms          INTEGER CHECK (finished_at_ms IS NULL OR finished_at_ms >= created_at_ms),
    CHECK (retry_of_job_id IS NULL OR retry_of_job_id <> generation_job_id),
    CHECK ((result_artifact_id IS NULL) = (result_media_version_id IS NULL))
) STRICT;

INSERT INTO generation_jobs (
    generation_job_id,generation_batch_id,project_id,episode_id,target_media_slot_id,retry_of_job_id,state,
    snapshot_json,generator_id,target_slot_revision,stop_supported,generator_operation_id,progress_stage,
    progress_message,result_artifact_id,result_media_version_id,failure_code,failure_message,revision,
    created_at_ms,updated_at_ms,started_at_ms,finished_at_ms
)
SELECT
    old.generation_job_id,old.generation_batch_id,old.project_id,old.episode_id,old.target_media_slot_id,
    old.retry_of_job_id,old.state,
    json_remove(
        json_set(
            old.snapshot_json,
            '$.schema_version', 2,
            '$.generator_id', json_extract(old.snapshot_json, '$.target_media_kind') || '.legacy',
            '$.generation_mode', CASE json_extract(old.snapshot_json, '$.generation_mode') WHEN 'asset_reference' THEN 'reference' ELSE json_extract(old.snapshot_json, '$.generation_mode') END,
            '$.references', json(COALESCE((
                SELECT json_group_array(json_object(
                    'role', CASE json_extract(ref.value, '$.role') WHEN 'asset_reference' THEN 'image_reference' ELSE json_extract(ref.value, '$.role') END,
                    'artifact_id', json_extract(ref.value, '$.artifact_id'),
                    'trim_start_ms', 0,
                    'trim_end_ms', CASE WHEN json_extract(ref.value, '$.role') = 'audio_reference' THEN artifact.duration_ms ELSE 0 END
                ))
                FROM json_each(json_extract(old.snapshot_json, '$.references')) AS ref
                JOIN artifacts AS artifact ON artifact.artifact_id = json_extract(ref.value, '$.artifact_id')
            ), '[]'))
        ),
        '$.provider_id','$.model_id','$.quote_id','$.estimated_credits'
    ),
    json_extract(old.snapshot_json, '$.target_media_kind') || '.legacy',
    old.target_slot_revision,old.stop_supported,old.provider_operation_id,old.progress_stage,
    old.progress_message,old.result_artifact_id,old.result_media_version_id,old.failure_code,old.failure_message,
    old.revision,old.created_at_ms,old.updated_at_ms,old.started_at_ms,old.finished_at_ms
FROM generation_jobs_v5 AS old;

CREATE UNIQUE INDEX generation_jobs_one_active_per_slot
ON generation_jobs (target_media_slot_id)
WHERE state IN ('queued','running','stop_requested');

CREATE INDEX generation_jobs_batch_order
ON generation_jobs (generation_batch_id, created_at_ms, generation_job_id);

CREATE INDEX generation_jobs_queue
ON generation_jobs (state, created_at_ms, generation_job_id);

CREATE TABLE generation_job_artifact_references (
    generation_job_id    TEXT NOT NULL REFERENCES generation_jobs(generation_job_id) ON DELETE RESTRICT,
    role                 TEXT NOT NULL CHECK (role IN ('first_image','last_image','image_reference','video_reference','audio_reference')),
    position             INTEGER NOT NULL CHECK (position >= 1),
    artifact_id          TEXT NOT NULL REFERENCES artifacts(artifact_id) ON DELETE RESTRICT,
    trim_start_ms        INTEGER NOT NULL DEFAULT 0 CHECK (trim_start_ms >= 0),
    trim_end_ms          INTEGER NOT NULL DEFAULT 0 CHECK (trim_end_ms >= trim_start_ms),
    preparation_required INTEGER NOT NULL DEFAULT 0 CHECK (preparation_required IN (0,1)),
    PRIMARY KEY (generation_job_id, role, position)
) STRICT;

INSERT INTO generation_job_artifact_references (generation_job_id,role,position,artifact_id,trim_start_ms,trim_end_ms,preparation_required)
SELECT old.generation_job_id,
       CASE old.role WHEN 'asset_reference' THEN 'image_reference' ELSE old.role END,
       old.position,old.artifact_id,0,
       CASE WHEN old.role = 'audio_reference' THEN artifact.duration_ms ELSE 0 END,
       0
FROM generation_job_artifact_references_v5 AS old
JOIN artifacts AS artifact ON artifact.artifact_id = old.artifact_id;

CREATE INDEX generation_job_references_artifact
ON generation_job_artifact_references (artifact_id, generation_job_id);

DROP TABLE generation_job_artifact_references_v5;
DROP TABLE generation_jobs_v5;
DROP TABLE generation_batches_v5;
`,
	},
	{
		Version: 7,
		Name:    "create_agent_change_sets",
		SQL: `
CREATE TABLE change_sets (
    change_set_id       TEXT PRIMARY KEY NOT NULL CHECK (length(trim(change_set_id)) > 0),
    project_id          TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id          TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    agent_session_id    TEXT NOT NULL CHECK (length(agent_session_id) BETWEEN 1 AND 200),
    agent_turn_id       TEXT NOT NULL CHECK (length(agent_turn_id) BETWEEN 1 AND 200),
    codex_thread_id     TEXT NOT NULL CHECK (length(codex_thread_id) BETWEEN 1 AND 500),
    codex_turn_id       TEXT NOT NULL CHECK (length(codex_turn_id) BETWEEN 1 AND 500),
    title               TEXT NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 200 AND title=trim(title) AND instr(title,char(10))=0 AND instr(title,char(13))=0 AND instr(title,char(0))=0),
    summary             TEXT NOT NULL DEFAULT '' CHECK (length(summary)<=2000 AND summary=trim(summary) AND instr(summary,char(13))=0 AND instr(summary,char(0))=0),
    status              TEXT NOT NULL CHECK (status IN ('draft','pending','applied','rejected','conflicted')),
    operation_count     INTEGER NOT NULL DEFAULT 0 CHECK (operation_count BETWEEN 0 AND 2000),
    revision            INTEGER NOT NULL CHECK (revision>=1),
    failure_code        TEXT NOT NULL DEFAULT '' CHECK (length(failure_code)<=100 AND instr(failure_code,char(10))=0 AND instr(failure_code,char(13))=0 AND instr(failure_code,char(0))=0),
    failure_summary     TEXT NOT NULL DEFAULT '' CHECK (length(failure_summary)<=2000 AND instr(failure_summary,char(13))=0 AND instr(failure_summary,char(0))=0),
    created_at_ms       INTEGER NOT NULL CHECK (created_at_ms>=0),
    updated_at_ms       INTEGER NOT NULL CHECK (updated_at_ms>=created_at_ms),
    finalized_at_ms     INTEGER CHECK (finalized_at_ms IS NULL OR finalized_at_ms>=created_at_ms),
    resolved_at_ms      INTEGER CHECK (resolved_at_ms IS NULL OR resolved_at_ms>=created_at_ms),
    CHECK (
        (status='draft' AND finalized_at_ms IS NULL AND resolved_at_ms IS NULL AND failure_code='' AND failure_summary='') OR
        (status='pending' AND operation_count>0 AND finalized_at_ms IS NOT NULL AND resolved_at_ms IS NULL AND failure_code='' AND failure_summary='') OR
        (status='applied' AND operation_count>0 AND finalized_at_ms IS NOT NULL AND resolved_at_ms IS NOT NULL AND failure_code='' AND failure_summary='') OR
        (status='rejected' AND operation_count>0 AND finalized_at_ms IS NOT NULL AND resolved_at_ms IS NOT NULL AND failure_code='user_rejected' AND length(failure_summary)>0) OR
        (status='conflicted' AND operation_count>0 AND finalized_at_ms IS NOT NULL AND resolved_at_ms IS NOT NULL AND length(failure_code)>0 AND length(failure_summary)>0)
    )
) STRICT;

CREATE INDEX change_sets_episode_created ON change_sets (episode_id, created_at_ms DESC, change_set_id);
CREATE INDEX change_sets_pending ON change_sets (project_id, status, created_at_ms, change_set_id);

CREATE TABLE change_set_operations (
    operation_id        TEXT PRIMARY KEY NOT NULL CHECK (length(trim(operation_id))>0),
    change_set_id       TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    position            INTEGER NOT NULL CHECK (position>=1),
    operation_type      TEXT NOT NULL CHECK (operation_type IN ('update_asset_prompt','update_clip_prompts')),
    target_asset_id     TEXT REFERENCES assets(asset_id) ON DELETE RESTRICT,
    target_clip_id      TEXT REFERENCES clips(clip_id) ON DELETE RESTRICT,
    asset_prompt_before TEXT,
    asset_prompt_after  TEXT,
    clip_first_before   TEXT,
    clip_first_after    TEXT,
    clip_last_before    TEXT,
    clip_last_after     TEXT,
    clip_video_before   TEXT,
    clip_video_after    TEXT,
    created_at_ms       INTEGER NOT NULL CHECK (created_at_ms>=0),
    UNIQUE (change_set_id, position),
    CHECK (
        (operation_type='update_asset_prompt' AND target_asset_id IS NOT NULL AND target_clip_id IS NULL AND asset_prompt_before IS NOT NULL AND asset_prompt_after IS NOT NULL AND asset_prompt_before<>asset_prompt_after AND clip_first_before IS NULL AND clip_first_after IS NULL AND clip_last_before IS NULL AND clip_last_after IS NULL AND clip_video_before IS NULL AND clip_video_after IS NULL) OR
        (operation_type='update_clip_prompts' AND target_asset_id IS NULL AND target_clip_id IS NOT NULL AND asset_prompt_before IS NULL AND asset_prompt_after IS NULL AND clip_first_before IS NOT NULL AND clip_first_after IS NOT NULL AND clip_last_before IS NOT NULL AND clip_last_after IS NOT NULL AND clip_video_before IS NOT NULL AND clip_video_after IS NOT NULL AND (clip_first_before<>clip_first_after OR clip_last_before<>clip_last_after OR clip_video_before<>clip_video_after))
    )
) STRICT;

CREATE TABLE change_set_preconditions (
    precondition_id     TEXT PRIMARY KEY NOT NULL CHECK (length(trim(precondition_id))>0),
    operation_id        TEXT NOT NULL UNIQUE REFERENCES change_set_operations(operation_id) ON DELETE CASCADE,
    change_set_id       TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    target_kind         TEXT NOT NULL CHECK (target_kind IN ('asset','clip')),
    target_id           TEXT NOT NULL CHECK (length(trim(target_id))>0),
    expected_revision   INTEGER NOT NULL CHECK (expected_revision>=1),
    CHECK ((target_kind='asset' AND target_id LIKE 'ast_%') OR (target_kind='clip' AND target_id LIKE 'clp_%'))
) STRICT;

CREATE UNIQUE INDEX change_set_operations_asset_target ON change_set_operations (change_set_id,target_asset_id) WHERE target_asset_id IS NOT NULL;
CREATE UNIQUE INDEX change_set_operations_clip_target ON change_set_operations (change_set_id,target_clip_id) WHERE target_clip_id IS NOT NULL;
CREATE INDEX change_set_preconditions_target ON change_set_preconditions (target_kind, target_id, expected_revision);

CREATE TABLE change_set_conflicts (
    conflict_id         TEXT PRIMARY KEY NOT NULL CHECK (length(trim(conflict_id))>0),
    change_set_id       TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE RESTRICT,
    operation_id        TEXT REFERENCES change_set_operations(operation_id) ON DELETE RESTRICT,
    code                TEXT NOT NULL CHECK (length(trim(code)) BETWEEN 1 AND 100 AND code=trim(code) AND instr(code,char(10))=0 AND instr(code,char(13))=0 AND instr(code,char(0))=0),
    summary             TEXT NOT NULL CHECK (length(trim(summary)) BETWEEN 1 AND 2000 AND summary=trim(summary) AND instr(summary,char(13))=0 AND instr(summary,char(0))=0),
    detected_at_ms      INTEGER NOT NULL CHECK (detected_at_ms>=0)
) STRICT;

CREATE INDEX change_set_conflicts_change_set ON change_set_conflicts (change_set_id, detected_at_ms, conflict_id);

CREATE TABLE change_set_command_receipts (
    project_id          TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    operation_key       TEXT NOT NULL CHECK (length(trim(operation_key)) BETWEEN 1 AND 200 AND operation_key=trim(operation_key) AND instr(operation_key,char(10))=0 AND instr(operation_key,char(13))=0 AND instr(operation_key,char(0))=0),
    request_hash        TEXT NOT NULL CHECK (length(request_hash)=64 AND request_hash=lower(request_hash)),
    command_type        TEXT NOT NULL CHECK (command_type IN ('begin','append','finalize','apply','reject')),
    change_set_id       TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    created_at_ms       INTEGER NOT NULL CHECK (created_at_ms>=0),
    PRIMARY KEY (project_id, operation_key)
) STRICT;

CREATE TRIGGER change_sets_identity_immutable BEFORE UPDATE ON change_sets
WHEN OLD.change_set_id IS NOT NEW.change_set_id OR OLD.project_id IS NOT NEW.project_id OR OLD.episode_id IS NOT NEW.episode_id OR OLD.agent_session_id IS NOT NEW.agent_session_id OR OLD.agent_turn_id IS NOT NEW.agent_turn_id OR OLD.codex_thread_id IS NOT NEW.codex_thread_id OR OLD.codex_turn_id IS NOT NEW.codex_turn_id OR OLD.created_at_ms IS NOT NEW.created_at_ms
BEGIN SELECT RAISE(ABORT, 'immutable ChangeSet identity'); END;

CREATE TRIGGER change_sets_status_transition BEFORE UPDATE OF status ON change_sets
WHEN NOT (OLD.status=NEW.status OR (OLD.status='draft' AND NEW.status='pending') OR (OLD.status='pending' AND NEW.status IN ('applied','rejected','conflicted')))
BEGIN SELECT RAISE(ABORT, 'invalid ChangeSet status transition'); END;

CREATE TRIGGER change_sets_pending_content_immutable BEFORE UPDATE ON change_sets
WHEN OLD.status<>'draft' AND (OLD.title IS NOT NEW.title OR OLD.summary IS NOT NEW.summary OR OLD.operation_count IS NOT NEW.operation_count OR OLD.finalized_at_ms IS NOT NEW.finalized_at_ms)
BEGIN SELECT RAISE(ABORT, 'pending ChangeSet is immutable'); END;

CREATE TRIGGER change_sets_terminal_update_forbidden BEFORE UPDATE ON change_sets
WHEN OLD.status IN ('applied','rejected','conflicted')
BEGIN SELECT RAISE(ABORT, 'terminal ChangeSet is immutable'); END;

CREATE TRIGGER change_sets_terminal_delete_forbidden BEFORE DELETE ON change_sets
WHEN OLD.status<>'draft'
BEGIN SELECT RAISE(ABORT, 'durable ChangeSet cannot be deleted'); END;

CREATE TRIGGER change_set_operations_insert_draft_only BEFORE INSERT ON change_set_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept operations'); END;

CREATE TRIGGER change_set_operations_update_forbidden BEFORE UPDATE ON change_set_operations
BEGIN SELECT RAISE(ABORT, 'ChangeSet Operation is immutable'); END;

CREATE TRIGGER change_set_operations_delete_draft_only BEFORE DELETE ON change_set_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
BEGIN SELECT RAISE(ABORT, 'frozen ChangeSet Operation cannot be deleted'); END;

CREATE TRIGGER change_set_preconditions_insert_draft_only BEFORE INSERT ON change_set_preconditions
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT change_set_id FROM change_set_operations WHERE operation_id=NEW.operation_id)<>NEW.change_set_id OR
     NOT EXISTS (
        SELECT 1 FROM change_set_operations o
        WHERE o.operation_id=NEW.operation_id AND
        ((o.operation_type='update_asset_prompt' AND NEW.target_kind='asset' AND NEW.target_id=o.target_asset_id) OR
         (o.operation_type='update_clip_prompts' AND NEW.target_kind='clip' AND NEW.target_id=o.target_clip_id))
     )
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept preconditions'); END;

CREATE TRIGGER change_set_preconditions_update_forbidden BEFORE UPDATE ON change_set_preconditions
BEGIN SELECT RAISE(ABORT, 'ChangeSet Precondition is immutable'); END;

CREATE TRIGGER change_set_preconditions_delete_draft_only BEFORE DELETE ON change_set_preconditions
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
BEGIN SELECT RAISE(ABORT, 'frozen ChangeSet Precondition cannot be deleted'); END;
`,
	},
	{
		Version: 8,
		Name:    "add_asset_creation_change_set_operations",
		SQL: `
CREATE TABLE change_set_asset_creations (
    operation_id               TEXT PRIMARY KEY NOT NULL CHECK (length(trim(operation_id))>0),
    change_set_id              TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    position                   INTEGER NOT NULL CHECK (position>=1),
    precondition_id            TEXT NOT NULL UNIQUE CHECK (length(trim(precondition_id))>0),
    episode_id                 TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    expected_script_revision   INTEGER NOT NULL CHECK (expected_script_revision>=1),
    asset_id                   TEXT NOT NULL UNIQUE CHECK (asset_id LIKE 'ast_%'),
    asset_identity_id          TEXT NOT NULL CHECK (asset_identity_id LIKE 'asi_%'),
    source_asset_id            TEXT REFERENCES assets(asset_id) ON DELETE RESTRICT,
    source_asset_revision      INTEGER,
    inherited_media_version_id TEXT,
    asset_kind                 TEXT NOT NULL CHECK (asset_kind IN ('character','location','prop','style')),
    asset_name                 TEXT NOT NULL CHECK (
        length(trim(asset_name)) BETWEEN 1 AND 100 AND asset_name=trim(asset_name) AND
        instr(asset_name,char(10))=0 AND instr(asset_name,char(13))=0 AND instr(asset_name,char(0))=0
    ),
    asset_prompt               TEXT NOT NULL DEFAULT '' CHECK (
        length(asset_prompt)<=20000 AND instr(asset_prompt,char(13))=0 AND instr(asset_prompt,char(0))=0
    ),
    created_at_ms              INTEGER NOT NULL CHECK (created_at_ms>=0),
    UNIQUE (change_set_id, position),
    UNIQUE (change_set_id, asset_identity_id),
    UNIQUE (change_set_id, asset_kind, asset_name),
    CHECK (
        (source_asset_id IS NULL AND source_asset_revision IS NULL AND inherited_media_version_id IS NULL) OR
        (source_asset_id IS NOT NULL AND source_asset_revision>=1 AND inherited_media_version_id LIKE 'mvr_%')
    )
) STRICT;

CREATE INDEX change_set_asset_creations_change_set
ON change_set_asset_creations (change_set_id, position);

CREATE TRIGGER change_set_asset_creations_insert_draft_only BEFORE INSERT ON change_set_asset_creations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position))
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Asset creation'); END;

CREATE TRIGGER change_set_asset_creations_update_forbidden BEFORE UPDATE ON change_set_asset_creations
BEGIN SELECT RAISE(ABORT, 'Asset creation Operation is immutable'); END;

CREATE TRIGGER change_set_asset_creations_delete_draft_only BEFORE DELETE ON change_set_asset_creations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
BEGIN SELECT RAISE(ABORT, 'frozen Asset creation Operation cannot be deleted'); END;

ALTER TABLE change_set_conflicts RENAME TO change_set_conflicts_v7;

CREATE TABLE change_set_conflicts (
    conflict_id         TEXT PRIMARY KEY NOT NULL CHECK (length(trim(conflict_id))>0),
    change_set_id       TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE RESTRICT,
    operation_id        TEXT,
    code                TEXT NOT NULL CHECK (length(trim(code)) BETWEEN 1 AND 100 AND code=trim(code) AND instr(code,char(10))=0 AND instr(code,char(13))=0 AND instr(code,char(0))=0),
    summary             TEXT NOT NULL CHECK (length(trim(summary)) BETWEEN 1 AND 2000 AND summary=trim(summary) AND instr(summary,char(10))=0 AND instr(summary,char(13))=0 AND instr(summary,char(0))=0),
    detected_at_ms      INTEGER NOT NULL CHECK (detected_at_ms>=0)
) STRICT;

INSERT INTO change_set_conflicts (conflict_id,change_set_id,operation_id,code,summary,detected_at_ms)
SELECT conflict_id,change_set_id,operation_id,code,summary,detected_at_ms
FROM change_set_conflicts_v7;

DROP TABLE change_set_conflicts_v7;

CREATE INDEX change_set_conflicts_change_set
ON change_set_conflicts (change_set_id, detected_at_ms, conflict_id);

CREATE TRIGGER change_set_conflicts_operation_scope BEFORE INSERT ON change_set_conflicts
WHEN NEW.operation_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM change_set_operations
    WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL
    SELECT 1 FROM change_set_asset_creations
    WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
)
BEGIN SELECT RAISE(ABORT, 'ChangeSet conflict Operation is outside scope'); END;
`,
	},
	{
		Version: 9,
		Name:    "record_clip_derivations",
		SQL: `
CREATE TABLE clip_derivations (
    clip_id        TEXT    NOT NULL REFERENCES clips(clip_id) ON DELETE RESTRICT,
    source_clip_id TEXT    NOT NULL REFERENCES clips(clip_id) ON DELETE RESTRICT,
    position       INTEGER NOT NULL CHECK (position>=1),
    PRIMARY KEY (clip_id, source_clip_id),
    UNIQUE (clip_id, position),
    CHECK (clip_id<>source_clip_id)
) STRICT;

CREATE INDEX clip_derivations_source
ON clip_derivations (source_clip_id, clip_id);
`,
	},
	{
		Version: 10,
		Name:    "add_storyboard_creation_change_set_operations",
		SQL: `
CREATE TABLE change_set_storyboard_operations (
    operation_id                 TEXT PRIMARY KEY NOT NULL CHECK (operation_id LIKE 'cop_%'),
    change_set_id                TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    position                     INTEGER NOT NULL CHECK (position>=1),
    precondition_id              TEXT NOT NULL UNIQUE CHECK (precondition_id LIKE 'cpc_%'),
    episode_id                   TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    expected_storyboard_revision INTEGER NOT NULL CHECK (expected_storyboard_revision>=1),
    expected_script_revision     INTEGER NOT NULL CHECK (expected_script_revision>=1),
    operation_type               TEXT NOT NULL CHECK (operation_type IN ('create_scene','create_clip','add_clip_asset_reference')),
    temporary_ref                TEXT NOT NULL CHECK (length(trim(temporary_ref)) BETWEEN 1 AND 100 AND temporary_ref=trim(temporary_ref)),
    parent_ref                   TEXT,
    scene_id                     TEXT,
    clip_id                      TEXT,
    asset_id                     TEXT REFERENCES assets(asset_id) ON DELETE RESTRICT,
    payload_json                 TEXT NOT NULL CHECK (json_valid(payload_json)),
    created_at_ms                INTEGER NOT NULL CHECK (created_at_ms>=0),
    UNIQUE (change_set_id, position),
    CHECK (
        (operation_type='create_scene' AND parent_ref IS NULL AND scene_id LIKE 'scn_%' AND clip_id IS NULL AND asset_id IS NULL) OR
        (operation_type='create_clip' AND parent_ref IS NOT NULL AND scene_id LIKE 'scn_%' AND clip_id LIKE 'clp_%' AND asset_id IS NULL) OR
        (operation_type='add_clip_asset_reference' AND parent_ref IS NOT NULL AND scene_id IS NULL AND clip_id LIKE 'clp_%' AND asset_id LIKE 'ast_%')
    )
) STRICT;

CREATE UNIQUE INDEX change_set_storyboard_temp_objects
ON change_set_storyboard_operations(change_set_id,operation_type,temporary_ref)
WHERE operation_type IN ('create_scene','create_clip');

DROP TRIGGER change_set_operations_insert_draft_only;
CREATE TRIGGER change_set_operations_insert_draft_only BEFORE INSERT ON change_set_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position))
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept operations'); END;

DROP TRIGGER change_set_asset_creations_insert_draft_only;
CREATE TRIGGER change_set_asset_creations_insert_draft_only BEFORE INSERT ON change_set_asset_creations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position))
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Asset creation'); END;

CREATE TRIGGER change_set_storyboard_operations_insert_draft_only BEFORE INSERT ON change_set_storyboard_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position))
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Storyboard creation'); END;

CREATE TRIGGER change_set_storyboard_operations_update_forbidden BEFORE UPDATE ON change_set_storyboard_operations
BEGIN SELECT RAISE(ABORT, 'Storyboard creation Operation is immutable'); END;

CREATE TRIGGER change_set_storyboard_operations_delete_draft_only BEFORE DELETE ON change_set_storyboard_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
BEGIN SELECT RAISE(ABORT, 'frozen Storyboard creation Operation cannot be deleted'); END;

DROP TRIGGER change_set_conflicts_operation_scope;
CREATE TRIGGER change_set_conflicts_operation_scope BEFORE INSERT ON change_set_conflicts
WHEN NEW.operation_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL
    SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL
    SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
)
BEGIN SELECT RAISE(ABORT, 'ChangeSet conflict Operation is outside scope'); END;
`,
	},
	{
		Version: 11,
		Name:    "add_episode_export_jobs",
		SQL: `
CREATE TABLE export_jobs (
    export_job_id          TEXT PRIMARY KEY NOT NULL CHECK (export_job_id LIKE 'exj_%'),
    project_id             TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    episode_id             TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    operation_key          TEXT NOT NULL CHECK (length(trim(operation_key)) BETWEEN 1 AND 200 AND operation_key=trim(operation_key)),
    state                  TEXT NOT NULL CHECK (state IN ('queued','running','cancel_requested','succeeded','failed','cancelled')),
    snapshot_json          TEXT NOT NULL CHECK (json_valid(snapshot_json)),
    storyboard_revision    INTEGER NOT NULL CHECK (storyboard_revision>=1),
    progress_stage         TEXT NOT NULL DEFAULT '' CHECK (length(progress_stage)<=100),
    progress_message       TEXT NOT NULL DEFAULT '' CHECK (length(progress_message)<=500),
    output_relative_path   TEXT NOT NULL DEFAULT '',
    failure_code           TEXT NOT NULL DEFAULT '' CHECK (length(failure_code)<=100),
    failure_message        TEXT NOT NULL DEFAULT '' CHECK (length(failure_message)<=2000),
    revision               INTEGER NOT NULL CHECK (revision>=1),
    created_at_ms          INTEGER NOT NULL CHECK (created_at_ms>=0),
    updated_at_ms          INTEGER NOT NULL CHECK (updated_at_ms>=created_at_ms),
    started_at_ms          INTEGER,
    finished_at_ms         INTEGER,
    UNIQUE (project_id,operation_key),
    CHECK (
        (state='queued' AND started_at_ms IS NULL AND finished_at_ms IS NULL AND output_relative_path='' AND failure_code='' AND failure_message='') OR
        (state IN ('running','cancel_requested') AND started_at_ms IS NOT NULL AND finished_at_ms IS NULL AND output_relative_path='' AND failure_code='' AND failure_message='') OR
        (state='succeeded' AND started_at_ms IS NOT NULL AND finished_at_ms IS NOT NULL AND output_relative_path LIKE 'exports/%.mp4' AND failure_code='' AND failure_message='') OR
        (state='failed' AND started_at_ms IS NOT NULL AND finished_at_ms IS NOT NULL AND output_relative_path='' AND failure_code<>'' AND failure_message<>'') OR
        (state='cancelled' AND finished_at_ms IS NOT NULL AND output_relative_path='' AND failure_code='' AND failure_message='')
    )
) STRICT;

CREATE UNIQUE INDEX export_jobs_one_active_per_episode
ON export_jobs(episode_id)
WHERE state IN ('queued','running','cancel_requested');

CREATE INDEX export_jobs_recent ON export_jobs(updated_at_ms DESC,export_job_id);
CREATE INDEX export_jobs_dispatch ON export_jobs(state,created_at_ms,export_job_id);

CREATE TRIGGER export_jobs_frozen_identity BEFORE UPDATE ON export_jobs
WHEN NEW.export_job_id<>OLD.export_job_id OR NEW.project_id<>OLD.project_id OR NEW.episode_id<>OLD.episode_id OR
     NEW.operation_key<>OLD.operation_key OR NEW.snapshot_json<>OLD.snapshot_json OR NEW.storyboard_revision<>OLD.storyboard_revision OR
     NEW.created_at_ms<>OLD.created_at_ms
BEGIN SELECT RAISE(ABORT, 'ExportJob identity and snapshot are immutable'); END;
`,
	},
	{
		Version: 12,
		Name:    "add_script_and_revert_change_set_operations",
		SQL: `
CREATE TABLE change_set_script_operations (
    operation_id             TEXT PRIMARY KEY NOT NULL CHECK (operation_id LIKE 'cop_%'),
    change_set_id            TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    position                 INTEGER NOT NULL CHECK (position>=1),
    precondition_id          TEXT NOT NULL UNIQUE CHECK (precondition_id LIKE 'cpc_%'),
    episode_id               TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    expected_script_revision INTEGER NOT NULL CHECK (expected_script_revision>=1),
    before_markdown          TEXT NOT NULL CHECK (length(before_markdown)<=50000 AND instr(before_markdown,char(13))=0 AND instr(before_markdown,char(0))=0),
    after_markdown           TEXT NOT NULL CHECK (length(after_markdown)<=50000 AND instr(after_markdown,char(13))=0 AND instr(after_markdown,char(0))=0),
    created_at_ms            INTEGER NOT NULL CHECK (created_at_ms>=0),
    UNIQUE (change_set_id,position),
    UNIQUE (change_set_id,episode_id),
    CHECK (before_markdown<>after_markdown)
) STRICT;

CREATE TABLE change_set_revert_operations (
    operation_id                 TEXT PRIMARY KEY NOT NULL CHECK (operation_id LIKE 'cop_%'),
    change_set_id                TEXT NOT NULL UNIQUE REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    position                     INTEGER NOT NULL CHECK (position=1),
    precondition_id              TEXT NOT NULL UNIQUE CHECK (precondition_id LIKE 'cpc_%'),
    original_change_set_id       TEXT NOT NULL UNIQUE REFERENCES change_sets(change_set_id) ON DELETE RESTRICT,
    expected_change_set_revision INTEGER NOT NULL CHECK (expected_change_set_revision>=1),
    created_at_ms                INTEGER NOT NULL CHECK (created_at_ms>=0),
    CHECK (change_set_id<>original_change_set_id)
) STRICT;

DROP TRIGGER change_set_operations_insert_draft_only;
CREATE TRIGGER change_set_operations_insert_draft_only BEFORE INSERT ON change_set_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept operations'); END;

DROP TRIGGER change_set_asset_creations_insert_draft_only;
CREATE TRIGGER change_set_asset_creations_insert_draft_only BEFORE INSERT ON change_set_asset_creations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Asset creation'); END;

DROP TRIGGER change_set_storyboard_operations_insert_draft_only;
CREATE TRIGGER change_set_storyboard_operations_insert_draft_only BEFORE INSERT ON change_set_storyboard_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Storyboard creation'); END;

CREATE TRIGGER change_set_script_operations_insert_draft_only BEFORE INSERT ON change_set_script_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Script replacement'); END;

CREATE TRIGGER change_set_script_operations_update_forbidden BEFORE UPDATE ON change_set_script_operations
BEGIN SELECT RAISE(ABORT, 'Script replacement Operation is immutable'); END;
CREATE TRIGGER change_set_script_operations_delete_draft_only BEFORE DELETE ON change_set_script_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
BEGIN SELECT RAISE(ABORT, 'frozen Script replacement Operation cannot be deleted'); END;

CREATE TRIGGER change_set_revert_operations_insert_draft_only BEFORE INSERT ON change_set_revert_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)<>0 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE change_set_id=NEW.change_set_id) OR
     (SELECT status FROM change_sets WHERE change_set_id=NEW.original_change_set_id)<>'applied'
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Revert operation'); END;

CREATE TRIGGER change_set_revert_operations_update_forbidden BEFORE UPDATE ON change_set_revert_operations
BEGIN SELECT RAISE(ABORT, 'Revert Operation is immutable'); END;
CREATE TRIGGER change_set_revert_operations_delete_draft_only BEFORE DELETE ON change_set_revert_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
BEGIN SELECT RAISE(ABORT, 'frozen Revert Operation cannot be deleted'); END;

DROP TRIGGER change_set_conflicts_operation_scope;
CREATE TRIGGER change_set_conflicts_operation_scope BEFORE INSERT ON change_set_conflicts
WHEN NEW.operation_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
)
BEGIN SELECT RAISE(ABORT, 'ChangeSet conflict Operation is outside scope'); END;
`,
	},
	{
		Version: 13,
		Name:    "add_scoped_change_set_history",
		SQL: `
CREATE TABLE change_set_scopes (
    change_set_id      TEXT PRIMARY KEY NOT NULL REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    scope_kind         TEXT NOT NULL CHECK (scope_kind IN ('asset','clip','scene','episode_script')),
    scope_id           TEXT NOT NULL CHECK (length(trim(scope_id))>0),
    before_guard_json  TEXT NOT NULL CHECK (json_valid(before_guard_json)),
    after_guard_json   TEXT NOT NULL CHECK (json_valid(after_guard_json)),
    undo_expired       INTEGER NOT NULL DEFAULT 0 CHECK (undo_expired IN (0,1)),
    created_at_ms      INTEGER NOT NULL CHECK (created_at_ms>=0)
) STRICT;

CREATE INDEX change_set_scopes_active
ON change_set_scopes (undo_expired,created_at_ms DESC,change_set_id);

CREATE TABLE resource_history_heads (
    resource_kind TEXT NOT NULL CHECK (resource_kind IN ('asset','clip','scene','episode_script')),
    resource_id   TEXT NOT NULL CHECK (length(trim(resource_id))>0),
    head_token    TEXT NOT NULL CHECK (length(trim(head_token))>0),
    source_kind   TEXT NOT NULL CHECK (source_kind IN ('changeset','manual','baseline')),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms>=0),
    PRIMARY KEY (resource_kind,resource_id)
) STRICT;

CREATE TABLE change_set_resource_heads (
    change_set_id TEXT NOT NULL REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    resource_kind TEXT NOT NULL CHECK (resource_kind IN ('asset','clip','scene','episode_script')),
    resource_id   TEXT NOT NULL CHECK (length(trim(resource_id))>0),
    before_head   TEXT NOT NULL,
    after_head    TEXT NOT NULL CHECK (length(trim(after_head))>0),
    PRIMARY KEY (change_set_id,resource_kind,resource_id)
) STRICT;

CREATE INDEX change_set_resource_heads_resource
ON change_set_resource_heads (resource_kind,resource_id,change_set_id);

-- Expired ChangeSets keep their lightweight audit header and scope marker, but
-- their potentially large reversible payload may be compacted.
DROP TRIGGER change_set_operations_delete_draft_only;
CREATE TRIGGER change_set_operations_delete_draft_only BEFORE DELETE ON change_set_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
 AND NOT EXISTS (SELECT 1 FROM change_set_scopes WHERE change_set_id=OLD.change_set_id AND undo_expired=1)
BEGIN SELECT RAISE(ABORT, 'frozen ChangeSet Operation cannot be deleted'); END;

DROP TRIGGER change_set_preconditions_delete_draft_only;
CREATE TRIGGER change_set_preconditions_delete_draft_only BEFORE DELETE ON change_set_preconditions
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
 AND NOT EXISTS (SELECT 1 FROM change_set_scopes WHERE change_set_id=OLD.change_set_id AND undo_expired=1)
BEGIN SELECT RAISE(ABORT, 'frozen ChangeSet Precondition cannot be deleted'); END;

DROP TRIGGER change_set_asset_creations_delete_draft_only;
CREATE TRIGGER change_set_asset_creations_delete_draft_only BEFORE DELETE ON change_set_asset_creations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
 AND NOT EXISTS (SELECT 1 FROM change_set_scopes WHERE change_set_id=OLD.change_set_id AND undo_expired=1)
BEGIN SELECT RAISE(ABORT, 'frozen Asset creation Operation cannot be deleted'); END;

DROP TRIGGER change_set_storyboard_operations_delete_draft_only;
CREATE TRIGGER change_set_storyboard_operations_delete_draft_only BEFORE DELETE ON change_set_storyboard_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
 AND NOT EXISTS (SELECT 1 FROM change_set_scopes WHERE change_set_id=OLD.change_set_id AND undo_expired=1)
BEGIN SELECT RAISE(ABORT, 'frozen Storyboard creation Operation cannot be deleted'); END;

DROP TRIGGER change_set_script_operations_delete_draft_only;
CREATE TRIGGER change_set_script_operations_delete_draft_only BEFORE DELETE ON change_set_script_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
 AND NOT EXISTS (SELECT 1 FROM change_set_scopes WHERE change_set_id=OLD.change_set_id AND undo_expired=1)
BEGIN SELECT RAISE(ABORT, 'frozen Script replacement Operation cannot be deleted'); END;
`,
	},
	{
		Version: 14,
		Name:    "make_clip_prompt_asset_bindings_atomic",
		SQL: `
ALTER TABLE change_set_operations
ADD COLUMN clip_asset_ids_before_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(clip_asset_ids_before_json));

ALTER TABLE change_set_operations
ADD COLUMN clip_asset_ids_after_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(clip_asset_ids_after_json));

ALTER TABLE change_set_operations
ADD COLUMN clip_asset_bindings_recorded INTEGER NOT NULL DEFAULT 0 CHECK (clip_asset_bindings_recorded IN (0,1));
`,
	},
	{
		Version: 15,
		Name:    "split_video_prompts_by_generation_mode",
		SQL: `
ALTER TABLE clips
ADD COLUMN first_image_video_prompt TEXT NOT NULL DEFAULT '' CHECK (
    length(first_image_video_prompt) <= 20000 AND instr(first_image_video_prompt, char(13)) = 0 AND instr(first_image_video_prompt, char(0)) = 0
);

ALTER TABLE clips
ADD COLUMN first_last_image_video_prompt TEXT NOT NULL DEFAULT '' CHECK (
    length(first_last_image_video_prompt) <= 20000 AND instr(first_last_image_video_prompt, char(13)) = 0 AND instr(first_last_image_video_prompt, char(0)) = 0
);

ALTER TABLE clips
ADD COLUMN reference_video_prompt TEXT NOT NULL DEFAULT '' CHECK (
    length(reference_video_prompt) <= 20000 AND instr(reference_video_prompt, char(13)) = 0 AND instr(reference_video_prompt, char(0)) = 0
);

UPDATE clips SET
    first_image_video_prompt = CASE WHEN generation_mode='first_image' THEN video_prompt ELSE '' END,
    first_last_image_video_prompt = CASE WHEN generation_mode='first_last_image' THEN video_prompt ELSE '' END,
    reference_video_prompt = CASE WHEN generation_mode='asset_reference' THEN video_prompt ELSE '' END;

DROP TRIGGER change_set_storyboard_operations_update_forbidden;

UPDATE change_set_storyboard_operations
SET payload_json = json_remove(
    json_set(
        payload_json,
        '$.Clip.VideoPrompts',
        json_object(
            'FirstImage', CASE WHEN json_extract(payload_json, '$.Clip.GenerationMode')='first_image' THEN coalesce(json_extract(payload_json, '$.Clip.VideoPrompt'), '') ELSE '' END,
            'FirstLastImage', CASE WHEN json_extract(payload_json, '$.Clip.GenerationMode')='first_last_image' THEN coalesce(json_extract(payload_json, '$.Clip.VideoPrompt'), '') ELSE '' END,
            'Reference', CASE WHEN json_extract(payload_json, '$.Clip.GenerationMode')='reference' THEN coalesce(json_extract(payload_json, '$.Clip.VideoPrompt'), '') ELSE '' END
        )
    ),
    '$.Clip.VideoPrompt'
)
WHERE operation_type='create_clip' AND json_type(payload_json, '$.Clip.VideoPrompt') IS NOT NULL;

CREATE TRIGGER change_set_storyboard_operations_update_forbidden BEFORE UPDATE ON change_set_storyboard_operations
BEGIN SELECT RAISE(ABORT, 'Storyboard creation Operation is immutable'); END;

ALTER TABLE change_set_operations
ADD COLUMN clip_video_mode TEXT CHECK (clip_video_mode IN ('first_image','first_last_image','reference'));

DROP TRIGGER change_set_operations_update_forbidden;

UPDATE change_set_operations
SET clip_video_mode = (
    SELECT CASE clips.generation_mode
        WHEN 'asset_reference' THEN 'reference'
        ELSE clips.generation_mode
    END
    FROM clips
    WHERE clips.clip_id=change_set_operations.target_clip_id
)
WHERE operation_type='update_clip_prompts';

CREATE TRIGGER change_set_operations_update_forbidden BEFORE UPDATE ON change_set_operations
BEGIN SELECT RAISE(ABORT, 'ChangeSet Operation is immutable'); END;

CREATE TRIGGER change_set_operations_video_mode_required BEFORE INSERT ON change_set_operations
WHEN (NEW.operation_type='update_clip_prompts' AND NEW.clip_video_mode IS NULL) OR
     (NEW.operation_type='update_asset_prompt' AND NEW.clip_video_mode IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'ChangeSet Clip Prompt mode is invalid'); END;
		`,
	},
	{
		Version: 16,
		Name:    "add_clip_reference_audio_slots",
		SQL: `
INSERT INTO media_slots (
    media_slot_id, project_id, episode_id, owner_kind, owner_id, slot_kind,
    current_media_version_id, revision, created_at_ms, updated_at_ms
)
SELECT
    'msl_F' || substr(clip_id, 5), project_id, episode_id, 'clip', clip_id,
    'primary_audio', NULL, 1, created_at_ms, updated_at_ms
FROM clips;
`,
	},
	{
		Version: 17,
		Name:    "add_episode_trash_lifecycle",
		SQL: `
ALTER TABLE episodes
ADD COLUMN deleted_at_ms INTEGER CHECK (deleted_at_ms IS NULL OR deleted_at_ms >= created_at_ms);

CREATE INDEX episodes_project_lifecycle
ON episodes (project_id, deleted_at_ms, sequence_number);
`,
	},
	{
		Version: 18,
		Name:    "add_clip_reference_media",
		SQL: `
CREATE TABLE clip_reference_media (
    clip_id       TEXT NOT NULL REFERENCES clips(clip_id) ON DELETE CASCADE,
    artifact_id   TEXT NOT NULL REFERENCES artifacts(artifact_id) ON DELETE RESTRICT,
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    deleted_at_ms INTEGER CHECK (deleted_at_ms IS NULL OR deleted_at_ms >= created_at_ms),
    PRIMARY KEY (clip_id, artifact_id)
) STRICT;

CREATE INDEX clip_reference_media_artifact
ON clip_reference_media (artifact_id, clip_id)
WHERE deleted_at_ms IS NULL;
`,
	},
	{
		Version: 19,
		Name:    "add_episode_reference_media_library",
		SQL: `
CREATE TABLE episode_reference_media (
    episode_id       TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE CASCADE,
    artifact_id      TEXT NOT NULL REFERENCES artifacts(artifact_id) ON DELETE RESTRICT,
    project_id       TEXT NOT NULL REFERENCES projects(project_id) ON DELETE CASCADE,
    name             TEXT NOT NULL CHECK (length(trim(name)) > 0 AND name = trim(name) AND length(name) <= 40 AND instr(name, '@') = 0 AND instr(name, char(10)) = 0 AND instr(name, char(13)) = 0 AND instr(name, char(0)) = 0),
    description      TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 2000 AND instr(description, char(0)) = 0),
    revision         INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms    INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms    INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    deleted_at_ms    INTEGER CHECK (deleted_at_ms IS NULL OR deleted_at_ms >= created_at_ms),
    PRIMARY KEY (episode_id, artifact_id)
) STRICT;

CREATE INDEX episode_reference_media_episode
ON episode_reference_media (episode_id, deleted_at_ms, updated_at_ms DESC);

INSERT INTO episode_reference_media (
    episode_id, artifact_id, project_id, name, description, revision,
    created_at_ms, updated_at_ms, deleted_at_ms
)
SELECT clip.episode_id,
       reference.artifact_id,
       clip.project_id,
       CASE
         WHEN length(trim(replace(replace(replace(artifact.original_filename, '@', ''), char(10), ' '), char(13), ' '))) > 0
           THEN substr(trim(replace(replace(replace(artifact.original_filename, '@', ''), char(10), ' '), char(13), ' ')), 1, 20)
         ELSE 'upload'
       END || '-' || substr(reference.artifact_id, 6, 8),
       '',
       1,
       min(reference.created_at_ms),
       max(reference.created_at_ms),
       NULL
FROM clip_reference_media AS reference
JOIN clips AS clip ON clip.clip_id=reference.clip_id
JOIN artifacts AS artifact ON artifact.artifact_id=reference.artifact_id
WHERE reference.deleted_at_ms IS NULL
GROUP BY clip.episode_id, reference.artifact_id, clip.project_id, artifact.original_filename;
		`,
	},
	{
		Version: 20,
		Name:    "add_clip_video_continuation",
		SQL: `
ALTER TABLE clips
ADD COLUMN continue_from_previous INTEGER NOT NULL DEFAULT 0 CHECK (continue_from_previous IN (0,1));
`,
	},
	{
		Version: 21,
		Name:    "add_artifact_remote_references",
		SQL: `
ALTER TABLE artifacts
ADD COLUMN remote_reference_url TEXT CHECK (
    remote_reference_url IS NULL OR (
        length(remote_reference_url) BETWEEN 1 AND 8192 AND
        remote_reference_url = trim(remote_reference_url) AND
        substr(remote_reference_url, 1, 8) = 'https://' AND
        instr(remote_reference_url, char(10)) = 0 AND
        instr(remote_reference_url, char(13)) = 0 AND
        instr(remote_reference_url, char(0)) = 0
    )
);

ALTER TABLE artifacts
ADD COLUMN remote_reference_expires_at_ms INTEGER CHECK (
    remote_reference_expires_at_ms IS NULL OR remote_reference_expires_at_ms > 0
);
		`,
	},
	{
		Version: 22,
		Name:    "add_agent_clip_merge_change_sets",
		SQL: `
CREATE TABLE change_set_clip_merge_operations (
    operation_id                 TEXT PRIMARY KEY NOT NULL CHECK (operation_id LIKE 'cop_%'),
    change_set_id                TEXT NOT NULL UNIQUE REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    position                     INTEGER NOT NULL CHECK (position=1),
    precondition_id              TEXT NOT NULL UNIQUE CHECK (precondition_id LIKE 'cpc_%'),
    episode_id                   TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE RESTRICT,
    expected_storyboard_revision INTEGER NOT NULL CHECK (expected_storyboard_revision>=1),
    payload_json                 TEXT NOT NULL CHECK (json_valid(payload_json)),
    created_at_ms                INTEGER NOT NULL CHECK (created_at_ms>=0)
) STRICT;

CREATE TRIGGER change_set_clip_merge_operations_insert_draft_only BEFORE INSERT ON change_set_clip_merge_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)<>0 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Clip merge'); END;

CREATE TRIGGER change_set_clip_merge_operations_update_forbidden BEFORE UPDATE ON change_set_clip_merge_operations
BEGIN SELECT RAISE(ABORT, 'Clip merge Operation is immutable'); END;

CREATE TRIGGER change_set_clip_merge_operations_delete_draft_only BEFORE DELETE ON change_set_clip_merge_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
 AND NOT EXISTS (SELECT 1 FROM change_set_scopes WHERE change_set_id=OLD.change_set_id AND undo_expired=1)
BEGIN SELECT RAISE(ABORT, 'frozen Clip merge Operation cannot be deleted'); END;

DROP TRIGGER change_set_operations_insert_draft_only;
CREATE TRIGGER change_set_operations_insert_draft_only BEFORE INSERT ON change_set_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_clip_merge_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept operations'); END;

DROP TRIGGER change_set_asset_creations_insert_draft_only;
CREATE TRIGGER change_set_asset_creations_insert_draft_only BEFORE INSERT ON change_set_asset_creations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_clip_merge_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Asset creation'); END;

DROP TRIGGER change_set_storyboard_operations_insert_draft_only;
CREATE TRIGGER change_set_storyboard_operations_insert_draft_only BEFORE INSERT ON change_set_storyboard_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id OR (change_set_id=NEW.change_set_id AND position=NEW.position)) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_clip_merge_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Storyboard creation'); END;

DROP TRIGGER change_set_script_operations_insert_draft_only;
CREATE TRIGGER change_set_script_operations_insert_draft_only BEFORE INSERT ON change_set_script_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)>=2000 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_clip_merge_operations WHERE operation_id=NEW.operation_id OR change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Script replacement'); END;

DROP TRIGGER change_set_revert_operations_insert_draft_only;
CREATE TRIGGER change_set_revert_operations_insert_draft_only BEFORE INSERT ON change_set_revert_operations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)<>0 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_clip_merge_operations WHERE change_set_id=NEW.change_set_id) OR
     (SELECT status FROM change_sets WHERE change_set_id=NEW.original_change_set_id)<>'applied'
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Revert operation'); END;

DROP TRIGGER change_set_conflicts_operation_scope;
CREATE TRIGGER change_set_conflicts_operation_scope BEFORE INSERT ON change_set_conflicts
WHEN NEW.operation_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_clip_merge_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
)
BEGIN SELECT RAISE(ABORT, 'ChangeSet conflict Operation is outside scope'); END;
`,
	},
	{
		Version: 23,
		Name:    "add_agent_conversation_media",
		SQL: `
CREATE TABLE agent_conversation_media (
    episode_id          TEXT NOT NULL REFERENCES episodes(episode_id) ON DELETE CASCADE,
    session_id          TEXT NOT NULL CHECK (length(session_id) BETWEEN 1 AND 200),
    client_operation_id TEXT NOT NULL DEFAULT '' CHECK (length(client_operation_id) <= 200),
    turn_id             TEXT NOT NULL DEFAULT '' CHECK (length(turn_id) <= 200),
    artifact_id         TEXT NOT NULL REFERENCES artifacts(artifact_id) ON DELETE RESTRICT,
    name                TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 200 AND name=trim(name) AND instr(name, char(10))=0 AND instr(name, char(13))=0 AND instr(name, char(0))=0),
    origin              TEXT NOT NULL CHECK (origin IN ('uploaded','asset','generated')),
    created_at_ms       INTEGER NOT NULL CHECK (created_at_ms >= 0),
    PRIMARY KEY (episode_id, session_id, artifact_id, client_operation_id, turn_id),
    CHECK (client_operation_id <> '' OR turn_id <> '')
) STRICT;

CREATE INDEX agent_conversation_media_session
ON agent_conversation_media (episode_id, session_id, created_at_ms, artifact_id);

CREATE INDEX agent_conversation_media_operation
ON agent_conversation_media (episode_id, session_id, client_operation_id)
WHERE client_operation_id <> '';
`,
	},
	{
		Version: 24,
		Name:    "add_asset_restoration_change_sets",
		SQL: `
CREATE TABLE change_set_asset_restorations (
    operation_id            TEXT PRIMARY KEY NOT NULL CHECK (operation_id LIKE 'cop_%'),
    change_set_id           TEXT NOT NULL UNIQUE REFERENCES change_sets(change_set_id) ON DELETE CASCADE,
    position                INTEGER NOT NULL CHECK (position=1),
    precondition_id         TEXT NOT NULL UNIQUE CHECK (precondition_id LIKE 'cpc_%'),
    asset_id                TEXT NOT NULL REFERENCES assets(asset_id) ON DELETE RESTRICT CHECK (asset_id LIKE 'ast_%'),
    expected_asset_revision INTEGER NOT NULL CHECK (expected_asset_revision>=1),
    created_at_ms           INTEGER NOT NULL CHECK (created_at_ms>=0)
) STRICT;

CREATE TRIGGER change_set_asset_restorations_insert_draft_only BEFORE INSERT ON change_set_asset_restorations
WHEN (SELECT status FROM change_sets WHERE change_set_id=NEW.change_set_id)<>'draft' OR
     (SELECT operation_count FROM change_sets WHERE change_set_id=NEW.change_set_id)<>0 OR
     EXISTS (SELECT 1 FROM change_set_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_asset_creations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_storyboard_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_script_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_revert_operations WHERE change_set_id=NEW.change_set_id) OR
     EXISTS (SELECT 1 FROM change_set_clip_merge_operations WHERE change_set_id=NEW.change_set_id)
BEGIN SELECT RAISE(ABORT, 'ChangeSet does not accept Asset restoration'); END;

CREATE TRIGGER change_set_asset_restorations_update_forbidden BEFORE UPDATE ON change_set_asset_restorations
BEGIN SELECT RAISE(ABORT, 'Asset restoration Operation is immutable'); END;

CREATE TRIGGER change_set_asset_restorations_delete_draft_only BEFORE DELETE ON change_set_asset_restorations
WHEN (SELECT status FROM change_sets WHERE change_set_id=OLD.change_set_id)<>'draft'
 AND NOT EXISTS (SELECT 1 FROM change_set_scopes WHERE change_set_id=OLD.change_set_id AND undo_expired=1)
BEGIN SELECT RAISE(ABORT, 'frozen Asset restoration Operation cannot be deleted'); END;

DROP TRIGGER change_set_conflicts_operation_scope;
CREATE TRIGGER change_set_conflicts_operation_scope BEFORE INSERT ON change_set_conflicts
WHEN NEW.operation_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM change_set_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_asset_creations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_asset_restorations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_storyboard_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_script_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_revert_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
    UNION ALL SELECT 1 FROM change_set_clip_merge_operations WHERE operation_id=NEW.operation_id AND change_set_id=NEW.change_set_id
)
BEGIN SELECT RAISE(ABORT, 'ChangeSet conflict Operation is outside scope'); END;
`,
	},
	{
		Version: 25,
		Name:    "record_clip_reference_media_in_prompt_change_sets",
		SQL: `
ALTER TABLE change_set_operations
ADD COLUMN clip_reference_artifact_ids_before_json TEXT NOT NULL DEFAULT '[]'
CHECK (json_valid(clip_reference_artifact_ids_before_json) AND json_type(clip_reference_artifact_ids_before_json)='array');

ALTER TABLE change_set_operations
ADD COLUMN clip_reference_artifact_ids_after_json TEXT NOT NULL DEFAULT '[]'
CHECK (json_valid(clip_reference_artifact_ids_after_json) AND json_type(clip_reference_artifact_ids_after_json)='array');

ALTER TABLE change_set_operations
ADD COLUMN clip_reference_bindings_recorded INTEGER NOT NULL DEFAULT 0
CHECK (clip_reference_bindings_recorded IN (0,1));
`,
	},
	{
		Version: 26,
		Name:    "add_episode_reference_media_trim",
		SQL: `
ALTER TABLE episode_reference_media
ADD COLUMN trim_start_ms INTEGER NOT NULL DEFAULT 0 CHECK (trim_start_ms>=0);

ALTER TABLE episode_reference_media
ADD COLUMN trim_end_ms INTEGER NOT NULL DEFAULT 0
CHECK ((trim_start_ms=0 AND trim_end_ms=0) OR trim_end_ms>trim_start_ms);
`,
	},
}
