package product

import (
	"aster/contracts/evolution"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Source policies are central authorization records. Configuration authors must
// pin these records; a configuration's source flags never create a grant.
type EvolutionSourcePolicyInput struct {
	RequestID        string                  `json:"request_id"`
	ExpectedRevision int64                   `json:"expected_revision"`
	Source           evolution.SourceBinding `json:"source"`
}

func (s *Store) SetEvolutionSourcePolicy(ctx context.Context, token, group string, input EvolutionSourcePolicyInput) (evolution.SourceBinding, error) {
	var result evolution.SourceBinding
	source := input.Source
	check := evolution.Configuration{GroupID: group, Revision: 1, Sources: []evolution.SourceBinding{source}, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	if input.ExpectedRevision < 0 || source.PolicyRevision != input.ExpectedRevision+1 || source.CaptureFrontier != "" || check.Validate() != nil {
		return result, ErrInvalid
	}
	raw, err := json.Marshal(source)
	if err != nil || len(raw) > evolution.MaxDocumentBytes {
		return result, ErrInvalid
	}
	request, err := mutationRequest(input.RequestID, "evolution.source.policy", group+"/"+source.ID, input)
	if err != nil {
		return result, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		session, err := s.normalLogin(ctx, tx, token)
		if err != nil {
			return err
		}
		if err := authorizeEvolutionSourceIssuer(ctx, tx, session.Account.ID, source); err != nil {
			return err
		}
		receipt, replayed, err := readAdminReceipt(ctx, tx, session.Account.ID, request)
		if err != nil {
			return err
		}
		if replayed {
			return json.Unmarshal(receipt, &result)
		}
		var revision int64
		var existingIssuer string
		err = tx.QueryRowContext(ctx, "SELECT revision,issuer_id FROM evolution_source_policies WHERE group_id=? AND source_id=?", group, source.ID).Scan(&revision, &existingIssuer)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if existingIssuer != "" && existingIssuer != session.Account.ID {
			return ErrForbidden
		}
		if revision != input.ExpectedRevision {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO evolution_source_policies(issuer_id,group_id,source_id,revision,body) VALUES(?,?,?,?,?) ON CONFLICT(group_id,source_id) DO UPDATE SET issuer_id=excluded.issuer_id,revision=excluded.revision,body=excluded.body`, session.Account.ID, group, source.ID, source.PolicyRevision, string(raw)); err != nil {
			return err
		}
		if err = s.writeAdminReceipt(ctx, tx, session.Account.ID, request, raw); err != nil {
			return err
		}
		result = source
		return s.auditEvent(ctx, tx, session.Account.ID, "evolution.source.policy", "group", group, input.RequestID, source.PolicyRevision, source)
	})
	return result, err
}

func authorizeEvolutionSources(ctx context.Context, q queryer, config evolution.Configuration) error {
	for _, source := range config.Sources {
		var raw, issuer string
		if err := q.QueryRowContext(ctx, "SELECT body,issuer_id FROM evolution_source_policies WHERE group_id=? AND source_id=? AND revision=?", config.GroupID, source.ID, source.PolicyRevision).Scan(&raw, &issuer); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrForbidden
			}
			return err
		}
		if err := authorizeEvolutionSourceIssuer(ctx, q, issuer, source); err != nil {
			return err
		}
		expected, err := json.Marshal(source)
		if err != nil || string(expected) != raw {
			return ErrForbidden
		}
	}
	return nil
}

func authorizeEvolutionSourceIssuer(ctx context.Context, q queryer, issuer string, source evolution.SourceBinding) error {
	if err := activeAccount(ctx, q, issuer); err != nil {
		return ErrForbidden
	}
	var count int
	switch source.Kind {
	case "project":
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE id=? AND owner_id=? AND state NOT IN ('deleting','deleted')", source.ResourceID, issuer).Scan(&count); err != nil {
			return err
		}
	case "session":
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM sessions s JOIN projects p ON p.id=s.project_id WHERE s.id=? AND p.owner_id=? AND p.state NOT IN ('deleting','deleted') AND s.deletion_intent=0", source.ResourceID, issuer).Scan(&count); err != nil {
			return err
		}
	case "volume":
		for _, action := range []string{"volume.read", "volume.configure"} {
			decision, err := studioAccess(ctx, q, evolution.AccessRequest{Principal: evolution.Principal{Kind: "human", Subject: issuer}, LibraryID: source.ResourceID, Action: action})
			if err != nil {
				return err
			}
			if !decision.Allowed {
				return ErrForbidden
			}
		}
		return nil
	default:
		return ErrInvalid
	}
	if count != 1 {
		return ErrForbidden
	}
	return nil
}
