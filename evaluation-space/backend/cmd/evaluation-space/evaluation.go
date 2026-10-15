package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/evaluator"
	"aster/evaluation-space/internal/identity"
	"aster/evaluation-space/internal/projections"
)

type evaluationConnection struct {
	InputSecretEnv string `json:"input_secret_env"`
	ID             string `json:"id"`
	Endpoint       string `json:"endpoint"`
	ClientID       string `json:"client_id"`
	SecretEnv      string `json:"secret_env"`
}
type evaluationConfig struct {
	Connections []evaluationConnection `json:"connections"`
}

func readEvaluationConnections(path string) (map[string]evaluator.Connection, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	config, err := evolution.Decode[evaluationConfig](file)
	if err != nil {
		return nil, err
	}
	if len(config.Connections) == 0 || len(config.Connections) > 32 {
		return nil, fmt.Errorf("one to 32 evaluator connections required")
	}
	envName := regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	connections := map[string]evaluator.Connection{}
	for _, connection := range config.Connections {
		if connection.ID == "" || !envName.MatchString(connection.SecretEnv) || !envName.MatchString(connection.InputSecretEnv) {
			return nil, fmt.Errorf("invalid evaluator connection reference")
		}
		if _, ok := connections[connection.ID]; ok {
			return nil, fmt.Errorf("duplicate evaluator connection")
		}
		inputSecret := os.Getenv(connection.InputSecretEnv)
		if len(inputSecret) < 32 || len(inputSecret) > 1024 {
			return nil, fmt.Errorf("provider input credential required")
		}
		value := evaluator.Connection{InputSecret: inputSecret, Endpoint: connection.Endpoint, ClientID: connection.ClientID, Secret: os.Getenv(connection.SecretEnv)}
		client, err := evaluator.NewHTTPProvider(evolution.Revision{ID: "validation", Revision: 1, Digest: evolution.Digest([]byte("validation"))}, value)
		if err != nil {
			return nil, err
		}
		client.Close()
		connections[connection.ID] = value
	}
	return connections, nil
}
func runEvaluations(ctx context.Context, store *projections.Store, authority *identity.ProjectionAuthority, connections map[string]evaluator.Connection) {
	worker := evaluator.Worker{Store: store, ID: "evaluation-worker", Authority: evaluator.CentralAuthority{Store: store, Identity: authority}, Providers: evaluator.HTTPRegistry{Catalog: store, Connections: connections}}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	after := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		items, err := store.RunnableEvaluations(ctx, after, 10)
		if err == nil {
			if len(items) == 0 {
				after = ""
			}
			for _, item := range items {
				if ctx.Err() != nil {
					return
				}
				_ = worker.Step(ctx, item.GroupID, item.JobID)
				after = item.JobID
			}
		}
		settled, err := store.UnsettledEvaluations(ctx, 10)
		if err != nil {
			continue
		}
		for _, item := range settled {
			job, err := store.Evaluation(ctx, item.GroupID, item.JobID)
			if err != nil || job.Grant == nil {
				continue
			}
			completion, err := store.EvaluationResult(ctx, item.GroupID, item.JobID)
			if err != nil {
				if job.State != "canceled" {
					continue
				}
				// Undispatched cancellation is the only local zero-spend terminal outcome.
				budget, err := store.EvaluationBudget(ctx, item.GroupID, item.JobID)
				if err != nil || budget.Held.Tokens != 0 || budget.Held.CostMicros != 0 {
					continue
				}
				completion = projections.EvaluationCompletion{Result: evolution.EvaluationResult{JobID: job.ID, Service: job.Service, Subject: job.Subject, State: "canceled", Outcome: "inconclusive"}}
			}
			if err = authority.SettleEvaluation(ctx, *job.Grant, completion); err == nil {
				_ = store.AcknowledgeEvaluationSettlement(ctx, item.GroupID, item.JobID)
			}
		}
	}
}
