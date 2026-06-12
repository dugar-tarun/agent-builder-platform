package pg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/dugar-tarun/agent-builder-platform/internal/registry"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
	ErrVersionNotDraft = errors.New("version not draft")
	ErrActorRequired   = errors.New("actor required")
	ErrNoActiveVersion = errors.New("no active version")
)

type Store struct {
	db *sql.DB
}

func New(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			id UUID PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			active_version_id UUID NULL,
			owners JSONB NOT NULL DEFAULT '[]'::jsonb,
			labels JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			created_by TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS agent_versions (
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
		)`,
		`CREATE TABLE IF NOT EXISTS agent_version_changes (
			id UUID PRIMARY KEY,
			agent_id UUID NOT NULL REFERENCES agents(id),
			from_version_id UUID NULL REFERENCES agent_versions(id),
			to_version_id UUID NOT NULL REFERENCES agent_versions(id),
			kind TEXT NOT NULL CHECK (kind IN ('publish','rollback')),
			reason TEXT NOT NULL DEFAULT '',
			actor TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS payload_blobs (
			id UUID PRIMARY KEY,
			sha256 BYTEA NOT NULL UNIQUE,
			size_bytes BIGINT NOT NULL,
			content_type TEXT NOT NULL,
			data BYTEA NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS agent_runs (
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
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS agent_runs_temporal_idx
			ON agent_runs(temporal_workflow_id, temporal_run_id)`,
		`CREATE TABLE IF NOT EXISTS agent_run_events (
			run_id UUID NOT NULL REFERENCES agent_runs(id),
			seq BIGINT NOT NULL,
			ts TIMESTAMPTZ NOT NULL DEFAULT now(),
			node_id TEXT NULL,
			event_type TEXT NOT NULL,
			payload_blob_id UUID NULL REFERENCES payload_blobs(id),
			meta JSONB NOT NULL DEFAULT '{}'::jsonb,
			PRIMARY KEY(run_id, seq)
		)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateAgent(ctx context.Context, in registry.Agent) (registry.Agent, error) {
	ownersJSON, _ := json.Marshal(in.Owners)
	labelsJSON, _ := json.Marshal(in.Labels)

	query := `
		INSERT INTO agents (id, name, description, owners, labels)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb)
		RETURNING created_at`
	if err := s.db.QueryRowContext(ctx, query, in.ID, in.Name, in.Description, ownersJSON, labelsJSON).Scan(&in.CreatedAt); err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return registry.Agent{}, ErrConflict
		}
		return registry.Agent{}, err
	}
	return in, nil
}

func (s *Store) ListAgents(ctx context.Context) ([]registry.Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, active_version_id, owners, labels, created_at
		FROM agents
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []registry.Agent
	for rows.Next() {
		var a registry.Agent
		var ownersJSON []byte
		var labelsJSON []byte
		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.ActiveVersionID, &ownersJSON, &labelsJSON, &a.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(ownersJSON, &a.Owners)
		_ = json.Unmarshal(labelsJSON, &a.Labels)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAgentByName(ctx context.Context, name string) (registry.Agent, error) {
	var a registry.Agent
	var ownersJSON []byte
	var labelsJSON []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, active_version_id, owners, labels, created_at
		FROM agents
		WHERE name = $1`, name).
		Scan(&a.ID, &a.Name, &a.Description, &a.ActiveVersionID, &ownersJSON, &labelsJSON, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return registry.Agent{}, ErrNotFound
		}
		return registry.Agent{}, err
	}
	_ = json.Unmarshal(ownersJSON, &a.Owners)
	_ = json.Unmarshal(labelsJSON, &a.Labels)
	return a, nil
}

func (s *Store) UpdateAgent(ctx context.Context, name, description string, owners []string, labels map[string]string) (registry.Agent, error) {
	ownersJSON, _ := json.Marshal(owners)
	labelsJSON, _ := json.Marshal(labels)

	var a registry.Agent
	var outOwnersJSON []byte
	var outLabelsJSON []byte
	err := s.db.QueryRowContext(ctx, `
		UPDATE agents
		   SET description = $2, owners = $3::jsonb, labels = $4::jsonb
		 WHERE name = $1
		RETURNING id, name, description, active_version_id, owners, labels, created_at`,
		name, description, ownersJSON, labelsJSON,
	).Scan(&a.ID, &a.Name, &a.Description, &a.ActiveVersionID, &outOwnersJSON, &outLabelsJSON, &a.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return registry.Agent{}, ErrNotFound
		}
		return registry.Agent{}, err
	}
	_ = json.Unmarshal(outOwnersJSON, &a.Owners)
	_ = json.Unmarshal(outLabelsJSON, &a.Labels)
	return a, nil
}

func (s *Store) CreateVersion(ctx context.Context, in registry.Version) (registry.Version, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return registry.Version{}, err
	}
	defer tx.Rollback()

	var latestVersion int
	var parentVersionID *uuid.UUID
	row := tx.QueryRowContext(ctx, `
		SELECT version_num, id
		FROM agent_versions
		WHERE agent_id = $1
		ORDER BY version_num DESC
		LIMIT 1`, in.AgentID)
	switch err := row.Scan(&latestVersion, &parentVersionID); {
	case err == nil:
		in.VersionNum = latestVersion + 1
		in.ParentVersionID = parentVersionID
	case errors.Is(err, sql.ErrNoRows):
		in.VersionNum = 1
		in.ParentVersionID = nil
	default:
		return registry.Version{}, err
	}

	query := `
		INSERT INTO agent_versions (
			id, agent_id, version_num, parent_version_id, status, dsl_yaml, compiled_graph, compiled_hash, created_by
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9
		) RETURNING created_at`
	err = tx.QueryRowContext(
		ctx, query, in.ID, in.AgentID, in.VersionNum, in.ParentVersionID, in.Status, in.DSLYAML, in.CompiledGraph, in.CompiledHash, in.CreatedBy,
	).Scan(&in.CreatedAt)
	if err != nil {
		return registry.Version{}, err
	}

	if err := tx.Commit(); err != nil {
		return registry.Version{}, err
	}
	return in, nil
}

func (s *Store) UpdateDraftVersion(ctx context.Context, versionID uuid.UUID, rawYAML string, compiledGraph []byte, compiledHash []byte) (registry.Version, error) {
	query := `
		UPDATE agent_versions
		   SET dsl_yaml = $2, compiled_graph = $3::jsonb, compiled_hash = $4
		 WHERE id = $1 AND status = 'draft'
		RETURNING id, agent_id, version_num, parent_version_id, status, dsl_yaml, compiled_graph, compiled_hash, created_by, created_at, published_at`
	return scanVersion(s.db.QueryRowContext(ctx, query, versionID, rawYAML, compiledGraph, compiledHash))
}

func (s *Store) ListVersions(ctx context.Context, agentID uuid.UUID) ([]registry.Version, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, agent_id, version_num, parent_version_id, status, dsl_yaml, compiled_graph, compiled_hash, created_by, created_at, published_at
		FROM agent_versions
		WHERE agent_id = $1
		ORDER BY version_num DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []registry.Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetVersion(ctx context.Context, versionID uuid.UUID) (registry.Version, error) {
	query := `
		SELECT id, agent_id, version_num, parent_version_id, status, dsl_yaml, compiled_graph, compiled_hash, created_by, created_at, published_at
		FROM agent_versions
		WHERE id = $1`
	return scanVersion(s.db.QueryRowContext(ctx, query, versionID))
}

func (s *Store) GetVersionByAgentAndID(ctx context.Context, agentID uuid.UUID, versionID uuid.UUID) (registry.Version, error) {
	query := `
		SELECT id, agent_id, version_num, parent_version_id, status, dsl_yaml, compiled_graph, compiled_hash, created_by, created_at, published_at
		FROM agent_versions
		WHERE id = $1 AND agent_id = $2`
	return scanVersion(s.db.QueryRowContext(ctx, query, versionID, agentID))
}

func (s *Store) PublishVersion(ctx context.Context, versionID uuid.UUID, actor string) (registry.PublishResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return registry.PublishResult{}, err
	}
	defer tx.Rollback()

	var agentID uuid.UUID
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT agent_id, status
		FROM agent_versions
		WHERE id = $1
		FOR UPDATE`, versionID).Scan(&agentID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return registry.PublishResult{}, ErrNotFound
		}
		return registry.PublishResult{}, err
	}

	var fromVersionID *uuid.UUID
	err = tx.QueryRowContext(ctx, `
		SELECT active_version_id
		FROM agents
		WHERE id = $1
		FOR UPDATE`, agentID).Scan(&fromVersionID)
	if err != nil {
		return registry.PublishResult{}, err
	}

	if status == "published" && fromVersionID != nil && *fromVersionID == versionID {
		if err := tx.Commit(); err != nil {
			return registry.PublishResult{}, err
		}
		return registry.PublishResult{
			AgentID:         agentID,
			VersionID:       versionID,
			ActiveVersionID: versionID,
			Status:          "published",
		}, nil
	}

	if status != "draft" {
		return registry.PublishResult{}, ErrVersionNotDraft
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_versions
		   SET status = 'published', published_at = now()
		 WHERE id = $1`, versionID); err != nil {
		return registry.PublishResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE agents
		   SET active_version_id = $2
		 WHERE id = $1`, agentID, versionID); err != nil {
		return registry.PublishResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_version_changes (id, agent_id, from_version_id, to_version_id, kind, actor)
		VALUES ($1, $2, $3, $4, 'publish', $5)
	`, uuid.New(), agentID, fromVersionID, versionID, actor); err != nil {
		return registry.PublishResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return registry.PublishResult{}, err
	}
	return registry.PublishResult{
		AgentID:         agentID,
		VersionID:       versionID,
		ActiveVersionID: versionID,
		Status:          "published",
	}, nil
}

func (s *Store) Rollback(ctx context.Context, agentID uuid.UUID, toVersionID uuid.UUID, reason, actor string) (registry.RollbackResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return registry.RollbackResult{}, err
	}
	defer tx.Rollback()

	var versionStatus string
	var versionAgentID uuid.UUID
	err = tx.QueryRowContext(ctx, `
		SELECT status, agent_id
		FROM agent_versions
		WHERE id = $1
		FOR SHARE`, toVersionID).Scan(&versionStatus, &versionAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return registry.RollbackResult{}, ErrNotFound
		}
		return registry.RollbackResult{}, err
	}
	if versionAgentID != agentID || versionStatus != "published" {
		return registry.RollbackResult{}, ErrConflict
	}

	var fromVersionID *uuid.UUID
	if err := tx.QueryRowContext(ctx, `
		SELECT active_version_id
		FROM agents
		WHERE id = $1
		FOR UPDATE`, agentID).Scan(&fromVersionID); err != nil {
		return registry.RollbackResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE agents
		   SET active_version_id = $2
		 WHERE id = $1`, agentID, toVersionID); err != nil {
		return registry.RollbackResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_version_changes (id, agent_id, from_version_id, to_version_id, kind, reason, actor)
		VALUES ($1, $2, $3, $4, 'rollback', $5, $6)
	`, uuid.New(), agentID, fromVersionID, toVersionID, reason, actor); err != nil {
		return registry.RollbackResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return registry.RollbackResult{}, err
	}
	return registry.RollbackResult{
		AgentID:         agentID,
		FromVersionID:   fromVersionID,
		ToVersionID:     toVersionID,
		ActiveVersionID: toVersionID,
	}, nil
}

type Run struct {
	ID                 uuid.UUID       `json:"id"`
	AgentName          string          `json:"agentName,omitempty"`
	AgentID            uuid.UUID       `json:"agentId"`
	VersionID          uuid.UUID       `json:"versionId"`
	TemporalWorkflowID string          `json:"temporalWorkflowId"`
	TemporalRunID      string          `json:"temporalRunId"`
	Status             string          `json:"status"`
	InputBlobID        *uuid.UUID      `json:"inputBlobId,omitempty"`
	OutputBlobID       *uuid.UUID      `json:"outputBlobId,omitempty"`
	Error              json.RawMessage `json:"error,omitempty"`
	StartedAt          time.Time       `json:"startedAt"`
	EndedAt            *time.Time      `json:"endedAt,omitempty"`
	TriggeredBy        string          `json:"triggeredBy"`
}

type Event struct {
	RunID     uuid.UUID       `json:"runId"`
	Seq       int64           `json:"seq"`
	TS        time.Time       `json:"ts"`
	NodeID    string          `json:"nodeId,omitempty"`
	EventType string          `json:"eventType"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	PayloadID *uuid.UUID      `json:"payloadBlobId,omitempty"`
	Meta      json.RawMessage `json:"meta,omitempty"`
}

func (s *Store) StartRun(ctx context.Context, agentName string, selectedVersionID *uuid.UUID, inputs json.RawMessage, idempotencyKey, actor string) (Run, error) {
	agent, err := s.GetAgentByName(ctx, agentName)
	if err != nil {
		return Run{}, err
	}

	versionID := uuid.Nil
	if selectedVersionID != nil {
		version, err := s.GetVersionByAgentAndID(ctx, agent.ID, *selectedVersionID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return Run{}, ErrNotFound
			}
			return Run{}, err
		}
		versionID = version.ID
	} else {
		if agent.ActiveVersionID == nil {
			return Run{}, ErrNoActiveVersion
		}
		versionID = *agent.ActiveVersionID
	}

	workflowID := buildWorkflowID(agentName, idempotencyKey)
	inputBlobID, err := s.PutBlob(ctx, inputs, "application/json")
	if err != nil {
		return Run{}, err
	}

	run := Run{
		ID:                 uuid.New(),
		AgentName:          agentName,
		AgentID:            agent.ID,
		VersionID:          versionID,
		TemporalWorkflowID: workflowID,
		Status:             "queued",
		TriggeredBy:        actor,
		InputBlobID:        &inputBlobID,
	}

	query := `
		INSERT INTO agent_runs (
			id, agent_id, version_id, temporal_workflow_id, status, input_blob_id, triggered_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (temporal_workflow_id, temporal_run_id) DO NOTHING
		RETURNING started_at`
	err = s.db.QueryRowContext(
		ctx, query, run.ID, run.AgentID, run.VersionID, run.TemporalWorkflowID, run.Status, run.InputBlobID, run.TriggeredBy,
	).Scan(&run.StartedAt)
	if err == nil {
		return run, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, err
	}

	// Duplicate start with same idempotency key - return existing run.
	existing, err := s.GetRunByWorkflowID(ctx, workflowID)
	if err != nil {
		return Run{}, err
	}
	return existing, nil
}

func (s *Store) MarkRunRunning(ctx context.Context, runID uuid.UUID, temporalRunID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs
		   SET temporal_run_id = $2
		 WHERE id = $1`, runID, temporalRunID)
	return err
}

func (s *Store) GetRun(ctx context.Context, runID uuid.UUID) (Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, `
		SELECT r.id, a.name, r.agent_id, r.version_id, r.temporal_workflow_id, r.temporal_run_id, r.status, r.input_blob_id, r.output_blob_id, r.error, r.started_at, r.ended_at, r.triggered_by
		FROM agent_runs r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.id = $1`, runID))
}

func (s *Store) ListRunsForAgent(ctx context.Context, agentName string, limit int) ([]Run, error) {
	agent, err := s.GetAgentByName(ctx, agentName)
	if err != nil {
		return nil, err
	}

	if limit <= 0 {
		limit = 25
	}
	if limit > 200 {
		limit = 200
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, a.name, r.agent_id, r.version_id, r.temporal_workflow_id, r.temporal_run_id, r.status, r.input_blob_id, r.output_blob_id, r.error, r.started_at, r.ended_at, r.triggered_by
		FROM agent_runs r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.agent_id = $1
		ORDER BY r.started_at DESC
		LIMIT $2`, agent.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Run, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) GetRunByWorkflowID(ctx context.Context, workflowID string) (Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, `
		SELECT r.id, a.name, r.agent_id, r.version_id, r.temporal_workflow_id, r.temporal_run_id, r.status, r.input_blob_id, r.output_blob_id, r.error, r.started_at, r.ended_at, r.triggered_by
		FROM agent_runs r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.temporal_workflow_id = $1
		ORDER BY r.started_at DESC
		LIMIT 1`, workflowID))
}

