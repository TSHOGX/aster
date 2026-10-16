package projections

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

var ErrArtifactExpired = errors.New("artifact_expired")
var ErrArtifactPinned = errors.New("artifact_pinned")

type ArtifactExpirationInput struct {
	RequestID string                `json:"request_id"`
	Artifact  evolution.ArtifactRef `json:"artifact"`
	Reason    string                `json:"reason"`
}
type ArtifactExpiration struct {
	RequestID     string                `json:"request_id"`
	Artifact      evolution.ArtifactRef `json:"artifact"`
	Reason        string                `json:"reason"`
	ExpiredAt     time.Time             `json:"expired_at"`
	ReleasedBytes int64                 `json:"released_bytes"`
}
type ArtifactRetention struct {
	Artifact  evolution.ArtifactRef `json:"artifact"`
	ExpiredAt *time.Time            `json:"expired_at,omitempty"`
	Pins      []string              `json:"pins"`
}

// Durable references, including non-advertised inputs and prepared handoff bytes.
// Metadata survives expiry; these relationships cannot be deleted to evade pins.
const retentionRunRefs = `run_refs(run_id,metadata) AS (
 SELECT r.id,j.value FROM runs r,json_each(r.body,'$.pins.inputs') j
 UNION ALL SELECT r.id,json_extract(r.body,'$.experiment.checkpoint') FROM runs r
 UNION ALL SELECT e.run_id,j.value FROM run_events e,json_each(e.request,'$.artifacts') j
 UNION ALL SELECT run_id,json_extract(body,'$.artifact') FROM run_checkpoints
 UNION ALL SELECT run_id,json_extract(body,'$.request') FROM provider_calls
 UNION ALL SELECT run_id,json_extract(body,'$.output') FROM provider_outcomes
 UNION ALL SELECT o.run_id,j.value FROM provider_outcomes o,json_each(o.body,'$.evidence') j
 UNION ALL SELECT h.run_id,json_extract(j.value,'$.artifact') FROM run_handoffs h,json_each(h.request,'$.entries') j
)`

// Pins propagate through experiment/dependency ancestry, including when an
// ancestor is terminal but a descendant still needs its frozen replay state.
const artifactPinQuery = `WITH RECURSIVE ` + retentionRunRefs + `,
 parents(child,parent) AS (
 SELECT id,experiment_of FROM runs WHERE experiment_of<>''
 UNION SELECT r.id,json_extract(j.value,'$.run_id') FROM runs r,json_each(r.body,'$.pins.dependencies') j
 ), pinned_runs(id) AS (
 SELECT r.id FROM runs r WHERE r.state NOT IN('completed','failed','cancelled')
 OR EXISTS(SELECT 1 FROM run_budget_reservations b WHERE b.run_id=r.id AND b.state='held')
 OR ((EXISTS(SELECT 1 FROM generation_grants g WHERE g.run_id=r.id) OR EXISTS(SELECT 1 FROM generation_admissions a WHERE a.run_id=r.id))
 AND (NOT EXISTS(SELECT 1 FROM generation_settlements s WHERE s.run_id=r.id)
 OR (r.state='completed' AND json_extract(r.body,'$.draft_test') IS NULL AND NOT EXISTS(SELECT 1 FROM run_handoffs h WHERE h.run_id=r.id AND h.state IN('submitted','no_change')))))
 OR EXISTS(SELECT 1 FROM run_handoffs h WHERE h.run_id=r.id AND h.state='pending')
 UNION SELECT p.parent FROM parents p JOIN pinned_runs r ON r.id=p.child WHERE p.parent IS NOT NULL
 )
 SELECT 'profile_dataset' WHERE EXISTS(SELECT 1 FROM catalog_definitions WHERE kind='profile' AND json_extract(body,'$.dataset.group_id')=:group AND json_extract(body,'$.dataset.id')=:artifact)
 UNION ALL SELECT 'run_dependency' WHERE EXISTS(SELECT 1 FROM run_refs a JOIN pinned_runs p ON p.id=a.run_id WHERE json_extract(a.metadata,'$.group_id')=:group AND json_extract(a.metadata,'$.id')=:artifact) OR EXISTS(SELECT 1 FROM generation_grants g JOIN pinned_runs p ON p.id=g.run_id WHERE json_extract(g.grant,'$.group_id')=:group AND json_extract(g.grant,'$.id')=:grant)
 UNION ALL SELECT 'evaluation_dependency' WHERE EXISTS(SELECT 1 FROM evaluation_jobs j WHERE
 (j.state IN('queued','running','cancelling') OR (json_extract(j.body,'$.grant') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM evaluation_settlements s WHERE s.job_id=j.id))) AND
 ((j.group_id=:group AND json_extract(j.body,'$.grant.id')=:grant) OR (json_extract(j.body,'$.subject.dataset.group_id')=:group AND json_extract(j.body,'$.subject.dataset.id')=:artifact)
 OR EXISTS(SELECT 1 FROM json_each(j.result,'$.result.reports') a WHERE json_extract(a.value,'$.group_id')=:group AND json_extract(a.value,'$.id')=:artifact)))`

