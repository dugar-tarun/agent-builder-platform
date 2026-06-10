package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"go.temporal.io/sdk/client"

	"github.com/dugar-tarun/agent-builder-platform/internal/api"
	"github.com/dugar-tarun/agent-builder-platform/internal/registry"
	"github.com/dugar-tarun/agent-builder-platform/internal/storage/pg"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := pg.New(ctx, env("PG_DSN", "postgres://postgres:postgres@localhost:5432/agent_builder_platform?sslmode=disable"))
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(ctx); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	var temporalClient client.Client
	temporalAddr := os.Getenv("TEMPORAL_ADDRESS")
	if temporalAddr != "" {
		temporalClient, err = client.Dial(client.Options{HostPort: temporalAddr, Namespace: env("TEMPORAL_NAMESPACE", "agents")})
		if err != nil {
			log.Fatalf("connect temporal: %v", err)
		}
		defer temporalClient.Close()
	}

	server := api.NewServer(registry.NewService(store), store, temporalClient, env("TEMPORAL_TASK_QUEUE", "agent-builder-platform"))
	addr := env("CONTROLPLANE_ADDR", ":8080")
	log.Printf("controlplane listening on %s", addr)
	if err := server.Start(ctx, addr); err != nil && err != context.Canceled {
		log.Fatalf("controlplane error: %v", err)
	}
}

func env(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}
