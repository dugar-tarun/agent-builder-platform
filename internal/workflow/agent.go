package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dugar-tarun/agent-builder-platform/internal/dsl"
	"github.com/dugar-tarun/agent-builder-platform/internal/engine"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	LoadGraphActivityName        = "system.LoadGraph"
	LoadInputBlobActivityName    = "system.LoadInputBlob"
	CompleteRunActivityName      = "system.CompleteRun"
	AppendEventActivityName      = "system.AppendEvent"
	EvalSwitchActivityName       = "system.EvaluateSwitch"
	ExecuteLLMActivityName       = "system.ExecuteLLM"
	ExecuteHTTPActivityName      = "system.ExecuteHTTP"
	ExecuteDBActivityName        = "system.ExecuteDB"
	ExecuteTransformActivityName = "system.ExecuteTransform"
)

type AgentWorkflowInput struct {
	RunID        string `json:"runId"`
	AgentName    string `json:"agentName"`
	VersionID    string `json:"versionId"`
	InputsBlobID string `json:"inputsBlobId"`
	ReplayMockID string `json:"replayMockId,omitempty"`
}

type AgentWorkflowResult struct {
	RunID    string          `json:"runId"`
	Status   string          `json:"status"`
	Output   json.RawMessage `json:"output,omitempty"`
	GraphRef string          `json:"graphRef,omitempty"`
}

type runtimeState struct {
	RunID    string
	Graph    dsl.CompiledGraph
	Eval     engine.EvalContext
	Seq      int64
	Registry *NodeRegistry
}

func AgentWorkflow(ctx workflow.Context, in AgentWorkflowInput) (AgentWorkflowResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    500 * time.Millisecond,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var graphJSON []byte
	if err := workflow.ExecuteActivity(ctx, LoadGraphActivityName, in.VersionID).Get(ctx, &graphJSON); err != nil {
		_ = workflow.ExecuteActivity(ctx, CompleteRunActivityName, in.RunID, "failed", []byte(nil), []byte(`{"message":"load graph failed"}`)).Get(ctx, nil)
		return AgentWorkflowResult{}, err
	}
	var graph dsl.CompiledGraph
	if err := json.Unmarshal(graphJSON, &graph); err != nil {
		_ = workflow.ExecuteActivity(ctx, CompleteRunActivityName, in.RunID, "failed", []byte(nil), []byte(`{"message":"invalid compiled graph json"}`)).Get(ctx, nil)
		return AgentWorkflowResult{}, err
	}

	var inputJSON []byte
	if err := workflow.ExecuteActivity(ctx, LoadInputBlobActivityName, in.InputsBlobID).Get(ctx, &inputJSON); err != nil {
		_ = workflow.ExecuteActivity(ctx, CompleteRunActivityName, in.RunID, "failed", []byte(nil), []byte(`{"message":"load input blob failed"}`)).Get(ctx, nil)
		return AgentWorkflowResult{}, err
	}

	var inputValue any
	if err := json.Unmarshal(inputJSON, &inputValue); err != nil {
		_ = workflow.ExecuteActivity(ctx, CompleteRunActivityName, in.RunID, "failed", []byte(nil), []byte(`{"message":"invalid input json"}`)).Get(ctx, nil)
		return AgentWorkflowResult{}, err
	}
	inputs, ok := inputValue.(map[string]any)
	if !ok {
		inputs = map[string]any{"value": inputValue}
	}

	state := &runtimeState{
		RunID:    in.RunID,
		Graph:    graph,
		Eval:     engine.NewEvalContext(inputs, in.RunID),
		Registry: currentNodeRegistry(),
	}

	if err := appendEvent(ctx, state, "", "run_started", inputs, nil); err != nil {
		return AgentWorkflowResult{}, err
	}

	output, runErr := runPath(ctx, state, graph.Start)
	if runErr != nil {
		_ = appendEvent(ctx, state, "", "run_failed", map[string]any{"message": runErr.Error()}, nil)
		_ = workflow.ExecuteActivity(ctx, CompleteRunActivityName, in.RunID, "failed", []byte(nil), mustJSON(map[string]any{"message": runErr.Error()})).Get(ctx, nil)
		return AgentWorkflowResult{}, runErr
	}

	var outputValue any
	_ = json.Unmarshal(output, &outputValue)
	if err := appendEvent(ctx, state, "", "run_succeeded", outputValue, nil); err != nil {
		return AgentWorkflowResult{}, err
	}
	if err := workflow.ExecuteActivity(ctx, CompleteRunActivityName, in.RunID, "succeeded", output, []byte(nil)).Get(ctx, nil); err != nil {
		return AgentWorkflowResult{}, err
	}
	return AgentWorkflowResult{
		RunID:    in.RunID,
		Status:   "succeeded",
		Output:   json.RawMessage(output),
		GraphRef: in.VersionID,
	}, nil
}

func runPath(ctx workflow.Context, state *runtimeState, startNodeID string) ([]byte, error) {
	current := startNodeID
	if current == "" {
		return nil, fmt.Errorf("start node is empty")
	}

	limit := len(state.Graph.Nodes) * 32
	if limit < 64 {
		limit = 64
	}
	for i := 0; i < limit; i++ {
		node, ok := state.Graph.Nodes[current]
		if !ok {
			return nil, fmt.Errorf("unknown node id: %s", current)
		}

		output, next, err := executeNode(ctx, state, current, node)
		if err != nil {
			return nil, err
		}
		if next == "" {
			return output, nil
		}
		current = next
	}

	return nil, fmt.Errorf("execution exceeded safety limit, possible cycle")
}

