package registry

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/dugar-tarun/agent-builder-platform/internal/dsl"
)

type Store interface {
	CreateAgent(ctx context.Context, agent Agent) (Agent, error)
	ListAgents(ctx context.Context) ([]Agent, error)
	GetAgentByName(ctx context.Context, name string) (Agent, error)
	UpdateAgent(ctx context.Context, name, description string, owners []string, labels map[string]string) (Agent, error)

	CreateVersion(ctx context.Context, version Version) (Version, error)
	UpdateDraftVersion(ctx context.Context, versionID uuid.UUID, rawYAML string, compiledGraph []byte, compiledHash []byte) (Version, error)
	ListVersions(ctx context.Context, agentID uuid.UUID) ([]Version, error)
	GetVersion(ctx context.Context, versionID uuid.UUID) (Version, error)
	GetVersionByAgentAndID(ctx context.Context, agentID uuid.UUID, versionID uuid.UUID) (Version, error)
	PublishVersion(ctx context.Context, versionID uuid.UUID, actor string) (PublishResult, error)
	Rollback(ctx context.Context, agentID uuid.UUID, toVersionID uuid.UUID, reason, actor string) (RollbackResult, error)
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

type Agent struct {
	ID              uuid.UUID
	Name            string
	Description     string
	Owners          []string
	Labels          map[string]string
	ActiveVersionID *uuid.UUID
	CreatedAt       time.Time
}

type Version struct {
	ID              uuid.UUID
	AgentID         uuid.UUID
	VersionNum      int
	ParentVersionID *uuid.UUID
	Status          string
	DSLYAML         string
	CompiledGraph   []byte
	CompiledHash    []byte
	CreatedBy       string
	CreatedAt       time.Time
	PublishedAt     *time.Time
}

type PublishResult struct {
	AgentID         uuid.UUID
	VersionID       uuid.UUID
	ActiveVersionID uuid.UUID
	Status          string
}

type RollbackResult struct {
	AgentID         uuid.UUID
	FromVersionID   *uuid.UUID
	ToVersionID     uuid.UUID
	ActiveVersionID uuid.UUID
}

func (s *Service) CreateAgent(ctx context.Context, name, description string, owners []string, labels map[string]string) (Agent, error) {
	return s.store.CreateAgent(ctx, Agent{
		ID:          uuid.New(),
		Name:        name,
		Description: description,
		Owners:      owners,
		Labels:      labels,
	})
}

func (s *Service) ListAgents(ctx context.Context) ([]Agent, error) {
	return s.store.ListAgents(ctx)
}

func (s *Service) GetAgentByName(ctx context.Context, name string) (Agent, error) {
	return s.store.GetAgentByName(ctx, name)
}

func (s *Service) UpdateAgent(ctx context.Context, name, description string, owners []string, labels map[string]string) (Agent, error) {
	return s.store.UpdateAgent(ctx, name, description, owners, labels)
}

func (s *Service) CreateVersion(ctx context.Context, agentName string, rawYAML []byte, actor string) (Version, error) {
	doc, compiled, hash, err := dsl.ParseAndCompile(rawYAML)
	if err != nil {
		return Version{}, err
	}
	if doc.Metadata.Name != agentName {
		return Version{}, dsl.ValidationError{
			Path:  "metadata.name",
			Issue: fmt.Sprintf("must match URL agent name %q", agentName),
		}
	}

	agent, err := s.store.GetAgentByName(ctx, agentName)
	if err != nil {
		return Version{}, err
	}

	compiledJSON, err := MarshalCompiledGraph(compiled)
	if err != nil {
		return Version{}, err
	}

	version := Version{
		ID:            uuid.New(),
		AgentID:       agent.ID,
		Status:        "draft",
		DSLYAML:       string(rawYAML),
		CompiledGraph: compiledJSON,
		CompiledHash:  hash,
		CreatedBy:     actor,
	}

	return s.store.CreateVersion(ctx, version)
}

func (s *Service) UpdateDraftVersion(ctx context.Context, agentName string, versionID uuid.UUID, rawYAML []byte) (Version, error) {
	doc, compiled, hash, err := dsl.ParseAndCompile(rawYAML)
	if err != nil {
		return Version{}, err
	}
	if doc.Metadata.Name != agentName {
		return Version{}, dsl.ValidationError{
			Path:  "metadata.name",
			Issue: fmt.Sprintf("must match URL agent name %q", agentName),
		}
	}

	agent, err := s.store.GetAgentByName(ctx, agentName)
	if err != nil {
		return Version{}, err
	}

	if _, err := s.store.GetVersionByAgentAndID(ctx, agent.ID, versionID); err != nil {
		return Version{}, err
	}

	compiledJSON, err := MarshalCompiledGraph(compiled)
	if err != nil {
		return Version{}, err
	}

	return s.store.UpdateDraftVersion(ctx, versionID, string(rawYAML), compiledJSON, hash)
}

func (s *Service) ListVersions(ctx context.Context, agentName string) ([]Version, error) {
	agent, err := s.store.GetAgentByName(ctx, agentName)
	if err != nil {
		return nil, err
	}
	return s.store.ListVersions(ctx, agent.ID)
}

func (s *Service) GetVersion(ctx context.Context, agentName string, versionID uuid.UUID) (Version, error) {
	agent, err := s.store.GetAgentByName(ctx, agentName)
	if err != nil {
		return Version{}, err
	}
	return s.store.GetVersionByAgentAndID(ctx, agent.ID, versionID)
}

func (s *Service) PublishVersion(ctx context.Context, versionID uuid.UUID, actor string) (PublishResult, error) {
	return s.store.PublishVersion(ctx, versionID, actor)
}

func (s *Service) Rollback(ctx context.Context, agentName string, toVersionID uuid.UUID, reason, actor string) (RollbackResult, error) {
	agent, err := s.store.GetAgentByName(ctx, agentName)
	if err != nil {
		return RollbackResult{}, err
	}
	return s.store.Rollback(ctx, agent.ID, toVersionID, reason, actor)
}

func MarshalCompiledGraph(graph *dsl.CompiledGraph) ([]byte, error) {
	// Keep one JSON encoding helper so hashing and persistence are consistent.
	return json.Marshal(graph)
}

func HexHash(hash []byte) string {
	return hex.EncodeToString(hash)
}
