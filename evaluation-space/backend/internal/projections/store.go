// Package projections retains immutable central configuration projections.
// It does not own group configuration or infer activation from receipt arrival.
package projections

import (
	manifest "aster/contracts/volumemanifest"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"aster/contracts/evolution"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("projection_conflict")
var ErrNotFound = errors.New("projection_not_found")
var ErrCatalog = errors.New("configuration_catalog_invalid")

const MaxArtifactStoreBytes int64 = 256 << 20

type Store struct {
	db        *sql.DB
	lock      *os.File
	path      string
	closeOnce sync.Once
	closeErr  error
}
type Projection struct {
	OperationID   string                  `json:"operation_id"`
	Digest        string                  `json:"digest"`
	Configuration evolution.Configuration `json:"configuration"`
}

const schema = `CREATE TABLE IF NOT EXISTS projections(group_id TEXT NOT NULL,revision INTEGER NOT NULL,digest TEXT NOT NULL,operation_id TEXT NOT NULL UNIQUE,body BLOB NOT NULL,PRIMARY KEY(group_id,revision));
CREATE TRIGGER IF NOT EXISTS immutable_projection BEFORE UPDATE ON projections BEGIN SELECT RAISE(ABORT,'immutable configuration projection'); END;
CREATE TRIGGER IF NOT EXISTS retained_projection BEFORE DELETE ON projections BEGIN SELECT RAISE(ABORT,'retain configuration provenance'); END;
CREATE TABLE workflow_draft_revisions(id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),body BLOB NOT NULL,layout_revision INTEGER NOT NULL,layout BLOB NOT NULL,PRIMARY KEY(id,revision,layout_revision));
CREATE TRIGGER immutable_workflow_draft_history_revision BEFORE UPDATE ON workflow_draft_revisions BEGIN SELECT RAISE(ABORT,'immutable workflow draft revision'); END;
CREATE TRIGGER retained_workflow_draft_revision BEFORE DELETE ON workflow_draft_revisions BEGIN SELECT RAISE(ABORT,'retain workflow draft history'); END;
CREATE TABLE workflow_drafts(id TEXT PRIMARY KEY,group_id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),body BLOB NOT NULL,layout_revision INTEGER NOT NULL DEFAULT 0,layout BLOB NOT NULL DEFAULT '{}');
CREATE TRIGGER immutable_workflow_draft_revision BEFORE UPDATE OF id,group_id ON workflow_drafts BEGIN SELECT RAISE(ABORT,'immutable workflow draft identity'); END;
CREATE TRIGGER retained_workflow_draft_owner BEFORE DELETE ON workflow_drafts BEGIN SELECT RAISE(ABORT,'retain workflow ownership'); END;
CREATE TABLE workflow_draft_creations(id TEXT PRIMARY KEY REFERENCES workflow_drafts(id),request BLOB NOT NULL,result BLOB NOT NULL);
CREATE TRIGGER immutable_workflow_draft_creation BEFORE UPDATE ON workflow_draft_creations BEGIN SELECT RAISE(ABORT,'immutable draft creation'); END;
CREATE TRIGGER retained_workflow_draft_creation BEFORE DELETE ON workflow_draft_creations BEGIN SELECT RAISE(ABORT,'retain draft creation'); END;
CREATE TABLE workflow_draft_publications(id TEXT NOT NULL,draft_revision INTEGER NOT NULL,revision INTEGER NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(id,draft_revision),UNIQUE(id,revision));
CREATE TRIGGER immutable_draft_publication BEFORE UPDATE ON workflow_draft_publications BEGIN SELECT RAISE(ABORT,'immutable draft publication'); END;
CREATE TRIGGER retained_draft_publication BEFORE DELETE ON workflow_draft_publications BEGIN SELECT RAISE(ABORT,'retain draft publication'); END;
CREATE TABLE workflow_revisions(id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),digest TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(id,revision));
CREATE TRIGGER immutable_workflow BEFORE UPDATE ON workflow_revisions BEGIN SELECT RAISE(ABORT,'immutable workflow revision'); END;
CREATE TRIGGER retained_workflow BEFORE DELETE ON workflow_revisions BEGIN SELECT RAISE(ABORT,'retain workflow provenance'); END;
CREATE TABLE catalog_definitions(kind TEXT NOT NULL CHECK(kind IN ('service','template','profile')),id TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),digest TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(kind,id,revision));
CREATE TRIGGER immutable_definition BEFORE UPDATE ON catalog_definitions BEGIN SELECT RAISE(ABORT,'immutable catalog definition'); END;
CREATE TRIGGER retained_definition BEFORE DELETE ON catalog_definitions BEGIN SELECT RAISE(ABORT,'retain catalog provenance'); END;
CREATE TABLE template_owners(id TEXT PRIMARY KEY,group_id TEXT NOT NULL);
CREATE TRIGGER immutable_template_owner BEFORE UPDATE ON template_owners BEGIN SELECT RAISE(ABORT,'immutable template owner'); END;
CREATE TRIGGER retained_template_owner BEFORE DELETE ON template_owners BEGIN SELECT RAISE(ABORT,'retain template owner'); END;
CREATE TABLE service_states(service_id TEXT PRIMARY KEY,revision INTEGER NOT NULL CHECK(revision>0),enabled INTEGER NOT NULL CHECK(enabled IN (0,1)));
CREATE TABLE service_state_receipts(request_id TEXT PRIMARY KEY,request TEXT NOT NULL,revision INTEGER NOT NULL);
CREATE TABLE runs(tokens INTEGER NOT NULL DEFAULT 0 CHECK(tokens>=0),cost_micros INTEGER NOT NULL DEFAULT 0 CHECK(cost_micros>=0),worker_id TEXT NOT NULL DEFAULT '',fence INTEGER NOT NULL DEFAULT 0 CHECK(fence>=0),lease_until INTEGER NOT NULL DEFAULT 0,id TEXT PRIMARY KEY,group_id TEXT NOT NULL,binding_id TEXT NOT NULL,configuration_revision INTEGER NOT NULL,occurrence_id TEXT NOT NULL,experiment_of TEXT NOT NULL DEFAULT '',request BLOB NOT NULL,body BLOB NOT NULL,state TEXT NOT NULL CHECK(state IN ('queued','running','pause_requested','paused','cancelling','completed','failed','cancelled')),UNIQUE(group_id,binding_id,configuration_revision,occurrence_id));
CREATE UNIQUE INDEX experiment_binding_occurrence ON runs(group_id,binding_id,configuration_revision,occurrence_id,experiment_of);
CREATE UNIQUE INDEX one_active_binding_run ON runs(group_id,binding_id) WHERE state IN ('queued','running','pause_requested','paused','cancelling') AND experiment_of='' AND json_extract(body,'$.draft_test') IS NULL;
CREATE TRIGGER immutable_run_admission BEFORE UPDATE OF id,group_id,binding_id,configuration_revision,occurrence_id,experiment_of,request,body ON runs BEGIN SELECT RAISE(ABORT,'immutable run admission'); END;
CREATE TRIGGER retained_run BEFORE DELETE ON runs BEGIN SELECT RAISE(ABORT,'retain run provenance'); END;
CREATE TABLE run_control_receipts(request_id TEXT PRIMARY KEY,run_id TEXT NOT NULL REFERENCES runs(id),request BLOB NOT NULL,result BLOB NOT NULL);
CREATE TRIGGER immutable_run_control BEFORE UPDATE ON run_control_receipts BEGIN SELECT RAISE(ABORT,'immutable run control receipt'); END;
CREATE TRIGGER retained_run_control BEFORE DELETE ON run_control_receipts BEGIN SELECT RAISE(ABORT,'retain run control receipt'); END;
CREATE TABLE run_events(run_id TEXT NOT NULL REFERENCES runs(id),event_id TEXT NOT NULL,sequence INTEGER NOT NULL CHECK(sequence>0),request BLOB NOT NULL,PRIMARY KEY(run_id,sequence),UNIQUE(run_id,event_id));
CREATE TRIGGER immutable_run_event BEFORE UPDATE ON run_events BEGIN SELECT RAISE(ABORT,'immutable run event'); END;
CREATE TRIGGER retained_run_event BEFORE DELETE ON run_events BEGIN SELECT RAISE(ABORT,'retain run event'); END;
CREATE TABLE run_checkpoints(run_id TEXT NOT NULL REFERENCES runs(id),id TEXT NOT NULL,sequence INTEGER NOT NULL CHECK(sequence>0),body BLOB NOT NULL,PRIMARY KEY(run_id,id),UNIQUE(run_id,sequence));
CREATE TRIGGER immutable_run_checkpoint BEFORE UPDATE ON run_checkpoints BEGIN SELECT RAISE(ABORT,'immutable run checkpoint'); END;
CREATE TRIGGER retained_run_checkpoint BEFORE DELETE ON run_checkpoints BEGIN SELECT RAISE(ABORT,'retain run checkpoint'); END;
CREATE TABLE artifact_store_quota(id INTEGER PRIMARY KEY CHECK(id=1),used_bytes INTEGER NOT NULL CHECK(used_bytes>=0));
INSERT INTO artifact_store_quota(id,used_bytes) VALUES(1,0);
CREATE TABLE artifacts(group_id TEXT NOT NULL,id TEXT NOT NULL,metadata BLOB NOT NULL,body BLOB NOT NULL,PRIMARY KEY(group_id,id));
CREATE TRIGGER immutable_artifact BEFORE UPDATE ON artifacts BEGIN SELECT RAISE(ABORT,'immutable sealed artifact'); END;
CREATE TRIGGER retained_artifact BEFORE DELETE ON artifacts BEGIN SELECT RAISE(ABORT,'retain artifact provenance'); END;
CREATE TABLE run_usage_receipts(run_id TEXT NOT NULL REFERENCES runs(id),request_id TEXT NOT NULL,request BLOB NOT NULL,result BLOB NOT NULL,PRIMARY KEY(run_id,request_id));
CREATE TRIGGER immutable_run_usage BEFORE UPDATE ON run_usage_receipts BEGIN SELECT RAISE(ABORT,'immutable usage receipt'); END;
CREATE TRIGGER retained_run_usage BEFORE DELETE ON run_usage_receipts BEGIN SELECT RAISE(ABORT,'retain usage receipt'); END;
CREATE TABLE run_budget_reservations(run_id TEXT NOT NULL REFERENCES runs(id),id TEXT NOT NULL,tokens INTEGER NOT NULL CHECK(tokens>=0),cost_micros INTEGER NOT NULL CHECK(cost_micros>=0),state TEXT NOT NULL CHECK(state IN ('held','settled')),actual_tokens INTEGER NOT NULL DEFAULT 0 CHECK(actual_tokens>=0),actual_cost_micros INTEGER NOT NULL DEFAULT 0 CHECK(actual_cost_micros>=0),PRIMARY KEY(run_id,id));
CREATE TRIGGER immutable_budget_reservation BEFORE UPDATE OF run_id,id,tokens,cost_micros ON run_budget_reservations BEGIN SELECT RAISE(ABORT,'immutable budget reservation'); END;
CREATE TRIGGER terminal_budget_reservation BEFORE UPDATE ON run_budget_reservations WHEN OLD.state='settled' BEGIN SELECT RAISE(ABORT,'settled budget reservation'); END;
CREATE TRIGGER retained_budget_reservation BEFORE DELETE ON run_budget_reservations BEGIN SELECT RAISE(ABORT,'retain budget reservation'); END;
CREATE TABLE provider_calls(run_id TEXT NOT NULL,id TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(run_id,id),FOREIGN KEY(run_id,id) REFERENCES run_budget_reservations(run_id,id));
CREATE TRIGGER immutable_provider_call BEFORE UPDATE ON provider_calls BEGIN SELECT RAISE(ABORT,'immutable provider request'); END;
CREATE TRIGGER retained_provider_call BEFORE DELETE ON provider_calls BEGIN SELECT RAISE(ABORT,'retain provider request'); END;
CREATE TABLE provider_outcomes(run_id TEXT NOT NULL,call_id TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(run_id,call_id),FOREIGN KEY(run_id,call_id) REFERENCES provider_calls(run_id,id));
CREATE TRIGGER immutable_provider_outcome BEFORE UPDATE ON provider_outcomes BEGIN SELECT RAISE(ABORT,'immutable provider outcome'); END;
CREATE TRIGGER retained_provider_outcome BEFORE DELETE ON provider_outcomes BEGIN SELECT RAISE(ABORT,'retain provider outcome'); END;
CREATE TRIGGER run_state_transition BEFORE UPDATE OF state ON runs
WHEN OLD.state<>NEW.state AND NOT (
 (OLD.state='queued' AND NEW.state IN ('running','paused','cancelled','cancelling')) OR
 (OLD.state='running' AND NEW.state IN ('pause_requested','paused','completed','failed','cancelled','cancelling')) OR
 (OLD.state='pause_requested' AND NEW.state IN ('paused','cancelled','cancelling')) OR
 (OLD.state='paused' AND NEW.state IN ('queued','cancelled','cancelling')) OR
 (OLD.state='cancelling' AND NEW.state='cancelled')
)
BEGIN SELECT RAISE(ABORT,'invalid run state transition'); END;
CREATE TRIGGER monotonic_run_fence BEFORE UPDATE OF fence ON runs
WHEN NEW.fence<OLD.fence
BEGIN SELECT RAISE(ABORT,'run fence cannot decrease'); END;`

func Open(ctx context.Context, path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("absolute projection database path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("projection store already owned: %w", err)
	}
	opened := false
	defer func() {
		if !opened {
			lock.Close()
		}
	}()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("projection database must be regular")
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	q := url.Values{"_pragma": {"journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(5000)"}}
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = initializeSchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		db.Close()
		return nil, err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		db.Close()
		return nil, err
	}
	opened = true
	return &Store{db: db, lock: lock, path: path}, nil
}
func (s *Store) Close() error {
	s.closeOnce.Do(func() { s.closeErr = errors.Join(s.db.Close(), s.lock.Close()) })
	return s.closeErr
}

