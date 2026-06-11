package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/dugar-tarun/agent-builder-platform/internal/dsl"
	"github.com/dugar-tarun/agent-builder-platform/internal/registry"
	"github.com/dugar-tarun/agent-builder-platform/internal/storage/pg"
	agentworkflow "github.com/dugar-tarun/agent-builder-platform/internal/workflow"
)

type Server struct {
	registry  *registry.Service
	store     *pg.Store
	temporal  client.Client
	taskQueue string
}

func NewServer(reg *registry.Service, store *pg.Store, temporal client.Client, taskQueue string) *Server {
	return &Server{registry: reg, store: store, temporal: temporal, taskQueue: taskQueue}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	r.Route("/v1", func(r chi.Router) {
		r.Get("/agents", s.listAgents)
		r.Post("/agents", s.createAgent)
		r.Get("/agents/{name}", s.getAgent)
		r.Put("/agents/{name}", s.updateAgent)
		r.Post("/agents/{name}/versions", s.createVersion)
		r.Get("/agents/{name}/versions", s.listVersions)
		r.Get("/agents/{name}/versions/{id}", s.getVersion)
		r.Put("/agents/{name}/versions/{id}", s.updateVersion)
		r.Post("/versions/{id}:publish", s.publishVersion)
		r.Post("/agents/{name}:rollback", s.rollbackAgent)
		r.Get("/agents/{name}/runs", s.listRuns)
		r.Post("/agents/{name}/runs", s.startRun)
		r.Get("/runs/{id}", s.getRun)
		r.Get("/runs/{id}/timeline", s.getTimeline)
	})
	return r
}

