package handoff

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evaluation-space/internal/examples"
	"aster/evaluation-space/internal/projections"
	"aster/evaluation-space/internal/workflow"
)

type examplePlanner struct{}

func (examplePlanner) Plan(run evolution.Run, values workflow.Values, base []manifest.Entry, grant string) (evolution.Patch, []Artifact, error) {
	candidate, err := examples.DecodeCandidate(values["candidate"])
	if err != nil {
		return evolution.Patch{}, nil, err
	}
	value, err := examples.CandidatePatch(run, run.Fence, candidate, base, grant)
	if err != nil {
		return evolution.Patch{}, nil, err
	}
	artifacts := []Artifact{}
	for _, artifact := range value.Artifacts {
		artifacts = append(artifacts, Artifact{artifact.Reference, artifact.Bytes})
	}
	return value.Patch, artifacts, nil
}

type authority struct{ denied bool }

func (a *authority) Check(context.Context, evolution.Run) error {
	if a.denied {
		return errors.New("revoked")
	}
	return nil
}
func (a *authority) Artifact(ctx context.Context, run evolution.Run, _ evolution.ArtifactRef) error {
	return a.Check(ctx, run)
}
func (a *authority) OutputGrant(context.Context, evolution.Run) (string, error) { return "grant", nil }

type studio struct {
	base         []manifest.Entry
	receipt      *Receipt
	submits      int
	lost         bool
	beforeSubmit func()
	lookupErr    error
}

func (s *studio) Base(context.Context, evolution.Run) ([]manifest.Entry, error) { return s.base, nil }
func (s *studio) Lookup(context.Context, evolution.Run, evolution.Patch) (Receipt, error) {
	if s.lookupErr != nil {
		return Receipt{}, s.lookupErr
	}
	if s.receipt == nil {
		return Receipt{}, ErrNotFound
	}
	return *s.receipt, nil
}
func (s *studio) Submit(_ context.Context, run evolution.Run, patch evolution.Patch) (Receipt, error) {
	s.submits++
	if s.beforeSubmit != nil {
		s.beforeSubmit()
	}
	raw, _ := json.Marshal(patch)
	receipt := Receipt{run.ID, evolution.Digest(raw), "result-branch", strings.Repeat("b", 64)}
	s.receipt = &receipt
	if s.lost {
		return Receipt{}, errors.New("reply lost")
	}
	return receipt, nil
}
func fixture(t *testing.T, noChange bool) (Worker, *studio, *authority, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "space.db")
	store, err := projections.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	base := []manifest.Entry{{Path: "root.txt", Kind: "file", Size: "4", Digest: evolution.Digest([]byte("root"))}}
	run := evolution.Run{ID: "run", GroupID: "group", BindingID: "binding", State: "queued", CreatedAt: time.Now().UTC(), Budget: evolution.Budget{MaxAttempts: 10, MaxTokens: 100, MaxArtifactBytes: 1 << 20, TimeoutSeconds: 60}, Pins: evolution.RunPins{SourcePolicyRevision: 1, Base: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: manifest.Profile, TreeDigest: manifest.TreeDigest(base)}, Output: evolution.OutputBinding{LibraryID: "library", Prefix: "lessons", Profile: manifest.Profile, ReservationGeneration: 1}}}
	raw, _ := json.Marshal(run)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,request,body,state) VALUES('run','group','binding',1,'occurrence','{}',?,'queued')`, raw)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimRun(ctx, "run", "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	candidate := examples.Candidate{Files: []examples.File{}, Held: []examples.Proposal{}}
	if !noChange {
		candidate.Files = append(candidate.Files, examples.File{Path: "lesson.md", Text: "# Lesson\nVerified content."})
	}
	candidateRaw, _ := json.Marshal(candidate)
	values, _ := json.Marshal(workflow.Values{"candidate": candidateRaw})
	artifact := evolution.ArtifactRef{ID: "result", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest(values), Size: int64(len(values)), MediaType: "application/json", GrantID: "grant", PolicyRevision: 1}
	if err = store.SealArtifact(ctx, artifact, bytes.NewReader(values)); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveRunCheckpoint(ctx, lease, projections.RunCheckpoint{ID: "workflow-result", RunID: "run", Fence: lease.Fence, Artifact: artifact}); err != nil {
		t.Fatal(err)
	}
	if err = store.SettleRun(ctx, lease, "completed"); err != nil {
		t.Fatal(err)
	}
	remote := &studio{base: base}
	auth := &authority{}
	return Worker{Store: store, Studio: remote, Authority: auth, Planner: examplePlanner{}}, remote, auth, path
}
func TestHandoffRecoversLostAcceptanceWithoutRerunningGeneration(t *testing.T) {
	worker, remote, _, path := fixture(t, false)
	ctx := context.Background()
	remote.lost = true
	if err := worker.Step(ctx, "group", "run"); err == nil {
		t.Fatal("uncertain response hidden")
	}
	retained, err := worker.Store.Handoff(ctx, "group", "run")
	if err != nil || retained.State != "pending" {
		t.Fatal(retained, err)
	}
	worker.Store.Close()
	worker.Store, err = projections.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Store.Close()
	if err = worker.Step(ctx, "group", "run"); err != nil {
		t.Fatal(err)
	}
	run, err := worker.Store.Run(ctx, "group", "run")
	if err != nil || run.State != "completed" || run.Handoff != "submitted" || run.ResultBranch != "result-branch" || remote.submits != 1 {
		t.Fatal(run, err, remote.submits)
	}
	if err = worker.Step(ctx, "group", "run"); err != nil || remote.submits != 1 {
		t.Fatal("terminal handoff repeated", err)
	}
	if _, err = worker.Store.Handoff(ctx, "other", "run"); !errors.Is(err, projections.ErrNotFound) {
		t.Fatal("foreign handoff", err)
	}
	if err = worker.Store.AcceptHandoff(ctx, "group", retained.Patch, "other-branch", strings.Repeat("c", 64)); !errors.Is(err, projections.ErrConflict) {
		t.Fatal("receipt rebound", err)
	}
}
func TestNoChangeAndFailedLookupDoNotSubmit(t *testing.T) {
	for _, mode := range []string{"no-change", "unavailable", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			worker, remote, auth, _ := fixture(t, mode == "no-change")
			if mode == "unavailable" {
				remote.lookupErr = errors.New("offline")
			}
			if mode == "revoked" {
				auth.denied = true
			}
			err := worker.Step(context.Background(), "group", "run")
			if mode == "no-change" {
				if err != nil {
					t.Fatal(err)
				}
				run, err := worker.Store.Run(context.Background(), "group", "run")
				if err != nil || run.Handoff != "no_change" || run.ResultBranch != "" {
					t.Fatal(run, err)
				}
			} else if err == nil {
				t.Fatal("failure hidden")
			}
			if remote.submits != 0 {
				t.Fatal("unexpected remote write")
			}
		})
	}
}
