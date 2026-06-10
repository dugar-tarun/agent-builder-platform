package workflow

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dugar-tarun/agent-builder-platform/internal/dsl"

	"go.temporal.io/sdk/workflow"
)

type NodeExecutionResult struct {
	Output []byte
	Next   string
}

type NodeHandler interface {
	Execute(ctx workflow.Context, state *runtimeState, nodeID string, node dsl.CompiledNode) (NodeExecutionResult, error)
}

type NodeHandlerFunc func(ctx workflow.Context, state *runtimeState, nodeID string, node dsl.CompiledNode) (NodeExecutionResult, error)

func (f NodeHandlerFunc) Execute(ctx workflow.Context, state *runtimeState, nodeID string, node dsl.CompiledNode) (NodeExecutionResult, error) {
	return f(ctx, state, nodeID, node)
}

type NodeRegistry struct {
	handlers map[string]NodeHandler
}

func NewNodeRegistry() *NodeRegistry {
	return &NodeRegistry{
		handlers: map[string]NodeHandler{},
	}
}

func (r *NodeRegistry) Clone() *NodeRegistry {
	cloned := NewNodeRegistry()
	for nodeType, handler := range r.handlers {
		cloned.handlers[nodeType] = handler
	}
	return cloned
}

func (r *NodeRegistry) Register(nodeType string, handler NodeHandler) error {
	nodeType = strings.TrimSpace(nodeType)
	if nodeType == "" {
		return fmt.Errorf("node type is required")
	}
	if handler == nil {
		return fmt.Errorf("node handler is required for %q", nodeType)
	}
	r.handlers[nodeType] = handler
	return nil
}

func (r *NodeRegistry) RegisterActivity(nodeType, activityName string) error {
	activityName = strings.TrimSpace(activityName)
	if activityName == "" {
		return fmt.Errorf("activity name is required for node type %q", nodeType)
	}
	return r.Register(nodeType, activityNodeHandler{activityName: activityName})
}

func (r *NodeRegistry) Unregister(nodeType string) {
	delete(r.handlers, strings.TrimSpace(nodeType))
}

func (r *NodeRegistry) Handler(nodeType string) (NodeHandler, bool) {
	handler, ok := r.handlers[strings.TrimSpace(nodeType)]
	return handler, ok
}

var (
	defaultNodeRegistryMu sync.RWMutex
	defaultNodeRegistry   = newDefaultNodeRegistry()
)

func RegisterNodeHandler(nodeType string, handler NodeHandler) error {
	defaultNodeRegistryMu.Lock()
	defer defaultNodeRegistryMu.Unlock()
	return defaultNodeRegistry.Register(nodeType, handler)
}

func RegisterActivityNodeHandler(nodeType, activityName string) error {
	defaultNodeRegistryMu.Lock()
	defer defaultNodeRegistryMu.Unlock()
	return defaultNodeRegistry.RegisterActivity(nodeType, activityName)
}

func UnregisterNodeHandler(nodeType string) {
	defaultNodeRegistryMu.Lock()
	defer defaultNodeRegistryMu.Unlock()
	defaultNodeRegistry.Unregister(nodeType)
}

func currentNodeRegistry() *NodeRegistry {
	defaultNodeRegistryMu.RLock()
	defer defaultNodeRegistryMu.RUnlock()
	return defaultNodeRegistry.Clone()
}

type activityNodeHandler struct {
	activityName string
}

func (h activityNodeHandler) Execute(ctx workflow.Context, state *runtimeState, nodeID string, node dsl.CompiledNode) (NodeExecutionResult, error) {
	var output []byte
	err := workflow.ExecuteActivity(ctx, h.activityName, nodeID, node.Config, state.Eval).Get(ctx, &output)
	if err != nil {
		return NodeExecutionResult{}, err
	}
	return NodeExecutionResult{
		Output: output,
		Next:   firstOrEmpty(node.Next),
	}, nil
}

func newDefaultNodeRegistry() *NodeRegistry {
	registry := NewNodeRegistry()

	_ = registry.Register("control.wait", NodeHandlerFunc(waitNodeHandler))
	_ = registry.Register("control.approval", NodeHandlerFunc(approvalNodeHandler))
	_ = registry.Register("control.switch", NodeHandlerFunc(switchNodeHandler))
	_ = registry.Register("control.parallel", NodeHandlerFunc(parallelNodeHandler))

	_ = registry.RegisterActivity("llm", ExecuteLLMActivityName)
	_ = registry.RegisterActivity("http", ExecuteHTTPActivityName)
	_ = registry.RegisterActivity("db", ExecuteDBActivityName)
	_ = registry.RegisterActivity("transform", ExecuteTransformActivityName)

	return registry
}

func waitNodeHandler(ctx workflow.Context, _ *runtimeState, _ string, node dsl.CompiledNode) (NodeExecutionResult, error) {
	durationMS := toInt64(node.Config["durationMs"])
	if err := workflow.Sleep(ctx, time.Duration(durationMS)*time.Millisecond); err != nil {
		return NodeExecutionResult{}, err
	}
	return NodeExecutionResult{
		Output: mustJSON(map[string]any{"waitedMs": durationMS}),
		Next:   firstOrEmpty(node.Next),
	}, nil
}

func approvalNodeHandler(ctx workflow.Context, _ *runtimeState, nodeID string, node dsl.CompiledNode) (NodeExecutionResult, error) {
	output, next, err := executeApproval(ctx, nodeID, node)
	if err != nil {
		return NodeExecutionResult{}, err
	}
	return NodeExecutionResult{
		Output: output,
		Next:   next,
	}, nil
}

func switchNodeHandler(ctx workflow.Context, state *runtimeState, nodeID string, node dsl.CompiledNode) (NodeExecutionResult, error) {
	var next string
	if err := workflow.ExecuteActivity(ctx, EvalSwitchActivityName, nodeID, node.Cases, state.Eval).Get(ctx, &next); err != nil {
		return NodeExecutionResult{}, err
	}
	return NodeExecutionResult{
		Output: mustJSON(map[string]any{"selected": next}),
		Next:   next,
	}, nil
}

func parallelNodeHandler(ctx workflow.Context, state *runtimeState, _ string, node dsl.CompiledNode) (NodeExecutionResult, error) {
	output, next, err := executeParallel(ctx, state, node)
	if err != nil {
		return NodeExecutionResult{}, err
	}
	return NodeExecutionResult{
		Output: output,
		Next:   next,
	}, nil
}
