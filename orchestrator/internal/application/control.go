package application

import (
	"context"
	"encoding/json"
	"errors"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/sandbox"
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
		}
		if !decodeControl(w, r, &body) {
			return
		}
		if body.TimeoutSeconds < 1 || body.TimeoutSeconds > 3600 {
			http.Error(w, "timeout_seconds must be between 1 and 3600", 400)
			return
		}
		state, err := a.Store.BeginReleaseDrain(r.Context(), body.Owner, time.Duration(body.TimeoutSeconds)*time.Second)
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
		state, err := a.Store.TryReleaseReady(r.Context(), body.Owner, body.Generation)
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
		release, err := a.Store.ReleaseState(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out["release"] = release
		out["writer"] = a.Store.WriterStats()
		out["runtime_work"] = a.Store.RuntimeWorkStats()
		if a.Agents != nil {
			out["agent_pressure"] = a.Agents.AgentPressure()
		}
		if a.Files != nil {
			out["workspace_reads"] = a.Files.ReadStats()
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
			observed, e := a.Backend.InspectConfiguration(r.Context(), resources.Reference)
			if e != nil {
				report["observation_error"] = e.Error()
			} else {
				mounts := append([]sandbox.Mount{}, resources.Profile.PublishedMounts...)
				mounts = append(mounts, sandbox.Mount{VolumeID: resources.WorkspaceVolume, Destination: "/workspace", Access: "exclusive"}, sandbox.Mount{VolumeID: resources.RuntimeVolume, Destination: "/runtime", Access: "exclusive"})
				report["configuration_matches"] = sandbox.SameConfiguration(observed, resources.Profile.Reachability.Declaration(), mounts)
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
		writeControl(w, map[string]any{"published_mounts": profile.PublishedMounts, "default_activation": "on application restart"})
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
