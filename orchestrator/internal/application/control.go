package application

import (
	"context"
	"encoding/json"
	"errors"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
	"harness-platform/orchestrator/internal/servicegateway"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func listenControl(name string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(name); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("control path is not a socket")
		}
		if connection, err := net.DialTimeout("unix", name, 200*time.Millisecond); err == nil {
			connection.Close()
			return nil, errors.New("control socket already has a live owner")
		}
		if err = os.Remove(name); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", name)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(name, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
func (a *Application) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /account/recover-admin", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decodeControl(w, r, &body) {
			return
		}
		if err := a.Store.RecoverAdmin(r.Context(), body.Username, body.Password); err != nil {
			http.Error(w, "account recovery refused", 409)
			return
		}
		writeControl(w, map[string]string{"status": "recovered"})
	})
	mux.HandleFunc("GET /network", func(w http.ResponseWriter, r *http.Request) {
		report, err := a.networkReport(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeControl(w, report)
	})
	mux.HandleFunc("POST /network/reload", func(w http.ResponseWriter, r *http.Request) {
		report, err := a.ReloadNetworkPolicy(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeControl(w, report)
	})
	audiences := make(map[string]int, len(a.Config.Gateways))
	for _, gateway := range a.Config.Gateways {
		audiences[gateway.Audience] = gateway.MaxRequests
	}
	mux.Handle(servicegateway.AuthorityPath, servicegateway.AuthorityHandler(a.Store, audiences))
	platformAudiences := map[string]bool{}
	for _, gateway := range a.Config.Gateways {
		platformAudiences[gateway.Audience] = gateway.ScheduleTools
	}
	mux.Handle(servicegateway.PlatformOwnerPath, servicegateway.PlatformHandler(a.Store, platformAudiences))
	mux.HandleFunc("GET /release", func(w http.ResponseWriter, r *http.Request) {
		status, err := a.Store.ReleaseDrainStatus(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeControl(w, status)
	})
	mux.HandleFunc("POST /release/drain", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Owner          string `json:"owner"`
			TimeoutSeconds int64  `json:"timeout_seconds"`
			Mode           string `json:"mode"`
			Target         string `json:"target"`
			Compatibility  string `json:"compatibility"`
		}
		if !decodeControl(w, r, &body) {
			return
		}
		if body.TimeoutSeconds < 1 || body.TimeoutSeconds > 3600 {
			http.Error(w, "timeout_seconds must be between 1 and 3600", 400)
			return
		}
		var state product.ReleaseState
		var err error
		switch body.Mode {
		case "", "quiescent":
			if body.Target != "" || body.Compatibility != "" {
				http.Error(w, "candidate binding requires owner mode", 400)
				return
			}
			state, err = a.Store.BeginReleaseDrain(r.Context(), body.Owner, time.Duration(body.TimeoutSeconds)*time.Second)
		case "owner":
			var proof product.GatewayObservation
			proof, err = a.observeGateways(r.Context())
			if err == nil {
				state, err = a.Store.BeginOwnerHandoff(r.Context(), body.Owner, time.Duration(body.TimeoutSeconds)*time.Second, body.Target, body.Compatibility, proof)
			}
		default:
			http.Error(w, "invalid release mode", 400)
			return
		}
		if err != nil {
			code := 409
			if errors.Is(err, product.ErrInvalid) {
				code = 400
			}
			http.Error(w, err.Error(), code)
			return
		}
		writeControl(w, state)
	})
	mux.HandleFunc("POST /release/abort", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Owner      string `json:"owner"`
			Generation int64  `json:"generation"`
		}
		if !decodeControl(w, r, &body) {
			return
		}
		state, err := a.Store.AbortReleaseDrain(r.Context(), body.Owner, body.Generation)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeControl(w, state)
	})
	mux.HandleFunc("POST /release/ready", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Owner      string `json:"owner"`
			Generation int64  `json:"generation"`
		}
		if !decodeControl(w, r, &body) {
			return
		}
		proofs, observeErr := a.releaseObservation(r.Context())
		if observeErr != nil {
			http.Error(w, observeErr.Error(), 409)
			return
		}
		state, err := a.Store.TryReleaseReady(r.Context(), body.Owner, body.Generation, proofs...)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeControl(w, state)
	})
	mux.HandleFunc("POST /release/activate", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Owner      string `json:"owner"`
			Generation int64  `json:"generation"`
			Target     string `json:"target"`
		}
		if !decodeControl(w, r, &body) {
			return
		}
		if a.controlServer == nil || a.cancel == nil {
			http.Error(w, "application retirement is unavailable", 503)
			return
		}
		proofs, observeErr := a.releaseObservation(r.Context())
		if observeErr != nil {
			http.Error(w, observeErr.Error(), 409)
			return
		}
		state, err := a.Store.ActivateRelease(r.Context(), body.Owner, body.Generation, body.Target, proofs...)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeControl(w, state)
		a.retireAfterActivation()
	})
	mux.HandleFunc("POST /release/complete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Owner      string `json:"owner"`
			Generation int64  `json:"generation"`
		}
		if !decodeControl(w, r, &body) {
			return
		}
		proofs, observeErr := a.releaseObservation(r.Context())
		if observeErr != nil {
			http.Error(w, observeErr.Error(), 409)
			return
		}
		state, err := a.Store.FinishRelease(r.Context(), body.Owner, body.Generation, a.releaseIdentity.ExecutableDigest, proofs...)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeControl(w, state)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		report, err := a.Store.OperatorDiagnostics(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out := map[string]any{"browser": a.BrowserAddress, "runtime_digest": a.Config.Template.Digest, "state": report}
		out["application_digest"] = a.releaseIdentity.ExecutableDigest
		identity := a.releaseIdentity
		if network, err := a.networkReport(r.Context()); err == nil {
			out["network"] = network
			identity.NetworkPolicyRevision = network.Defaults.Revision
		}
		out["release_identity"] = identity
		release, err := a.Store.ReleaseState(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out["release"] = release
		out["writer"] = a.Store.WriterStats()
		schedules, e := a.Store.ScheduleDiagnostics(r.Context())
		if e != nil {
			http.Error(w, "schedule diagnostics unavailable", 500)
			return
		}
		out["schedules"] = schedules
		out["runtime_work"] = a.Store.RuntimeWorkStats()
		if a.Agents != nil {
			out["agent_pressure"] = a.Agents.AgentPressure()
		}
		if a.Files != nil {
			out["workspace_reads"] = a.Files.ReadStats()
			out["preview_sources"] = a.Files.PreviewSnapshots().Stats()
		}
		writeControl(w, out)
	})
	mux.HandleFunc("GET /projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		project, err := a.Store.OperatorProject(r.Context(), r.PathValue("project"))
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		scope := product.Scope{Actor: project.OwnerID, ProjectID: project.ID}
		resources, resourceErr := a.Store.Resources(r.Context(), scope)
		blockers, err := a.Store.Blockers(r.Context(), scope)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		report := map[string]any{"project": project, "blockers": blockers}
		if resourceErr == nil {
			report["resources"] = resources
			if network, err := a.Store.NetworkDefaults(r.Context()); err == nil {
				report["default_network_policy_revision"] = network.Revision
				report["effective_network_policy_revision"] = resources.Profile.NetworkPolicyRevision
				report["network_policy_pending"] = network.Revision != resources.Profile.NetworkPolicyRevision
			}
			observed, e := a.Backend.InspectConfiguration(r.Context(), resources.Reference)
			if e != nil {
				report["observation_error"] = e.Error()
			} else {
				mounts, mountErr := resources.Mounts()
				report["configuration_matches"] = mountErr == nil && sandbox.SameConfiguration(observed, resources.Profile.Reachability.Declaration(), mounts)
				report["observation"] = observed
			}
		}
		writeControl(w, report)
	})
	mux.HandleFunc("POST /notify", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Audience, Title, Body string }
		if !decodeControl(w, r, &body) {
			return
		}
		if err := a.Store.Notify(r.Context(), body.Audience, body.Title, body.Body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /publish", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		profile, err := a.publish(ctx)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		published := []product.VolumePlan{}
		for _, slot := range profile.Volumes {
			if slot.Storage == "shared_readonly" {
				published = append(published, slot)
			}
		}
		writeControl(w, map[string]any{"published_volumes": published, "default_activation": "on application restart"})
	})
	return mux
}
func writeControl(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
func decodeControl(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		http.Error(w, "invalid control request", 400)
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "trailing control request", 400)
		return false
	}
	return true
}