func (s *Store) CompleteRun(ctx context.Context, runID uuid.UUID, _ string, output json.RawMessage, runErr json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var outputBlobID *uuid.UUID
	if len(output) > 0 {
		id, err := s.putBlobTx(ctx, tx, output, "application/json")
		if err != nil {
			return err
		}
		outputBlobID = &id
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE agent_runs
		   SET output_blob_id = $2, error = $3, ended_at = now()
		 WHERE id = $1`, runID, outputBlobID, runErr)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PutBlob(ctx context.Context, payload []byte, contentType string) (uuid.UUID, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback()
	id, err := s.putBlobTx(ctx, tx, payload, contentType)
	if err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit()
}

func (s *Store) putBlobTx(ctx context.Context, tx *sql.Tx, payload []byte, contentType string) (uuid.UUID, error) {
	sum := sha256.Sum256(payload)
	var existingID uuid.UUID
	err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM payload_blobs
		WHERE sha256 = $1`, sum[:]).Scan(&existingID)
	if err == nil {
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, err
	}

	id := uuid.New()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO payload_blobs (id, sha256, size_bytes, content_type, data)
		VALUES ($1, $2, $3, $4, $5)`,
		id, sum[:], len(payload), contentType, payload,
	)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (s *Store) GetBlob(ctx context.Context, blobID uuid.UUID) ([]byte, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT data
		FROM payload_blobs
		WHERE id = $1`, blobID).Scan(&payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return payload, nil
}

