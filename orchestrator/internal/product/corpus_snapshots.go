package product

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"aster/contracts/evolution"
	"github.com/google/uuid"
)

type CorpusSnapshotInput struct {
	RequestID      string `json:"request_id"`
	SourceID       string `json:"source_id"`
	SourceRevision int64  `json:"source_revision"`
	AfterSequence  int64  `json:"after_sequence"`
	Limit          int    `json:"limit"`
}
type CorpusMember struct {
	EpisodeID string `json:"episode_id"`
	Revision  int64  `json:"revision"`
	Sequence  int64  `json:"sequence"`
	Digest    string `json:"digest"`
}
type CorpusSnapshot struct {
	ID             string                `json:"id"`
	GroupID        string                `json:"group_id"`
	SourceID       string                `json:"source_id"`
	SourceRevision int64                 `json:"source_revision"`
	Through        int64                 `json:"through_sequence"`
	Artifact       evolution.ArtifactRef `json:"artifact"`
	Members        []CorpusMember        `json:"members"`
}

func (s *Store) SealCorpusSnapshot(ctx context.Context, token, group string, input CorpusSnapshotInput) (CorpusSnapshot, error) {
	var result CorpusSnapshot
	if !validID(group) || !validID(input.SourceID) || input.SourceRevision < 1 || input.AfterSequence < 0 || input.Limit < 1 || input.Limit > 100 {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "corpus.seal", group, input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.manage")
		if err != nil {
			return err
		}
		if err = authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
			return err
		}
		source, err := sourcePolicy(ctx, tx, group, input.SourceID, input.SourceRevision)
		if err != nil {
			return err
		}
		prior, replay, err := readAdminReceipt(ctx, tx, actor.ID, request)
		if err != nil {
			return err
		}
		if replay {
			return json.Unmarshal(prior, &result)
		}
		rows, err := tx.QueryContext(ctx, `SELECT e.episode_id,e.revision,e.rowid,e.digest,e.body FROM corpus_episodes e WHERE e.group_id=? AND e.source_id=? AND e.source_revision=? AND e.rowid>? AND NOT EXISTS(SELECT 1 FROM corpus_episodes newer WHERE newer.group_id=e.group_id AND newer.source_id=e.source_id AND newer.episode_id=e.episode_id AND newer.revision>e.revision) ORDER BY e.rowid LIMIT ?`, group, input.SourceID, input.SourceRevision, input.AfterSequence, input.Limit)
		if err != nil {
			return err
		}
		corpus := evolution.Corpus{Episodes: []evolution.CorpusEpisode{}}
		result = CorpusSnapshot{ID: uuid.NewString(), GroupID: group, SourceID: input.SourceID, SourceRevision: input.SourceRevision, Through: input.AfterSequence, Members: []CorpusMember{}}
		size := 0
		for rows.Next() {
			var member CorpusMember
			var raw []byte
			if err = rows.Scan(&member.EpisodeID, &member.Revision, &member.Sequence, &member.Digest, &raw); err != nil {
				rows.Close()
				return err
			}
			if evolution.Digest(raw) != member.Digest {
				rows.Close()
				return ErrInvalid
			}
			var record struct {
				Episode        evolution.CorpusEpisode `json:"episode"`
				SourceRevision int64                   `json:"source_revision"`
				CaptureState   string                  `json:"capture_state"`
			}
			record, err = evolution.Decode[struct {
				Episode        evolution.CorpusEpisode `json:"episode"`
				SourceRevision int64                   `json:"source_revision"`
				CaptureState   string                  `json:"capture_state"`
			}](bytes.NewReader(raw))
			if err != nil {
				rows.Close()
				return err
			}
			if size+len(raw) > corpusSnapshotLimit-4096 {
				break
			}
			size += len(raw)
			corpus.Episodes = append(corpus.Episodes, record.Episode)
			result.Members = append(result.Members, member)
			result.Through = member.Sequence
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(result.Members) == 0 {
			return ErrNotFound
		}
		body, _ := json.Marshal(corpus)
		members, _ := json.Marshal(result.Members)
		digest := evolution.Digest(body)
		result.Artifact = evolution.ArtifactRef{ID: result.ID, GroupID: group, SchemaDigest: evolution.Digest(evolution.CorpusSchema), Digest: digest, Size: int64(len(body)), MediaType: "application/json", GrantID: "corpus-" + result.ID, PolicyRevision: source.PolicyRevision}
		if _, err = tx.ExecContext(ctx, "INSERT INTO corpus_snapshots VALUES(?,?,?,?,?,?,?, ?,?)", result.ID, group, input.SourceID, input.SourceRevision, body, members, digest, result.Through, s.timestamp()); err != nil {
			return err
		}
		artifact, _ := json.Marshal(result.Artifact)
		sources, _ := json.Marshal([]evolution.SourceBinding{source})
		if _, err = tx.ExecContext(ctx, "INSERT INTO generation_input_grants VALUES(?,?,1,1,?,?)", group, result.ID, artifact, sources); err != nil {
			return err
		}
		raw, _ := json.Marshal(result)
		if err = s.writeAdminReceipt(ctx, tx, actor.ID, request, raw); err != nil {
			return err
		}
		return s.auditEvent(ctx, tx, actor.ID, "corpus.seal", "source", input.SourceID, input.RequestID, input.SourceRevision, map[string]any{"snapshot_id": result.ID, "digest": digest, "through_sequence": result.Through, "episodes": len(result.Members)})
	})
	if err != nil {
		return CorpusSnapshot{}, err
	}
	return result, nil
}

