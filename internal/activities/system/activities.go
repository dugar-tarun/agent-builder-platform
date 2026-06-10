package system

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/dugar-tarun/agent-builder-platform/internal/storage/pg"
)

type Activities struct {
	Store *pg.Store
}

func New(store *pg.Store) *Activities {
	return &Activities{Store: store}
}

func (a *Activities) LoadGraph(ctx context.Context, versionID string) ([]byte, error) {
	id, err := uuid.Parse(versionID)
	if err != nil {
		return nil, err
	}
	version, err := a.Store.GetVersion(ctx, id)
	if err != nil {
		return nil, err
	}
	return version.CompiledGraph, nil
}

func (a *Activities) LoadInputBlob(ctx context.Context, blobID string) ([]byte, error) {
	id, err := uuid.Parse(blobID)
	if err != nil {
		return nil, err
	}
	return a.Store.GetBlob(ctx, id)
}

func (a *Activities) CompleteRun(ctx context.Context, runID string, status string, output []byte, runErr []byte) error {
	id, err := uuid.Parse(runID)
	if err != nil {
		return err
	}

	var out json.RawMessage
	var errPayload json.RawMessage
	if len(output) > 0 {
		out = json.RawMessage(output)
	}
	if len(runErr) > 0 {
		errPayload = json.RawMessage(runErr)
	}
	return a.Store.CompleteRun(ctx, id, status, out, errPayload)
}