func (s *Store) AppendEvent(ctx context.Context, event Event) error {
	var payloadID *uuid.UUID
	if len(event.Payload) > 0 {
		id, err := s.PutBlob(ctx, event.Payload, "application/json")
		if err != nil {
			return err
		}
		payloadID = &id
	}
	if len(event.Meta) == 0 {
		event.Meta = json.RawMessage(`{}`)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_run_events (run_id, seq, node_id, event_type, payload_blob_id, meta)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		ON CONFLICT (run_id, seq) DO NOTHING`,
		event.RunID, event.Seq, event.NodeID, event.EventType, payloadID, event.Meta,
	)
	return err
}

func (s *Store) ListTimeline(ctx context.Context, runID uuid.UUID) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.run_id, e.seq, e.ts, COALESCE(e.node_id, ''), e.event_type, e.payload_blob_id, COALESCE(e.meta, '{}'::jsonb)
		FROM agent_run_events e
		WHERE e.run_id = $1
		ORDER BY e.seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.RunID, &e.Seq, &e.TS, &e.NodeID, &e.EventType, &e.PayloadID, &e.Meta); err != nil {
			return nil, err
		}
		if e.PayloadID != nil {
			data, err := s.GetBlob(ctx, *e.PayloadID)
			if err == nil {
				e.Payload = data
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func buildWorkflowID(agentName, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return fmt.Sprintf("agent.%s.%s", agentName, enc[:24])
}

func scanVersion(row scanner) (registry.Version, error) {
	var v registry.Version
	err := row.Scan(
		&v.ID,
		&v.AgentID,
		&v.VersionNum,
		&v.ParentVersionID,
		&v.Status,
		&v.DSLYAML,
		&v.CompiledGraph,
		&v.CompiledHash,
		&v.CreatedBy,
		&v.CreatedAt,
		&v.PublishedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return registry.Version{}, ErrNotFound
		}
		return registry.Version{}, err
	}
	return v, nil
}

func scanRun(row scanner) (Run, error) {
	var r Run
	var errBytes []byte
	err := row.Scan(
		&r.ID,
		&r.AgentName,
		&r.AgentID,
		&r.VersionID,
		&r.TemporalWorkflowID,
		&r.TemporalRunID,
		&r.Status,
		&r.InputBlobID,
		&r.OutputBlobID,
		&errBytes,
		&r.StartedAt,
		&r.EndedAt,
		&r.TriggeredBy,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, ErrNotFound
		}
		return Run{}, err
	}
	if len(errBytes) > 0 {
		r.Error = json.RawMessage(errBytes)
	}
	return r, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func HashHex(b []byte) string {
	return hex.EncodeToString(b)
}