// Snapshot export is a fresh sensitive read. Audit commits before bytes return.
func (s *Store) CorpusSnapshotContent(ctx context.Context, token, group, id string) ([]byte, error) {
	var result []byte
	err := s.write(ctx, func(tx *sql.Tx) error {
		actor, err := s.evolutionGroupSession(ctx, tx, token, group, "config.manage")
		if err != nil {
			return err
		}
		if err = authorizeCorpusEvidence(ctx, tx, actor, group); err != nil {
			return err
		}
		var source, digest string
		var revision int64
		if err = tx.QueryRowContext(ctx, "SELECT source_id,source_revision,body,digest FROM corpus_snapshots WHERE group_id=? AND id=?", group, id).Scan(&source, &revision, &result, &digest); err != nil {
			return missing(err)
		}
		if _, err = sourcePolicy(ctx, tx, group, source, revision); err != nil {
			return err
		}
		if evolution.Digest(result) != digest {
			return ErrInvalid
		}
		return s.auditEvent(ctx, tx, actor.ID, "corpus.export", "snapshot", id, "", revision, map[string]string{"digest": digest})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type CorpusConsumption struct {
	Through    int64  `json:"through_sequence"`
	SnapshotID string `json:"snapshot_id"`
	RunID      string `json:"run_id"`
}

func (s *Store) CorpusFrontier(ctx context.Context, token, group, binding, source string) (CorpusConsumption, error) {
	var result CorpusConsumption
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.evolutionGroupSession(ctx, tx, token, group, "config.read"); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, "SELECT through_sequence,snapshot_id,run_id FROM corpus_consumption WHERE group_id=? AND binding_id=? AND source_id=?", group, binding, source).Scan(&result.Through, &result.SnapshotID, &result.RunID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return result, err
}

// ConsumeCorpusSnapshot advances only the successful binding that actually pinned
// this snapshot. Sibling goals and failed runs retain their own independent frontier.
func (s *Store) ConsumeCorpusSnapshot(ctx context.Context, grant evolution.GenerationGrant, snapshotID string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		assignment, state, _, _, err := s.generationGrant(ctx, tx, grant)
		if err != nil {
			return err
		}
		if state != "completed" {
			return ErrConflict
		}
		if _, err = s.checkGeneration(ctx, tx, assignment, false); err != nil {
			return err
		}
		return consumeCorpusSnapshot(ctx, tx, assignment, snapshotID)
	})
}

func consumeCorpusSnapshot(ctx context.Context, tx *sql.Tx, assignment evolution.GenerationAssignment, snapshotID string) error {
	var err error
	var source, digest string
	var through int64
	if err = tx.QueryRowContext(ctx, "SELECT source_id,digest,sealed_through FROM corpus_snapshots WHERE group_id=? AND id=?", assignment.GroupID, snapshotID).Scan(&source, &digest, &through); err != nil {
		return missing(err)
	}
	found := false
	for _, input := range assignment.Pins.Inputs {
		if input.ID == snapshotID && input.Digest == digest {
			found = true
			break
		}
	}
	if !found {
		return ErrForbidden
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO corpus_consumption VALUES(?,?,?,?,?,?) ON CONFLICT(group_id,binding_id,source_id) DO UPDATE SET through_sequence=excluded.through_sequence,snapshot_id=excluded.snapshot_id,run_id=excluded.run_id WHERE excluded.through_sequence>corpus_consumption.through_sequence`, assignment.GroupID, assignment.BindingID, source, through, snapshotID, assignment.RunID)
	return err
}
