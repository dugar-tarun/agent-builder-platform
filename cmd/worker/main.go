package main

import (
	"context"
	"log"
	"os"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/dugar-tarun/agent-builder-platform/internal/activities/system"
	"github.com/dugar-tarun/agent-builder-platform/internal/storage/pg"
	agentworkflow "github.com/dugar-tarun/agent-builder-platform/internal/workflow"
)

func main() {
	ctx := context.Background()
	store, err := pg.New(ctx, env("PG_DSN", "postgres://postgres:postgres@localhost:5432/agent_builder_platform?sslmode=disable"))
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer store.Close()

	temporalClient, err := client.Dial(client.Options{HostPort: env("TEMPORAL_ADDRESS", "localhost:7233"), Namespace: env("TEMPORAL_NAMESPACE", "agents")})
	if err != nil {
		log.Fatalf("connect temporal: %v", err)
	}
	defer temporalClient.Close()

	taskQueue := env("TEMPORAL_TASK_QUEUE", "agent-builder-platform")
	w := worker.New(temporalClient, taskQueue, worker.Options{})
	w.RegisterWorkflow(agentworkflow.AgentWorkflow)

	acts := system.New(store)
	w.RegisterActivityWithOptions(acts.LoadGraph, activity.RegisterOptions{Name: agentworkflow.LoadGraphActivityName})
	w.RegisterActivityWithOptions(acts.LoadInputBlob, activity.RegisterOptions{Name: agentworkflow.LoadInputBlobActivityName})
	w.RegisterActivityWithOptions(acts.CompleteRun, activity.RegisterOptions{Name: agentworkflow.CompleteRunActivityName})
	w.RegisterActivityWithOptions(acts.AppendEvent, activity.RegisterOptions{Name: agentworkflow.AppendEventActivityName})
	w.RegisterActivityWithOptions(acts.EvaluateSwitch, activity.RegisterOptions{Name: agentworkflow.EvalSwitchActivityName})
	w.RegisterActivityWithOptions(acts.ExecuteLLM, activity.RegisterOptions{Name: agentworkflow.ExecuteLLMActivityName})
	w.RegisterActivityWithOptions(acts.ExecuteHTTP, activity.RegisterOptions{Name: agentworkflow.ExecuteHTTPActivityName})
	w.RegisterActivityWithOptions(acts.ExecuteDB, activity.RegisterOptions{Name: agentworkflow.ExecuteDBActivityName})
	w.RegisterActivityWithOptions(acts.ExecuteTransform, activity.RegisterOptions{Name: agentworkflow.ExecuteTransformActivityName})

	log.Printf("worker polling task queue %q", taskQueue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("worker failed: %v", err)
	}
}

func env(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}
