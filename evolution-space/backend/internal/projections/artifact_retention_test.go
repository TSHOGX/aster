package projections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"aster/contracts/evolution"
)

func retentionArtifact(t *testing.T, s *Store, id, body string) evolution.ArtifactRef {
	t.Helper()
	ref := evolution.ArtifactRef{ID: id, GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte(body)), Size: int64(len(body)), MediaType: "text/plain", GrantID: "grant", PolicyRevision: 1}
	if err := s.SealArtifact(context.Background(), ref, bytes.NewBufferString(body)); err != nil {
		t.Fatal(err)
	}
	return ref
}
func TestArtifactExpiryIsAtomicDurableAndCannotResurrect(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, filepath.Join(root, "state", "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ref := retentionArtifact(t, s, "artifact", "retained evidence")
	empty := retentionArtifact(t, s, "empty", "")
	if raw, err := s.Artifact(ctx, empty); err != nil || len(raw) != 0 {
		t.Fatal("empty artifact not readable", err)
	}
	input := ArtifactExpirationInput{RequestID: "expire", Artifact: ref, Reason: "Operator retention selection"}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_expiry BEFORE UPDATE ON artifact_store_quota BEGIN SELECT RAISE(ABORT,'quota fault'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExpireArtifact(ctx, input); err == nil {
		t.Fatal("fault did not abort expiry")
	}
	if raw, err := s.Artifact(ctx, ref); err != nil || string(raw) != "retained evidence" {
		t.Fatal("partial expiration escaped rollback", err)
	}
	if _, err = s.db.ExecContext(ctx, "DROP TRIGGER fail_expiry"); err != nil {
		t.Fatal(err)
	}
	result, err := s.ExpireArtifact(ctx, input)
	if err != nil || result.ReleasedBytes != ref.Size || result.ExpiredAt.IsZero() {
		t.Fatal(result, err)
	}
	var quota int64
	if err = s.db.QueryRowContext(ctx, "SELECT used_bytes FROM artifact_store_quota WHERE id=1").Scan(&quota); err != nil || quota != 0 {
		t.Fatal("expiry quota", quota, err)
	}
	for _, read := range []func(context.Context, evolution.ArtifactRef) ([]byte, error){s.Artifact, s.NonDatasetArtifact} {
		if raw, err := read(ctx, ref); !errors.Is(err, ErrArtifactExpired) || len(raw) != 0 {
			t.Fatal("expired bytes readable", err)
		}
	}
	if err = s.SealArtifact(ctx, ref, bytes.NewBufferString("retained evidence")); !errors.Is(err, ErrArtifactExpired) {
		t.Fatal("resealed expired identity", err)
	}
	changed := input
	changed.Reason = "different"
	if _, err = s.ExpireArtifact(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("receipt changed", err)
	}
	changed = input
	changed.RequestID = "another"
	if _, err = s.ExpireArtifact(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("artifact expired twice", err)
	}
	for _, statement := range []string{"UPDATE artifacts SET body=x'00',expired_at=0 WHERE id='artifact'", "DELETE FROM artifacts WHERE id='artifact'", "DELETE FROM artifact_expirations", "UPDATE artifact_expirations SET expired_at=1"} {
		if _, err = s.db.ExecContext(ctx, statement); err == nil {
			t.Fatal("expired history mutated", statement)
		}
	}
	// Freed capacity admits a new immutable identity, without resurrecting the old.
	_ = retentionArtifact(t, s, "replacement", "new evidence")
	if _, err = s.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "expire-empty", Artifact: empty, Reason: "Empty evidence retention"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Artifact(ctx, empty); !errors.Is(err, ErrArtifactExpired) {
		t.Fatal("zero bytes confused with expiry", err)
	}
	backup := filepath.Join(root, "backup")
	if err = s.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err = Restore(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(ctx, filepath.Join(restored, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	replay, err := copy.ExpireArtifact(ctx, input)
	if err != nil || replay != result {
		t.Fatal("restore lost immutable receipt", replay, err)
	}
	if _, err = copy.Artifact(ctx, ref); !errors.Is(err, ErrArtifactExpired) {
		t.Fatal("restore resurrected bytes", err)
	}
}

func TestArtifactExpiryPinsRunningAndRetainedWorkflowDependencies(t *testing.T) {
	for _, pin := range []string{"running", "held-budget", "pending-handoff", "unsettled-generation", "missing-handoff", "active-experiment", "active-dependent", "shared-input", "grant-only"} {
		t.Run(pin, func(t *testing.T) {
			ctx := context.Background()
			s, _, lease, call := journalFixture(t)
			ref := retentionArtifact(t, s, "source", "retained evidence")
			if pin != "grant-only" {
				if _, err := s.AppendRunEvent(ctx, lease, evolution.Event{ID: "source", RunID: lease.RunID, ProducerID: lease.WorkerID, Fence: lease.Fence, Timestamp: time.Now(), Kind: "artifact", Artifacts: []evolution.ArtifactRef{ref}}); err != nil {
					t.Fatal(err)
				}
			}
			if pin != "running" {
				if _, err := s.db.ExecContext(ctx, "UPDATE runs SET state='completed' WHERE id='run'"); err != nil {
					t.Fatal(err)
				}
			}
			switch pin {
			case "held-budget":
				_, err := s.db.ExecContext(ctx, "INSERT INTO run_budget_reservations(run_id,id,tokens,cost_micros,state) VALUES('run','held',1,1,'held')")
				if err != nil {
					t.Fatal(err)
				}
			case "pending-handoff":
				_, err := s.db.ExecContext(ctx, "INSERT INTO run_handoffs(run_id,request,state) VALUES('run','{}','pending')")
				if err != nil {
					t.Fatal(err)
				}
			case "unsettled-generation", "missing-handoff", "grant-only":
				raw, _ := json.Marshal(evolution.GenerationGrant{ID: ref.GrantID, GroupID: ref.GroupID, RunID: "run"})
				if _, err := s.db.ExecContext(ctx, "INSERT INTO generation_grants(run_id,assignment,grant) VALUES('run','{}',?)", raw); err != nil {
					t.Fatal(err)
				}
				if pin == "missing-handoff" {
					if _, err := s.db.ExecContext(ctx, "INSERT INTO generation_settlements VALUES('run')"); err != nil {
						t.Fatal(err)
					}
				}
			case "active-experiment", "active-dependent", "shared-input":
				other := evolution.Run{ID: "consumer", GroupID: "group", Pins: evolution.RunPins{Workflow: call.Workflow}}
				experiment := "run"
				if pin == "active-dependent" {
					experiment = ""
					other.Pins.Dependencies = []evolution.GenerationDependency{{RunID: "run"}}
				}
				if pin == "shared-input" {
					experiment = ""
					other.Pins.Inputs = []evolution.ArtifactRef{ref}
				}
				raw, _ := json.Marshal(other)
				if _, err := s.db.ExecContext(ctx, "INSERT INTO runs(id,group_id,binding_id,configuration_revision,occurrence_id,experiment_of,request,body,state) VALUES('consumer','group','consumer',1,'consumer',?,'{}',?,'queued')", experiment, raw); err != nil {
					t.Fatal(err)
				}
			}
			inspection, err := s.ArtifactRetention(ctx, ref)
			if err != nil || len(inspection.Pins) != 1 || inspection.Pins[0] != "run_dependency" {
				t.Fatal("pin missing", inspection, err)
			}
			if _, err = s.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "expiry", Artifact: ref, Reason: "retention"}); !errors.Is(err, ErrArtifactPinned) {
				t.Fatal("pinned artifact expired", err)
			}
			if raw, err := s.Artifact(ctx, ref); err != nil || string(raw) != "retained evidence" {
				t.Fatal("pin lost bytes", err)
			}
		})
	}
}

func TestArtifactExpiryProtectsDatasetsAndUnsettledEvaluatorEvidence(t *testing.T) {
	s, _, input := evaluationAdmissionFixture(t)
	ctx := context.Background()
	job, err := s.AdmitEvaluation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "dataset-expiry", Artifact: job.Subject.Dataset, Reason: "retention"}); !errors.Is(err, ErrArtifactPinned) {
		t.Fatal("profile dataset expired", err)
	}
	// Authenticated evaluator result delivery may lag the provider's sealed bytes.
	grant := evolution.EvaluationGrant{ID: "grant", GroupID: "group", JobID: "pending"}
	body, _ := json.Marshal(EvaluationJob{Grant: &grant})
	if _, err = s.db.ExecContext(ctx, "INSERT INTO evaluation_jobs(id,group_id,request,body,state) VALUES('pending','group','{}',?,'running')", body); err != nil {
		t.Fatal(err)
	}
	evidence := retentionArtifact(t, s, "provider-evidence", "private result")
	if _, err = s.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "provider-expiry", Artifact: evidence, Reason: "retention"}); !errors.Is(err, ErrArtifactPinned) {
		t.Fatal("unadvertised evaluator evidence expired", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE evaluation_jobs SET state='completed' WHERE id='pending'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "provider-expiry", Artifact: evidence, Reason: "retention"}); !errors.Is(err, ErrArtifactPinned) {
		t.Fatal("unsettled terminal evaluator evidence expired", err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO evaluation_settlements(job_id) VALUES('pending')"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "provider-expiry", Artifact: evidence, Reason: "retention"}); err != nil {
		t.Fatal("settled evaluator evidence remained pinned", err)
	}
}

