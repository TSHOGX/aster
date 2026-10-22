// Package evaluator coordinates assigned providers without accepting producer
// scores or exposing held-out artifacts to generation workers.
package evaluator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"aster/contracts/evolution"
	"aster/evolution-space/internal/projections"
)

var ErrProviderJobNotFound = errors.New("evaluation_provider_job_not_found")

// Provider must authenticate every response as the exact registered service
// revision. Lookup must distinguish definitive absence from transport failure.
type Provider interface {
	Status(context.Context, string) (evolution.JobStatus, error)
	Submit(context.Context, evolution.EvaluationSubmission) (evolution.JobStatus, error)
	Cancel(context.Context, string, int64) error
	Result(context.Context, string) (projections.EvaluationCompletion, error)
}
type Authority interface {
	Check(context.Context, projections.EvaluationJob, bool) error
	Grant(context.Context, projections.EvaluationJob) (string, error)
}
type Registry interface {
	Resolve(context.Context, evolution.Revision) (Provider, error)
}
type Worker struct {
	Store         *projections.Store
	ID            string
	Authority     Authority
	Providers     Registry
	LeaseDuration time.Duration
}

// Step reconciles the same durable provider request before any submission. All
// network work is bounded below the lease lifetime; a lost response retains the
// dispatch receipt and spending hold for a later worker's authenticated lookup.
func (w *Worker) Step(ctx context.Context, group, id string) error {
	if w.Store == nil || w.Authority == nil || w.Providers == nil {
		return fmt.Errorf("evaluator dependencies required")
	}
	duration := w.LeaseDuration
	if duration == 0 {
		duration = time.Minute
	}
	if duration < time.Second || duration > 5*time.Minute {
		return fmt.Errorf("invalid evaluator lease duration")
	}
	job, err := w.Store.Evaluation(ctx, group, id)
	if err != nil {
		return err
	}
	if job.State == "completed" || job.State == "error" || job.State == "canceled" {
		return nil
	}
	if time.Now().After(job.CreatedAt.Add(time.Duration(job.Budget.TimeoutSeconds) * time.Second)) {
		if err = w.Store.CancelEvaluation(ctx, group, id); err != nil {
			return err
		}
		job, err = w.Store.Evaluation(ctx, group, id)
		if err != nil {
			return err
		}
		if job.State == "canceled" {
			return nil
		}
	}
	cleanup := job.State == "cancelling"
	if err = w.Authority.Check(ctx, job, cleanup); err != nil {
		if errors.Is(err, ErrExecutionRevoked) {
			return w.Store.CancelEvaluation(ctx, group, id)
		}
		return err
	}
	lease, err := w.Store.ClaimEvaluation(ctx, group, id, w.ID, duration)
	if err != nil {
		return err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = w.Store.YieldEvaluation(release, lease)
	}()
	work, cancel := context.WithTimeout(ctx, duration/2)
	defer cancel()
	job, err = w.Store.Evaluation(work, group, id)
	if err != nil {
		return err
	}
	cleanup = job.State == "cancelling"
	if err = w.Authority.Check(work, job, cleanup); err != nil {
		return err
	}
	provider, err := w.Providers.Resolve(work, job.Service)
	if err != nil {
		return err
	}
	if closer, ok := provider.(interface{ Close() }); ok {
		defer closer.Close()
	}
	grant, err := w.Authority.Grant(work, job)
	if err != nil {
		return err
	}
	submission, err := w.Store.PrepareEvaluationDispatch(work, lease, grant)
	if err != nil {
		return err
	}
	status, err := provider.Status(work, id)
	if errors.Is(err, ErrProviderJobNotFound) {
		if cleanup {
			// Definitive provider absence is the only zero-spend cancellation shortcut.
			_, err = w.Store.CompleteEvaluation(work, lease, projections.EvaluationCompletion{Result: evolution.EvaluationResult{JobID: id, Service: job.Service, Fence: submission.Fence, Subject: job.Subject, State: "canceled"}})
			return err
		}
		if err = w.Authority.Check(work, job, false); err != nil {
			return err
		}
		current, currentErr := w.Store.Evaluation(work, group, id)
		if currentErr != nil {
			return currentErr
		}
		if current.State != "running" {
			return nil
		}
		if time.Now().After(job.CreatedAt.Add(time.Duration(job.Budget.TimeoutSeconds) * time.Second)) {
			return w.Store.CancelEvaluation(work, group, id)
		}
		status, err = provider.Submit(work, submission)
	}
	if err != nil {
		return err
	}
	if status.JobID != id || status.Fence != submission.Fence {
		return fmt.Errorf("provider status identity mismatch")
	}
	terminal := status.State == "completed" || status.State == "error" || status.State == "canceled"
	if !terminal && status.State != "queued" && status.State != "running" {
		return fmt.Errorf("invalid provider evaluation state")
	}
	if cleanup && !terminal {
		if err = w.Authority.Check(work, job, true); err != nil {
			return err
		}
		if err = provider.Cancel(work, id, submission.Fence); err != nil {
			return err
		}
		return nil
	}
	if !terminal {
		return nil
	}
	completion, err := provider.Result(work, id)
	if err != nil {
		return err
	}
	if completion.Result.State != status.State {
		return fmt.Errorf("provider result state mismatch")
	}
	if err = w.Authority.Check(work, job, cleanup); err != nil {
		return err
	}
	_, err = w.Store.CompleteEvaluation(work, lease, completion)
	return err
}