func artifactRetention(ctx context.Context, tx *sql.Tx, ref evolution.ArtifactRef) (ArtifactRetention, error) {
	result := ArtifactRetention{Artifact: ref, Pins: []string{}}
	if ref.Validate() != nil {
		return result, fmt.Errorf("invalid artifact reference")
	}
	var metadata []byte
	var expired int64
	err := tx.QueryRowContext(ctx, "SELECT metadata,expired_at FROM artifacts WHERE group_id=? AND id=?", ref.GroupID, ref.ID).Scan(&metadata, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	stored, err := evolution.Decode[evolution.ArtifactRef](bytes.NewReader(metadata))
	if err != nil || stored != ref {
		return result, ErrConflict
	}
	if expired > 0 {
		at := time.Unix(0, expired).UTC()
		result.ExpiredAt = &at
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, artifactPinQuery, sql.Named("group", ref.GroupID), sql.Named("artifact", ref.ID), sql.Named("grant", ref.GrantID))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var pin string
		if err = rows.Scan(&pin); err != nil {
			return result, err
		}
		result.Pins = append(result.Pins, pin)
	}
	return result, rows.Err()
}

// ArtifactRetention is an operator inspection API, not a browser source grant.
func (s *Store) ArtifactRetention(ctx context.Context, ref evolution.ArtifactRef) (ArtifactRetention, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ArtifactRetention{}, err
	}
	defer tx.Rollback()
	return artifactRetention(ctx, tx, ref)
}

// ExpireArtifact removes a sealed body only when no current work pins it. Callers
// are private operators; this is never inferred from age or a browser request.
func (s *Store) ExpireArtifact(ctx context.Context, input ArtifactExpirationInput) (ArtifactExpiration, error) {
	var result ArtifactExpiration
	if !manifest.ValidID(input.RequestID) || input.Artifact.Validate() != nil || strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 2000 {
		return result, fmt.Errorf("exact artifact, request identity and reason required")
	}
	request, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var prior, receipt []byte
	err = tx.QueryRowContext(ctx, "SELECT request,result FROM artifact_expirations WHERE request_id=?", input.RequestID).Scan(&prior, &receipt)
	if err == nil {
		if !bytes.Equal(request, prior) {
			return result, ErrConflict
		}
		return evolution.Decode[ArtifactExpiration](bytes.NewReader(receipt))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	retention, err := artifactRetention(ctx, tx, input.Artifact)
	if err != nil {
		return result, err
	}
	if retention.ExpiredAt != nil {
		return result, ErrConflict
	}
	if len(retention.Pins) > 0 {
		return result, ErrArtifactPinned
	}
	if err = requireSealedArtifact(ctx, tx, input.Artifact); err != nil {
		return result, err
	}
	result = ArtifactExpiration{RequestID: input.RequestID, Artifact: input.Artifact, Reason: input.Reason, ExpiredAt: time.Now().UTC(), ReleasedBytes: input.Artifact.Size}
	receipt, err = json.Marshal(result)
	if err != nil {
		return ArtifactExpiration{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO artifact_expirations(request_id,group_id,artifact_id,request,result,expired_at) VALUES(?,?,?,?,?,?)", input.RequestID, input.Artifact.GroupID, input.Artifact.ID, request, receipt, result.ExpiredAt.UnixNano()); err != nil {
		return ArtifactExpiration{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE artifacts SET body=NULL,expired_at=? WHERE group_id=? AND id=?", result.ExpiredAt.UnixNano(), input.Artifact.GroupID, input.Artifact.ID); err != nil {
		return ArtifactExpiration{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE artifact_store_quota SET used_bytes=used_bytes-? WHERE id=1", result.ReleasedBytes); err != nil {
		return ArtifactExpiration{}, err
	}
	if err = tx.Commit(); err != nil {
		return ArtifactExpiration{}, err
	}
	return result, nil
}

func requireAvailableRunEvidence(ctx context.Context, q artifactQuery, group, run string) error {
	// Held-out exclusion takes precedence over revealing an expiry marker.
	if err := requireNonDatasetRun(ctx, q, group, run); err != nil {
		return err
	}
	var expired bool
	err := q.QueryRowContext(ctx, `WITH `+retentionRunRefs+` SELECT EXISTS(SELECT 1 FROM run_refs r JOIN runs parent ON parent.id=r.run_id JOIN artifacts a ON a.group_id=json_extract(r.metadata,'$.group_id') AND a.id=json_extract(r.metadata,'$.id') WHERE parent.group_id=? AND r.run_id=? AND a.expired_at>0)`, group, run).Scan(&expired)
	if err != nil {
		return err
	}
	if expired {
		return ErrArtifactExpired
	}
	return nil
}
