package evaluator

import (
	"context"
	"errors"

	"aster/contracts/evolution"
	"aster/evaluation-space/internal/identity"
	"aster/evaluation-space/internal/projections"
)

var ErrExecutionRevoked = errors.New("evaluation_execution_revoked")

type CentralAuthority struct {
	Identity *identity.ProjectionAuthority
	Store    *projections.Store
}

func (a CentralAuthority) Check(ctx context.Context, job projections.EvaluationJob, cleanup bool) error {
	if a.Identity == nil || a.Store == nil || job.Grant == nil {
		return identity.ErrUnauthorized
	}
	action := "execute"
	if cleanup {
		action = "cleanup"
	}
	_, err := a.Identity.ResolveEvaluation(ctx, evolution.EvaluationGrantScope{Grant: *job.Grant, Action: action})
	if errors.Is(err, identity.ErrUnauthorized) && !cleanup {
		return ErrExecutionRevoked
	}
	if err != nil {
		return err
	}
	if !cleanup {
		state, err := a.Store.ServiceState(ctx, job.Service.ID)
		if err != nil {
			return err
		}
		if !state.Enabled {
			return ErrExecutionRevoked
		}
	}
	return nil
}
func (a CentralAuthority) Grant(ctx context.Context, job projections.EvaluationJob) (string, error) {
	if job.Grant == nil {
		return "", identity.ErrUnauthorized
	}
	return job.Grant.ID, nil
}
