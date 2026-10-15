package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/distribution"
	"volume-studio/backend/internal/evaluationclient"
	"volume-studio/backend/internal/httpapi"
	"volume-studio/backend/internal/identity"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8082", "private Studio gateway listener")
	directory := flag.String("state-directory", "", "absolute private persistent Studio directory")
	issuer := flag.String("public-origin", "", "public HTTPS origin and central issuer")
	endpoint := flag.String("identity-endpoint", "", "private central identity origin")
	clientID := flag.String("identity-client", "studio", "registered central client ID")
	secretEnv := flag.String("identity-secret-env", "STUDIO_IDENTITY_CLIENT_SECRET", "environment containing the registered client credential")
	frontend := flag.String("frontend", "http://127.0.0.1:3002", "independent Studio frontend origin")
	publisher := flag.String("publisher-id", "", "stable publisher instance ID retained through restore")
	quota := flag.Int64("quota-bytes", 8<<30, "Studio blob and upload reservation budget")
	distributionConfig := flag.String("distribution-config", "", "private JSON service client registrations")
	distributionListen := flag.String("distribution-listen", "127.0.0.1:8084", "private distribution listener")
	participantListen := flag.String("participant-listen", "", "optional private loopback evolution participant listener")
	participantSecretEnv := flag.String("participant-secret-env", "STUDIO_CENTRAL_SECRET", "environment containing the central participant credential")
	evaluationEndpoint := flag.String("evaluation-endpoint", "", "Optional private Evaluation Space origin")
	evaluationSecretEnv := flag.String("evaluation-secret-env", "EVALUATION_SPACE_STUDIO_SECRET", "Environment containing Studio evaluation credential")
	evaluationInputSecretEnv := flag.String("evaluation-input-secret-env", "STUDIO_EVALUATION_INPUT_SECRET", "Environment containing inbound Evaluation Space input credential")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("Studio gateway requires a private loopback listener behind the HTTPS front door")
	}
	if !filepath.IsAbs(*directory) || *directory == "/" {
		log.Fatal("absolute private state-directory required")
	}
	client, err := identity.NewClient(identity.Config{Issuer: *issuer, Endpoint: *endpoint, ClientID: *clientID, ClientSecret: os.Getenv(*secretEnv)})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sessions, err := identity.OpenSessions(ctx, filepath.Join(*directory, "sessions.db"), client)
	if err != nil {
		log.Fatal(err)
	}
	defer sessions.Close()
	var evaluator authoring.Evaluator
	var resultArtifacts authoring.RunResultArtifacts
	if *evaluationEndpoint != "" {
		transport, transportErr := evaluationclient.New(*evaluationEndpoint, os.Getenv(*evaluationSecretEnv), client)
		evaluator = transport
		resultArtifacts = transport
		err = transportErr
		if err != nil {
			log.Fatal(err)
		}
	}
	store, err := authoring.Open(ctx, authoring.Config{RunResults: client, RunArtifacts: resultArtifacts, Evaluator: evaluator, Authority: client, SeedSharedAssets: true, Directory: *directory, PublisherID: *publisher, QuotaBytes: *quota})
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	api := httpapi.NewAuthoringAPI(store)
	handler, err := (httpapi.Gateway{Sessions: sessions, PublicOrigin: *issuer, Frontend: *frontend, API: api.Serve}).Handler()
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 64 << 10}
	servers := []*http.Server{server}
	if *participantListen != "" {
		host, _, err := net.SplitHostPort(*participantListen)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			log.Fatal("participant requires a private loopback listener")
		}
		authority, err := httpapi.NewCentralEvolutionAuthority(client, os.Getenv(*participantSecretEnv))
		if err != nil {
			log.Fatal(err)
		}
		participant := httpapi.EvolutionParticipantAPI{Store: store, Authority: authority}
		var participantHandler http.Handler = participant.Handler()
		if *evaluationEndpoint != "" {
			inputAPI, err := httpapi.NewEvaluationInputAPI(store, client, os.Getenv(*evaluationInputSecretEnv))
			if err != nil {
				log.Fatal(err)
			}
			resultAPI, err := httpapi.NewRunResultAPI(store, os.Getenv(*evaluationInputSecretEnv))
			if err != nil {
				log.Fatal(err)
			}
			resultAPI.BaseAuthority = client
			resultHandler := resultAPI.Handler()
			inputHandler := inputAPI.Handler()
			original := participantHandler
			participantHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/volumes-studio/internal/v1/run-results/") {
					resultHandler.ServeHTTP(w, r)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/volumes-studio/internal/v1/evaluation-input") {
					inputHandler.ServeHTTP(w, r)
					return
				}
				original.ServeHTTP(w, r)
			})
		}
		servers = append(servers, &http.Server{Addr: *participantListen, Handler: participantHandler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10})
	}
	if *distributionConfig != "" {
		host, _, err := net.SplitHostPort(*distributionListen)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			log.Fatal("distribution requires a private loopback listener")
		}
		file, err := os.Open(*distributionConfig)
		if err != nil {
			log.Fatal("cannot open distribution registrations")
		}
		decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
		decoder.DisallowUnknownFields()
		var cfg distribution.Config
		err = decoder.Decode(&cfg)
		if err == nil && decoder.Decode(new(any)) != io.EOF {
			err = errors.New("trailing distribution configuration")
		}
		_ = file.Close()
		if err != nil {
			log.Fatal("invalid distribution registrations")
		}
		handler, err := distribution.New(store, cfg)
		if err != nil {
			log.Fatal(err)
		}
		servers = append(servers, &http.Server{Addr: *distributionListen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10})
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = store.ExpireUploads(ctx)
				_ = store.ExpireExports(ctx)
				_, err := store.ValidateNext(ctx)
				if err != nil && !errors.Is(err, context.Canceled) {
					log.Print("publication awaiting integrity or principal check; inspect operation state")
				}
				_, _ = store.Collect(ctx)
			}
		}
	}()
	failures := make(chan error, len(servers))
	for _, server := range servers {
		go func() { failures <- server.ListenAndServe() }()
	}
	select {
	case <-ctx.Done():
	case err := <-failures:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print("Studio listener stopped: ", err)
		}
		stop()
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}
	workers.Wait()
}
