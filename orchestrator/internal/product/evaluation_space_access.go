package product

import (
	"aster/contracts/appidentity"
	"context"
	"database/sql"
	"errors"
	"time"
)

// EvaluationSpaceAccess resolves identity and all group metadata in one snapshot.
// The private HTTP boundary authenticates the registered Evaluation Space client.
func (s *Store) EvaluationSpaceAccess(ctx context.Context, input appidentity.EvaluationAccessRequest) (appidentity.EvaluationAccess, error) {
	result := appidentity.EvaluationAccess{Items: []appidentity.EvaluationGroup{}}
	if !input.ValidScope() {
		return result, ErrInvalid
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	session, err := identityGrantSession(ctx, tx, "evaluation-space", input.Grant)
	if err != nil {
		return result, err
	}
	assertion, err := s.identitySession(ctx, tx, session, "evaluation-space")
	if err != nil {
		return result, err
	}
	actor := Account{ID: assertion.Subject}
	if err = tx.QueryRowContext(ctx, "SELECT role FROM accounts WHERE id=?", actor.ID).Scan(&actor.Role); err != nil {
		return result, err
	}
	result.Subject = actor.ID
	result.ExpiresAt, err = time.Parse(time.RFC3339Nano, assertion.ExpiresAt)
	if err != nil {
		return result, err
	}
	// Denials and membership are applied before limit/lookahead. No foreign group
	// contributes names, counts or cursor evidence to this response.
	rows, err := tx.QueryContext(ctx, `SELECT g.id,g.name,g.description,COALESCE(c.desired_revision,0),COALESCE(c.active_revision,0),COALESCE(c.auto_enabled,0),COALESCE(c.state,'unconfigured')
 FROM account_groups g LEFT JOIN evolution_group_configs c ON c.group_id=g.id
 WHERE (?='' OR g.id=?) AND g.id>? AND instr(lower(g.name||' '||g.id),lower(?))>0
 AND NOT EXISTS(SELECT 1 FROM evolution_group_grants p WHERE p.group_id=g.id AND p.account_id=? AND p.action='config.read' AND p.effect='deny')
 AND (?='admin' OR (EXISTS(SELECT 1 FROM account_memberships m WHERE m.group_id=g.id AND m.account_id=?) AND EXISTS(SELECT 1 FROM evolution_group_grants p WHERE p.group_id=g.id AND p.account_id=? AND p.action='config.read' AND p.effect='allow')))
 ORDER BY g.id LIMIT ?`, input.GroupID, input.GroupID, input.After, input.Search, actor.ID, actor.Role, actor.ID, actor.ID, input.Limit+1)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var group appidentity.EvaluationGroup
		if err = rows.Scan(&group.ID, &group.Name, &group.Description, &group.DesiredRevision, &group.ActiveRevision, &group.AutoEnabled, &group.State); err != nil {
			rows.Close()
			return result, err
		}
		if len(result.Items) == input.Limit {
			result.HasMore = true
			break
		}
		result.Items = append(result.Items, group)
		result.NextAfter = group.ID
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if input.GroupID != "" && len(result.Items) != 1 {
		return result, ErrForbidden
	}
	for i := range result.Items {
		group := &result.Items[i]
		check := func(action string) (bool, error) {
			var err error
			if action == "source.read" {
				err = authorizeCorpusEvidence(ctx, tx, actor, group.ID)
			} else {
				err = authorizeEvolutionGroup(ctx, tx, actor, group.ID, action)
			}
			if errors.Is(err, ErrForbidden) || errors.Is(err, ErrUnauthorized) {
				return false, nil
			}
			return err == nil, err
		}
		if group.CanEditWorkflows, err = check("workflow.edit"); err != nil {
			return result, err
		}
		if group.CanManage, err = check("config.manage"); err != nil {
			return result, err
		}
		if group.CanRun, err = check("run.start"); err != nil {
			return result, err
		}
		if group.CanReadEvidence, err = check("source.read"); err != nil {
			return result, err
		}
		if input.GroupID != "" {
			allowed, err := check(input.Action)
			if err != nil {
				return result, err
			}
			if !allowed {
				return result, ErrForbidden
			}
		}
	}
	return result, nil
}