func (s *Server) Start(ctx context.Context, addr string) error {
	server := &http.Server{Addr: addr, Handler: s.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	select {
	case <-ctx.Done():
		_ = server.Shutdown(context.Background())
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	out, err := s.registry.ListAgents(r.Context())
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	var req struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Owners      []string          `json:"owners"`
		Labels      map[string]string `json:"labels"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "name is required", nil)
		return
	}
	out, err := s.registry.CreateAgent(r.Context(), req.Name, req.Description, req.Owners, req.Labels)
	if err != nil {
		if errors.Is(err, pg.ErrConflict) {
			writeError(w, http.StatusConflict, "CONFLICT", "agent already exists", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	out, err := s.registry.GetAgentByName(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "agent not found", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	var req struct {
		Description string            `json:"description"`
		Owners      []string          `json:"owners"`
		Labels      map[string]string `json:"labels"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	out, err := s.registry.UpdateAgent(r.Context(), chi.URLParam(r, "name"), req.Description, req.Owners, req.Labels)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "agent not found", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createVersion(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	raw, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error(), nil)
		return
	}
	out, err := s.registry.CreateVersion(r.Context(), chi.URLParam(r, "name"), raw, actor)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "agent not found", nil)
			return
		}
		var vErr dsl.ValidationError
		if errors.As(err, &vErr) {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent DSL", []map[string]string{{"path": vErr.Path, "issue": vErr.Issue}})
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           out.ID,
		"agentName":    chi.URLParam(r, "name"),
		"versionNum":   out.VersionNum,
		"status":       out.Status,
		"compiledHash": registry.HexHash(out.CompiledHash),
		"createdAt":    out.CreatedAt,
	})
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	out, err := s.registry.ListVersions(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "agent not found", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid version id", nil)
		return
	}
	out, err := s.registry.GetVersion(r.Context(), chi.URLParam(r, "name"), id)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "version not found", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid version id", nil)
		return
	}
	raw, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error(), nil)
		return
	}
	out, err := s.registry.UpdateDraftVersion(r.Context(), chi.URLParam(r, "name"), id, raw)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "version not found", nil)
			return
		}
		var vErr dsl.ValidationError
		if errors.As(err, &vErr) {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid agent DSL", []map[string]string{{"path": vErr.Path, "issue": vErr.Issue}})
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) publishVersion(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if _, ok := requireIdempotencyKey(w, r); !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid version id", nil)
		return
	}
	out, err := s.registry.PublishVersion(r.Context(), id, actor)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "version not found", nil)
			return
		}
		if errors.Is(err, pg.ErrVersionNotDraft) {
			writeError(w, http.StatusConflict, "VERSION_NOT_DRAFT", "version is not draft", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versionId": out.VersionID, "agentId": out.AgentID, "status": out.Status, "activeVersionId": out.ActiveVersionID})
}

func (s *Server) rollbackAgent(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	var req struct {
		ToVersionID string `json:"toVersionId"`
		Reason      string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	to, err := uuid.Parse(req.ToVersionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid toVersionId", nil)
		return
	}
	out, err := s.registry.Rollback(r.Context(), chi.URLParam(r, "name"), to, req.Reason, actor)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "agent or version not found", nil)
			return
		}
		if errors.Is(err, pg.ErrConflict) {
			writeError(w, http.StatusConflict, "CONFLICT", "rollback target must be a published version of this agent", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	if s.temporal == nil {
		writeInternal(w, errors.New("temporal client is not configured"))
		return
	}

	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	idem, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}

	var req struct {
		VersionID string          `json:"versionId"`
		Inputs    json.RawMessage `json:"inputs"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Inputs) == 0 {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "inputs is required", nil)
		return
	}

	name := chi.URLParam(r, "name")
	var selectedVersionID *uuid.UUID
	if strings.TrimSpace(req.VersionID) != "" {
		parsedVersionID, err := uuid.Parse(strings.TrimSpace(req.VersionID))
		if err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid versionId", nil)
			return
		}
		selectedVersionID = &parsedVersionID
	}

	run, err := s.store.StartRun(r.Context(), name, selectedVersionID, req.Inputs, idem, actor)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			if selectedVersionID != nil {
				writeError(w, http.StatusNotFound, "NOT_FOUND", "agent or version not found", nil)
				return
			}
			writeError(w, http.StatusNotFound, "NOT_FOUND", "agent not found", nil)
			return
		}
		if errors.Is(err, pg.ErrNoActiveVersion) {
			writeError(w, http.StatusConflict, "CONFLICT", "agent has no active version", nil)
			return
		}
		writeInternal(w, err)
		return
	}

	if run.InputBlobID != nil {
		we, startErr := s.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
			ID:                    run.TemporalWorkflowID,
			TaskQueue:             s.taskQueue,
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		}, agentworkflow.AgentWorkflow, agentworkflow.AgentWorkflowInput{
			RunID:        run.ID.String(),
			AgentName:    name,
			VersionID:    run.VersionID.String(),
			InputsBlobID: run.InputBlobID.String(),
		})
		if startErr == nil {
			run.TemporalRunID = we.GetRunID()
		} else if _, duplicate := startErr.(*serviceerror.WorkflowExecutionAlreadyStarted); duplicate {
			if existing, getErr := s.store.GetRunByWorkflowID(r.Context(), run.TemporalWorkflowID); getErr == nil {
				run = existing
			}
		} else {
			writeInternal(w, startErr)
			return
		}
	}
	if err := s.populateTemporalRunStatus(r.Context(), &run); err != nil {
		writeInternal(w, err)
		return
	}

	w.Header().Set("Location", "/v1/runs/"+run.ID.String())
	writeJSON(w, http.StatusAccepted, map[string]any{"runId": run.ID, "versionId": run.VersionID, "temporalWorkflowId": run.TemporalWorkflowID, "status": run.Status})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := 25
	rawLimit := strings.TrimSpace(r.URL.Query().Get("limit"))
	if rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "limit must be an integer between 1 and 200", nil)
			return
		}
		limit = parsed
	}

	out, err := s.store.ListRunsForAgent(r.Context(), chi.URLParam(r, "name"), limit)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "agent not found", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	if err := s.populateTemporalRunStatuses(r.Context(), out); err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid run id", nil)
		return
	}
	out, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		if errors.Is(err, pg.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found", nil)
			return
		}
		writeInternal(w, err)
		return
	}
	if err := s.populateTemporalRunStatus(r.Context(), &out); err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTimeline(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid run id", nil)
		return
	}
	out, err := s.store.ListTimeline(r.Context(), id)
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) populateTemporalRunStatuses(ctx context.Context, runs []pg.Run) error {
	for i := range runs {
		if err := s.populateTemporalRunStatus(ctx, &runs[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) populateTemporalRunStatus(ctx context.Context, run *pg.Run) error {
	if s.temporal == nil {
		return errors.New("temporal client is not configured")
	}

	describe, err := s.temporal.DescribeWorkflowExecution(ctx, run.TemporalWorkflowID, run.TemporalRunID)
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			run.Status = "queued"
			return nil
		}
		return err
	}

	info := describe.GetWorkflowExecutionInfo()
	if info == nil {
		run.Status = "queued"
		return nil
	}

	run.Status = mapTemporalWorkflowStatus(info.GetStatus())
	if execution := info.GetExecution(); execution != nil && execution.GetRunId() != "" {
		run.TemporalRunID = execution.GetRunId()
	}
	return nil
}

func mapTemporalWorkflowStatus(status enumspb.WorkflowExecutionStatus) string {
	switch status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		return "running"
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		return "succeeded"
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:
		return "failed"
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		return "cancelled"
	case enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return "timed_out"
	default:
		return "queued"
	}
}

func requireActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	actor := strings.TrimSpace(r.Header.Get("X-Actor"))
	if actor == "" {
		writeError(w, http.StatusBadRequest, "ACTOR_REQUIRED", "X-Actor header is required", nil)
		return "", false
	}
	return actor, true
}

func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key header is required", nil)
		return "", false
	}
	return key, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", "invalid JSON body", nil)
		return false
	}
	return true
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("request body is empty")
	}
	return raw, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeInternal(w http.ResponseWriter, err error) {
	writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error(), nil)
}

func writeError(w http.ResponseWriter, status int, code, message string, details any) {
	out := map[string]any{"error": map[string]any{"code": code, "message": message}}
	if details != nil {
		out["error"].(map[string]any)["details"] = details
	}
	writeJSON(w, status, out)
}
