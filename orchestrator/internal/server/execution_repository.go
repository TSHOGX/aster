package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/store"
)

// legacyGenerationRepository translates the current database's runtime records
// to application lifecycle values. It keeps network/profile allocation and
// legacy restore details out of the execution service.
type legacyGenerationRepository struct{ server *Server }

func (r legacyGenerationRepository) Session(ctx context.Context, id string) (execution.Session, error) {
	session, err := r.server.store.GetSession(ctx, id)
	return execution.Session{ID: session.ID, Driver: session.Agent, ActiveGenerationID: session.ActiveGenerationID}, err
}
func (r legacyGenerationRepository) State(ctx context.Context, sessionID, generationID string) (execution.GenerationState, error) {
	state, err := r.server.store.GetRuntimeGenerationStatus(ctx, sessionID, generationID)
	if err != nil {
		return "", err
	}
	switch state {
	case "idle", "active":
		return execution.GenerationUsable, nil
	case "checkpointed":
		return execution.GenerationRetained, nil
	case "failed":
		return execution.GenerationFailed, nil
	default:
		return execution.GenerationTransitioning, nil
	}
}
func (r legacyGenerationRepository) Reserve(ctx context.Context, session execution.Session, owner, previous string) (execution.Generation, error) {
	s := r.server
	if _, err := s.resolveDriverDeployment(store.ModeForDriver(session.Driver), agents.ID(session.Driver)); err != nil {
		return execution.Generation{}, err
	}
	allocation, err := s.store.AllocateGeneration(ctx, store.AllocateGenerationParams{SessionID: session.ID, ExpectedGenerationID: sql.NullString{String: previous, Valid: previous != ""}, Owner: owner, LeaseTTL: s.cfg.Harness.Bridge.LeaseTTL.Duration, Now: time.Now().UTC(), Config: s.resourceAllocatorConfig(session.Driver)})
	return applicationGeneration(allocation), err
}
func (r legacyGenerationRepository) Resume(ctx context.Context, session execution.Session, owner string) (execution.Generation, error) {
	allocation, err := r.server.store.ClaimCheckpointedGenerationForRestore(ctx, store.ClaimCheckpointedGenerationParams{SessionID: session.ID, GenerationID: session.ActiveGenerationID, Owner: owner, LeaseTTL: r.server.cfg.Harness.Bridge.LeaseTTL.Duration, Now: time.Now().UTC()})
	if errors.Is(err, store.ErrStaleCheckpointRestore) {
		return execution.Generation{}, fmt.Errorf("%w: %v", execution.ErrResumeConflict, err)
	}
	return applicationGeneration(allocation), err
}
func applicationGeneration(allocation store.GenerationAllocation) execution.Generation {
	return execution.Generation{ID: allocation.GenerationID, Owner: allocation.Owner, DriverState: execution.DriverState{DriverID: allocation.DriverState.DriverID, Digest: allocation.DriverState.StateDigest, Version: allocation.DriverState.StateVersion}}
}