func TestArtifactExpiryClosesInlineReplayAndSerializesReaders(t *testing.T) {
	ctx := context.Background()
	s, _, lease, call := journalFixture(t)
	j := WorkflowJournal{Store: s, Lease: lease, GrantID: "grant"}
	record, err := j.Begin(ctx, call, 10)
	if err != nil {
		t.Fatal(err)
	}
	record.State = "succeeded"
	if err = j.Complete(ctx, record); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.LatestRunCheckpoint(ctx, "group", "run")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SettleRun(ctx, lease, "completed"); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for n := 0; n < 10; n++ {
				raw, err := s.Artifact(ctx, checkpoint.Artifact)
				if err == nil {
					if evolution.Digest(raw) != checkpoint.Artifact.Digest {
						t.Error("partial concurrent body")
					}
				} else if !errors.Is(err, ErrArtifactExpired) {
					t.Error(err)
				}
			}
		}()
	}
	if _, err = s.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "expire-checkpoint", Artifact: checkpoint.Artifact, Reason: "terminal retention"}); err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	if records, err := s.WorkflowAttempts(ctx, "group", "run", "", 10); !errors.Is(err, ErrArtifactExpired) || len(records) != 0 {
		t.Fatal("expired inline copies returned", err)
	}
	if _, _, err = s.ReadWorkflowCheckpoint(ctx, "group", "run", checkpoint.ID); !errors.Is(err, ErrArtifactExpired) {
		t.Fatal("expired replay allowed", err)
	}
	if _, err = s.PreviewRunExperiment(ctx, RunExperimentRequest{GroupID: "group", SourceRunID: "run", CheckpointID: checkpoint.ID}); !errors.Is(err, ErrArtifactExpired) {
		t.Fatal("expired preview lost typed status", err)
	}
	if page, err := s.RunArtifacts(ctx, "group", "run", "", 10); err != nil || len(page.Items) != 1 {
		t.Fatal("expiry removed provenance", page, err)
	}
}