// Adopt is an internal participant command. Its caller must authenticate central
// service authority and validate catalog/resource grants before invoking it.
func (s *Store) Adopt(ctx context.Context, p Projection) error {
	if !manifest.ValidID(p.OperationID) {
		return fmt.Errorf("operation identity required")
	}
	if err := p.Configuration.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p.Configuration)
	if err != nil {
		return err
	}
	if evolution.Digest(raw) != p.Digest {
		return fmt.Errorf("projection digest mismatch")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old []byte
	var operation, digest string
	err = tx.QueryRowContext(ctx, "SELECT operation_id,digest,body FROM projections WHERE group_id=? AND revision=?", p.Configuration.GroupID, p.Configuration.Revision).Scan(&operation, &digest, &old)
	if err == nil {
		if operation != p.OperationID || digest != p.Digest || !bytes.Equal(old, raw) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = validateConfigurationCatalog(ctx, tx, p.Configuration); err != nil {
		return fmt.Errorf("%w: %v", ErrCatalog, err)
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM projections WHERE operation_id=?", p.OperationID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO projections VALUES(?,?,?,?,?)", p.Configuration.GroupID, p.Configuration.Revision, p.Digest, p.OperationID, raw); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Read(ctx context.Context, group string, revision int64) (Projection, error) {
	var p Projection
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT operation_id,digest,body FROM projections WHERE group_id=? AND revision=?", group, revision).Scan(&p.OperationID, &p.Digest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if evolution.Digest(raw) != p.Digest {
		return p, fmt.Errorf("projection integrity mismatch")
	}
	p.Configuration, err = evolution.Decode[evolution.Configuration](bytes.NewReader(raw))
	if err != nil {
		return p, err
	}
	if p.Configuration.GroupID != group || p.Configuration.Revision != revision {
		return p, fmt.Errorf("projection identity mismatch")
	}
	return p, p.Configuration.Validate()
}
