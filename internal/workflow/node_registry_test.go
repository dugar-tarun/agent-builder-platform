package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dugar-tarun/agent-builder-platform/internal/dsl"
	"github.com/dugar-tarun/agent-builder-platform/internal/engine"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	tworkflow "go.temporal.io/sdk/workflow"
)

func TestNodeRegistry_RegisterUpdateUnregister(t *testing.T) {
	registry := NewNodeRegistry()
	nodeType := "custom.registry.test"

	if err := registry.Register(nodeType, NodeHandlerFunc(func(tworkflow.Context, *runtimeState, string, dsl.CompiledNode) (NodeExecutionResult, error) {
		return NodeExecutionResult{Output: mustJSON(map[string]any{"version": 1})}, nil
	})); err != nil {
		t.Fatalf("register first handler: %v", err)
	}

	first, ok := registry.Handler(nodeType)
	if !ok {
		t.Fatalf("expected handler for %s", nodeType)
	}
	result, err := first.Execute(nil, nil, "n1", dsl.CompiledNode{Type: nodeType})
	if err != nil {
		t.Fatalf("execute first handler: %v", err)
	}
	got := map[string]any{}
	_ = json.Unmarshal(result.Output, &got)
	if got["version"] != float64(1) {
		t.Fatalf("expected version 1, got %v", got["version"])
	}

	if err := registry.Register(nodeType, NodeHandlerFunc(func(tworkflow.Context, *runtimeState, string, dsl.CompiledNode) (NodeExecutionResult, error) {
		return NodeExecutionResult{Output: mustJSON(map[string]any{"version": 2})}, nil
	})); err != nil {
		t.Fatalf("update handler: %v", err)
	}

	updated, ok := registry.Handler(nodeType)
	if !ok {
		t.Fatalf("expected updated handler for %s", nodeType)
	}
	result, err = updated.Execute(nil, nil, "n1", dsl.CompiledNode{Type: nodeType})
	if err != nil {
		t.Fatalf("execute updated handler: %v", err)
	}
	got = map[string]any{}
	_ = json.Unmarshal(result.Output, &got)
	if got["version"] != float64(2) {
		t.Fatalf("expected version 2, got %v", got["version"])
	}

	registry.Unregister(nodeType)
	if _, ok := registry.Handler(nodeType); ok {
		t.Fatalf("expected handler to be removed for %s", nodeType)
	}
}

func TestAgentWorkflow_UsesCustomActivityNodeHandler(t *testing.T) {
	const (
		nodeType     = "custom.echo.test"
		activityName = "custom.ExecuteEcho"
	)

	if err := RegisterActivityNodeHandler(nodeType, activityName); err != nil {
		t.Fatalf("register custom node type: %v", err)
	}
	defer UnregisterNodeHandler(nodeType)

	graph := dsl.CompiledGraph{
		SchemaVersion: 1,
		AgentName:     "custom-activity-node-agent",
		Start:         "custom",
		Nodes: map[string]dsl.CompiledNode{
			"custom": {
				Type:   nodeType,
				Config: map[string]any{"message": "hello"},
			},
		},
	}
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(AgentWorkflow)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string) ([]byte, error) { return graphJSON, nil },
		activity.RegisterOptions{Name: LoadGraphActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string) ([]byte, error) { return []byte(`{"requestId":"r-123"}`), nil },
		activity.RegisterOptions{Name: LoadInputBlobActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string, _ string, _ []byte, _ []byte) error { return nil },
		activity.RegisterOptions{Name: CompleteRunActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string, _ int64, _ string, _ string, _ []byte, _ []byte) error { return nil },
		activity.RegisterOptions{Name: AppendEventActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string, config map[string]any, _ engine.EvalContext) ([]byte, error) {
			return json.Marshal(map[string]any{"echo": config["message"]})
		},
		activity.RegisterOptions{Name: activityName},
	)

	env.ExecuteWorkflow(AgentWorkflow, AgentWorkflowInput{
		RunID:        "run-custom-001",
		AgentName:    "custom-activity-node-agent",
		VersionID:    "version-custom-001",
		InputsBlobID: "blob-custom-001",
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow failed: %v", err)
	}

	var result AgentWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("read workflow result: %v", err)
	}
	if result.Status != "succeeded" {
		t.Fatalf("unexpected workflow status: %s", result.Status)
	}
	if string(result.Output) != `{"echo":"hello"}` {
		t.Fatalf("unexpected workflow output: %s", string(result.Output))
	}
}
