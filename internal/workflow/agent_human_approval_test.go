package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/dugar-tarun/agent-builder-platform/internal/activities/system"
	"github.com/dugar-tarun/agent-builder-platform/internal/dsl"
)

func TestHumanApprovalPipelineE2E(t *testing.T) {
	pipelinePath := filepath.Join("..", "..", "examples", "pipelines", "refund-triage-human-approval.yaml")
	yamlBytes, err := os.ReadFile(pipelinePath)
	if err != nil {
		t.Fatalf("read sample pipeline: %v", err)
	}

	doc, graph, hash, err := dsl.ParseAndCompile(yamlBytes)
	if err != nil {
		t.Fatalf("parse/compile sample pipeline: %v", err)
	}

	graphJSON, _ := json.Marshal(graph)
	inputJSON := []byte(`{"ticketId":"T-12345"}`)
	sysActs := &system.Activities{}

	type event struct {
		Seq       int64
		NodeID    string
		EventType string
	}
	var timeline []event
	var completionStatus string
	var completionOutput []byte

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(AgentWorkflow)

	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string) ([]byte, error) { return graphJSON, nil },
		activity.RegisterOptions{Name: LoadGraphActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string) ([]byte, error) { return inputJSON, nil },
		activity.RegisterOptions{Name: LoadInputBlobActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string, seq int64, nodeID, eventType string, _ []byte, _ []byte) error {
			timeline = append(timeline, event{Seq: seq, NodeID: nodeID, EventType: eventType})
			return nil
		},
		activity.RegisterOptions{Name: AppendEventActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ string, status string, output []byte, _ []byte) error {
			completionStatus = status
			completionOutput = output
			return nil
		},
		activity.RegisterOptions{Name: CompleteRunActivityName},
	)

	env.RegisterActivityWithOptions(sysActs.EvaluateSwitch, activity.RegisterOptions{Name: EvalSwitchActivityName})
	env.RegisterActivityWithOptions(sysActs.ExecuteLLM, activity.RegisterOptions{Name: ExecuteLLMActivityName})
	env.RegisterActivityWithOptions(sysActs.ExecuteHTTP, activity.RegisterOptions{Name: ExecuteHTTPActivityName})
	env.RegisterActivityWithOptions(sysActs.ExecuteDB, activity.RegisterOptions{Name: ExecuteDBActivityName})
	env.RegisterActivityWithOptions(sysActs.ExecuteTransform, activity.RegisterOptions{Name: ExecuteTransformActivityName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("approval.refund", map[string]any{
			"approved": true,
			"actor":    "alice@example.com",
			"comment":  "Looks good",
		})
	}, 2*time.Second)

	env.ExecuteWorkflow(AgentWorkflow, AgentWorkflowInput{
		RunID:        "run-approval-001",
		AgentName:    doc.Metadata.Name,
		VersionID:    "version-approval-001",
		InputsBlobID: "blob-approval-001",
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
	if completionStatus != "succeeded" {
		t.Fatalf("unexpected completion status: %s", completionStatus)
	}

	expectedOutput := `{"approvedBy":"alice@example.com","decision":"approved","ticketId":"T-12345"}`
	if string(result.Output) != expectedOutput {
		t.Fatalf("unexpected workflow output: %s", string(result.Output))
	}
	if string(completionOutput) != expectedOutput {
		t.Fatalf("unexpected completion output: %s", string(completionOutput))
	}
	if len(timeline) == 0 {
		t.Fatalf("expected timeline events")
	}

	t.Logf("Pipeline: %s", pipelinePath)
	t.Logf("Compiled hash: %x", hash)
	t.Logf("Output: %s", result.Output)
	for _, e := range timeline {
		node := e.NodeID
		if node == "" {
			node = "-"
		}
		t.Logf("seq=%d event=%s node=%s", e.Seq, e.EventType, node)
	}
}
