package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"harness-platform/orchestrator/internal/config"
	"harness-platform/orchestrator/internal/events"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/retainedapp"
	"harness-platform/orchestrator/internal/server"
	"harness-platform/orchestrator/internal/store"
)

type khoraApplication struct {
	log           *slog.Logger
	app           *server.Server
	runtime       *retainedapp.Runtime
	gateways      *gateway.Remotes
	authorization *http.Server
	listener      net.Listener
	deployment    *retainedapp.Deployment
	store         *store.Store
	owner         string
}

func constructKhoraApplication(ctx context.Context, cfg config.Config, db *store.Store, owner string, deployment *retainedapp.Deployment, log *slog.Logger) (*khoraApplication, error) {
	if deployment == nil {
		return nil, fmt.Errorf("Khora startup requires deployment and indefinite session retention")
	}
	backend, gateways, err := deployment.Connect(ctx, db)
	if err != nil {
		return nil, err
	}
	app := server.New(cfg, db, events.NewHub(), log)
	options := deployment.Options(backend, gateways, owner)
	options.OnError = func(scope string, err error) {
		log.Warn("retained reconciliation failed", "scope", scope, "error", err)
	}
	runtime, err := retainedapp.Attach(ctx, app, db, options)
	if err != nil {
		gateways.Close()
		return nil, err
	}
	var listener net.Listener
	var authorization *http.Server
	if deployment.AuthorizationSocket != "" {
		listener, err = gateway.ListenAuthorization(deployment.AuthorizationSocket, deployment.AuthorizationGroup)
		if err != nil {
			runtime.Close()
			gateways.Close()
			return nil, err
		}
		authorization = &http.Server{Handler: gateway.AuthorizationHandler(db, deployment.AuthorizationUID), ConnContext: gateway.PeerContext, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: deployment.ActionTimeout, WriteTimeout: deployment.ActionTimeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	}

	return &khoraApplication{log: log, app: app, runtime: runtime, gateways: gateways, authorization: authorization, listener: listener, deployment: deployment, store: db, owner: store.GenerationLeaseOwner(owner)}, nil
}
func (k *khoraApplication) Close() {
	if k.authorization != nil {
		_ = k.authorization.Close()
	}
	if k.listener != nil {
		_ = k.listener.Close()
	}
	_ = k.runtime.Close()
	k.gateways.Close()
}
func (k *khoraApplication) Run(ctx context.Context) error {
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 4)
	go func() { results <- k.runtime.Run(run) }()
	go func() {
		results <- k.app.RunRetainedMaintenance(run, k.deployment.PollInterval, k.deployment.ActionTimeout)
	}()
	go func() { results <- k.runIdle(run) }()
	workers := 3
	if k.authorization != nil {
		workers++
		go func() { results <- k.authorization.Serve(k.listener) }()
	}
	stop := context.AfterFunc(run, func() {
		if k.authorization != nil {
			_ = k.authorization.Close()
		}
	})
	defer stop()
	err := <-results
	cancel()
	for range workers - 1 {
		err = errors.Join(err, <-results)
	}
	return err
}
func (k *khoraApplication) runIdle(ctx context.Context) error {
	after := ""
	for ctx.Err() == nil {
		action, cancel := context.WithTimeout(ctx, k.deployment.ActionTimeout)
		candidates, err := k.store.RetainedIdleCandidates(action, k.owner, after, time.Now().Add(-k.deployment.IdleThreshold), 16)
		cancel()
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			after = ""
		}
		for _, b := range candidates {
			after = b.GenerationID
			action, cancel := context.WithTimeout(ctx, k.deployment.ActionTimeout)
			err := k.runtime.Controller.ReleaseIdle(action, b.SessionID, b.GenerationID, k.owner)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				k.log.Warn("retained idle pause deferred", "session_id", b.SessionID, "error", err)
			}
		}
		timer := time.NewTimer(k.deployment.IdleInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