func TestArtifactExpiryAndProviderPreparationHaveOneAtomicOrder(t *testing.T) {
	for attempt := 0; attempt < 10; attempt++ {
		s, _, lease, _ := journalFixture(t)
		ctx := context.Background()
		ref := retentionArtifact(t, s, "request", "provider request")
		if err := s.ReserveRunBudget(ctx, lease, "call", RunUsage{1, 1}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		prepared := make(chan error, 1)
		expired := make(chan error, 1)
		go func() { <-start; _, err := s.PrepareProviderCall(ctx, lease, "call", ref); prepared <- err }()
		go func() {
			<-start
			_, err := s.ExpireArtifact(ctx, ArtifactExpirationInput{RequestID: "expire", Artifact: ref, Reason: "retention"})
			expired <- err
		}()
		close(start)
		prepareErr, expireErr := <-prepared, <-expired
		if prepareErr == nil {
			if !errors.Is(expireErr, ErrArtifactPinned) {
				t.Fatal("prepared provider input lost its pin", expireErr)
			}
			if _, err := s.Artifact(ctx, ref); err != nil {
				t.Fatal("prepared input unreadable", err)
			}
		} else if !errors.Is(prepareErr, ErrArtifactExpired) || expireErr != nil {
			t.Fatal("expiry and preparation crossed snapshots", prepareErr, expireErr)
		}
	}
}
