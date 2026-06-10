CREATE TABLE IF NOT EXISTS agents (
  id UUID PRIMARY KEY,
  name TEXT UNIQUE NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  active_version_id UUID NULL,
  owners JSONB NOT NULL DEFAULT '[]'::jsonb,
  labels JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_by TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS agent_versions (
  id UUID PRIMARY KEY,
  agent_id UUID NOT NULL REFERENCES agents(id),
  version_num INTEGER NOT NULL,
  parent_version_id UUID NULL REFERENCES agent_versions(id),
  status TEXT NOT NULL CHECK (status IN ('draft','published','archived')),
  dsl_yaml TEXT NOT NULL,
  compiled_graph JSONB NOT NULL,
  compiled_hash BYTEA NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at TIMESTAMPTZ NULL,
  UNIQUE(agent_id, version_num)
);

ALTER TABLE agents
  ADD CONSTRAINT IF NOT EXISTS agents_active_version_fk
  FOREIGN KEY (active_version_id) REFERENCES agent_versions(id);

CREATE TABLE IF NOT EXISTS agent_version_changes (
  id UUID PRIMARY KEY,
  agent_id UUID NOT NULL REFERENCES agents(id),
  from_version_id UUID NULL REFERENCES agent_versions(id),
  to_version_id UUID NOT NULL REFERENCES agent_versions(id),
  kind TEXT NOT NULL CHECK (kind IN ('publish','rollback')),
  reason TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS payload_blobs (
  id UUID PRIMARY KEY,
  sha256 BYTEA NOT NULL UNIQUE,
  size_bytes BIGINT NOT NULL,
  content_type TEXT NOT NULL,
  data BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_runs (
  id UUID PRIMARY KEY,
  agent_id UUID NOT NULL REFERENCES agents(id),
  version_id UUID NOT NULL REFERENCES agent_versions(id),
  temporal_workflow_id TEXT NOT NULL,
  temporal_run_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed','cancelled','timed_out')),
  input_blob_id UUID NULL REFERENCES payload_blobs(id),
  output_blob_id UUID NULL REFERENCES payload_blobs(id),
  error JSONB NULL,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ended_at TIMESTAMPTZ NULL,
  triggered_by TEXT NOT NULL DEFAULT '',
  replay_of_run_id UUID NULL REFERENCES agent_runs(id),
  batch_id UUID NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_runs_temporal_idx
  ON agent_runs(temporal_workflow_id, temporal_run_id);

CREATE TABLE IF NOT EXISTS agent_run_events (
  run_id UUID NOT NULL REFERENCES agent_runs(id),
  seq BIGINT NOT NULL,
  ts TIMESTAMPTZ NOT NULL DEFAULT now(),
  node_id TEXT NULL,
  event_type TEXT NOT NULL,
  payload_blob_id UUID NULL REFERENCES payload_blobs(id),
  meta JSONB NOT NULL DEFAULT '{}'::jsonb,
  PRIMARY KEY(run_id, seq)
);