func executeNode(ctx workflow.Context, state *runtimeState, nodeID string, node dsl.CompiledNode) ([]byte, string, error) {
	if err := appendEvent(ctx, state, nodeID, "node_started", map[string]any{
		"type": node.Type,
	}, nil); err != nil {
		return nil, "", err
	}

	var (
		output []byte
		next   string
		err    error
	)

	handler, ok := state.Registry.Handler(node.Type)
	if !ok {
		err = fmt.Errorf("unknown node type: %s", node.Type)
	} else {
		var result NodeExecutionResult
		result, err = handler.Execute(ctx, state, nodeID, node)
		output = result.Output
		next = result.Next
	}

	if err != nil {
		envelope := map[string]any{
			"_error":  true,
			"message": err.Error(),
		}
		_ = appendEvent(ctx, state, nodeID, "node_failed", envelope, nil)

		onError := strings.TrimSpace(node.OnError)
		if onError == "" {
			onError = "fail"
		}
		switch {
		case onError == "continue":
			state.Eval.Nodes[nodeID] = envelope
			return mustJSON(envelope), firstOrEmpty(node.Next), nil
		case strings.HasPrefix(onError, "route:"):
			state.Eval.Nodes[nodeID] = envelope
			return mustJSON(envelope), strings.TrimSpace(strings.TrimPrefix(onError, "route:")), nil
		default:
			return nil, "", err
		}
	}

	value := unmarshalJSON(output)
	state.Eval.Nodes[nodeID] = value
	if err := appendEvent(ctx, state, nodeID, "node_succeeded", value, nil); err != nil {
		return nil, "", err
	}

	return output, next, nil
}

func executeParallel(ctx workflow.Context, state *runtimeState, node dsl.CompiledNode) ([]byte, string, error) {
	if len(node.Branches) == 0 {
		return nil, "", fmt.Errorf("control.parallel has no branches")
	}

	joinMode := strings.ToLower(strings.TrimSpace(node.Join))
	if joinMode == "" {
		joinMode = "all"
	}

	if joinMode == "any" {
		winner := node.Branches[0].To
		winnerOutput, err := runPath(ctx, state, winner)
		if err != nil {
			return nil, "", err
		}
		cancelled := make([]string, 0, len(node.Branches)-1)
		for _, b := range node.Branches[1:] {
			cancelled = append(cancelled, b.To)
		}
		return mustJSON(map[string]any{
			"winner":    winner,
			"output":    unmarshalJSON(winnerOutput),
			"cancelled": cancelled,
		}), firstOrEmpty(node.Next), nil
	}

	branches := map[string]any{}
	for _, branch := range node.Branches {
		branchOut, err := runPath(ctx, state, branch.To)
		if err != nil {
			return nil, "", err
		}
		branches[branch.To] = unmarshalJSON(branchOut)
	}
	return mustJSON(map[string]any{"branches": branches}), firstOrEmpty(node.Next), nil
}

func executeApproval(ctx workflow.Context, nodeID string, node dsl.CompiledNode) ([]byte, string, error) {
	signalName := strings.TrimSpace(fmt.Sprint(node.Config["signalName"]))
	if signalName == "" || signalName == "<nil>" {
		signalName = "approval." + nodeID
	}
	timeoutMS := toInt64(node.Config["timeoutMs"])

	signalCh := workflow.GetSignalChannel(ctx, signalName)
	approval := map[string]any{}
	received := false

	if timeoutMS > 0 {
		timerCtx, cancel := workflow.WithCancel(ctx)
		defer cancel()
		timer := workflow.NewTimer(timerCtx, time.Duration(timeoutMS)*time.Millisecond)
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(signalCh, func(c workflow.ReceiveChannel, more bool) {
			c.Receive(ctx, &approval)
			received = true
			cancel()
		})
		selector.AddFuture(timer, func(f workflow.Future) {
			_ = f.Get(ctx, nil)
		})
		selector.Select(ctx)
		if !received {
			return nil, "", fmt.Errorf("approval timeout after %dms", timeoutMS)
		}
	} else {
		signalCh.Receive(ctx, &approval)
		received = true
	}

	if !received {
		return nil, "", fmt.Errorf("approval signal not received")
	}
	if approval == nil {
		approval = map[string]any{}
	}
	if _, ok := approval["approved"]; !ok {
		approval["approved"] = true
	}
	approval["signalName"] = signalName
	return mustJSON(approval), firstOrEmpty(node.Next), nil
}

func appendEvent(ctx workflow.Context, state *runtimeState, nodeID, eventType string, payload any, meta any) error {
	seq := state.Seq
	state.Seq++
	return workflow.ExecuteActivity(
		ctx,
		AppendEventActivityName,
		state.RunID,
		seq,
		nodeID,
		eventType,
		mustJSON(payload),
		mustJSON(meta),
	).Get(ctx, nil)
}

func firstOrEmpty(next []string) string {
	if len(next) == 0 {
		return ""
	}
	return next[0]
}

func toInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	default:
		return 0
	}
}

func mustJSON(value any) []byte {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	return raw
}

func unmarshalJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return out
}
