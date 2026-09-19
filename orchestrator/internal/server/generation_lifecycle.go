package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

func (s *Server) startEnsuredGeneration(ctx context.Context, session store.Session, ensured ensuredGeneration, failureMode generationStartFailureMode) error {
	allocation := ensured.Allocation
	startCtx := ctx
	var leaseKeeper *execution.StartLease
	if ensured.IsNew || ensured.RestoreFromCheckpoint {
		var err error
		startCtx, leaseKeeper, err = execution.HoldStartLease(ctx, s.cfg.Harness.Bridge.LeaseTTL.Duration, func(renewCtx context.Context) error {
			return s.store.RenewGenerationStartLease(renewCtx, store.RenewGenerationStartLeaseParams{SessionID: session.ID, GenerationID: allocation.GenerationID, Owner: allocation.Owner, LeaseTTL: s.cfg.Harness.Bridge.LeaseTTL.Duration, Now: time.Now().UTC()})
		})
		if err != nil {
			return err
		}
	}
	defer leaseKeeper.Stop()
	generationDetails, err := s.runtimeGenerationDetails(ctx, session.ID, allocation.GenerationID)
	if err != nil {
		return err
	}
	dataVolumes, err := s.ensureSessionRuntimeDataVolumes(ctx, session)
	if err != nil {
		if leaseErr := leaseKeeper.Err(); leaseErr != nil {
			return leaseErr
		}
		if ensured.RestoreFromCheckpoint {
			if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
				return retireErr
			}
			return err
		}
		if ensured.IsNew {
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
		}
		return err
	}
	preparedArtifacts := runtimeArtifactsFromDetails(generationDetails)
	resourceWorkerID := runtimeResourceWorkerID(s.ownerUUID, allocation.Owner)
	resourceHostID := runtimeResourceHostID()
	var runtimeResourceCreated bool
	var runtimeResourceInstance store.RuntimeResourceInstance
	retireRuntimeResource := func() {
		if !runtimeResourceCreated {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.ClaimRuntimeResourceRetiring(cleanupCtx, store.RuntimeResourceRetireParams{
			GenerationID: allocation.GenerationID,
			WorkerID:     resourceWorkerID,
			HostID:       resourceHostID,
			Now:          time.Now().UTC(),
		}); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("failed to retire runtime resource after start failure", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", err)
		}
	}
	if ensured.IsNew {
		preparedArtifacts, err = s.runtime.PrepareGeneration(startCtx, s.runtimeStartRequest(session, allocation.GenerationID, generationDetails, runtime.GenerationArtifacts{}, dataVolumes))
		if err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			if leaseErr := leaseKeeper.EnsureOwned(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			return err
		}
		runtimeResourceParams, err := s.runtimeResourceInstanceParams(generationDetails, preparedArtifacts, resourceHostID)
		if err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		_, resourceIdentityDigest, err := store.RuntimeResourceIdentityForParams(runtimeResourceParams)
		if err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		contractPayload, err := s.sandboxContractPayload(session, generationDetails, preparedArtifacts, resourceIdentityDigest, dataVolumes)
		if err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		inputEvidence, err := s.sandboxContractInputEvidenceFor(session, generationDetails.Agent)
		if err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if _, err := s.store.StoreSandboxContract(ctx, store.StoreSandboxContractParams{
			ContractID:             sandboxContractID(allocation.GenerationID),
			SessionID:              session.ID,
			GenerationID:           allocation.GenerationID,
			Owner:                  allocation.Owner,
			SandboxContractVersion: store.SandboxContractVersion,
			ContractGateVersion:    store.SandboxContractGateDriverManifest,
			DriverState:            allocation.DriverState,
			Payload:                contractPayload,
			RuntimeConfigDigest:    inputEvidence.RuntimeConfigDigest,
			RuntimeConfigPreimage:  inputEvidence.RuntimeConfigPreimage,
			AgentManifestDigest:    inputEvidence.AgentManifestDigest,
			AgentManifestPayload:   inputEvidence.AgentManifestPayload,
			Now:                    time.Now().UTC(),
		}); err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			return err
		}
		if err := s.store.RecordGenerationRuntimeArtifactDigests(ctx, allocation.GenerationID, runtimeArtifactDigests(preparedArtifacts)); err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			return err
		}
		if _, err := s.store.RecordSandboxContractArtifacts(ctx, store.RecordSandboxContractArtifactsParams{
			ContractID:            sandboxContractID(allocation.GenerationID),
			ControlManifestDigest: preparedArtifacts.ManifestDigest,
			OCISpecDigest:         preparedArtifacts.SpecDigest,
			BundleDigest:          preparedArtifacts.BundleDigest,
			Now:                   time.Now().UTC(),
		}); err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			return err
		}
		instance, err := s.createRuntimeResourceInstance(ctx, runtimeResourceParams)
		if err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		runtimeResourceInstance = instance
		runtimeResourceCreated = true
		materializeNow := time.Now().UTC()
		if err := s.store.ClaimRuntimeResourceMaterialization(ctx, store.RuntimeResourceMaterializationClaimParams{
			GenerationID:     allocation.GenerationID,
			WorkerID:         resourceWorkerID,
			HostID:           resourceHostID,
			LeaseExpiresAt:   materializeNow.Add(s.cfg.Harness.Bridge.LeaseTTL.Duration),
			IdempotencyToken: "start:" + allocation.GenerationID,
			Now:              materializeNow,
		}); err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			retireRuntimeResource()
			return err
		}
		if err := s.store.MarkRuntimeResourceReady(ctx, store.RuntimeResourceWorkerTransitionParams{
			GenerationID: allocation.GenerationID,
			WorkerID:     resourceWorkerID,
			HostID:       resourceHostID,
			Now:          time.Now().UTC(),
		}); err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			retireRuntimeResource()
			return err
		}
		if err := s.store.MarkGenerationStarting(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			retireRuntimeResource()
			return err
		}
	}
	if ensured.RestoreFromCheckpoint {
		instance, resourceTracked, err := s.prepareRuntimeResourceRestore(ctx, allocation.GenerationID, resourceWorkerID, resourceHostID, s.cfg.Harness.Bridge.LeaseTTL.Duration)
		if err != nil {
			if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
				return retireErr
			}
			return err
		}
		runtimeResourceCreated = resourceTracked
		runtimeResourceInstance = instance
	}
	if runtimeResourceCreated {
		generationDetails = runtimeDetailsWithResourceInstance(generationDetails, runtimeResourceInstance)
	}
	if err := validateDriverStateForRuntimeLaunch(generationDetails, dataVolumes); err != nil {
		if ensured.RestoreFromCheckpoint {
			retireRuntimeResource()
			if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
				return retireErr
			}
			return err
		}
		retireRuntimeResource()
		s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
		return err
	}
	startReq := s.runtimeStartRequest(session, allocation.GenerationID, generationDetails, preparedArtifacts, dataVolumes)
	startReq.RestoreFromCheckpoint = ensured.RestoreFromCheckpoint
	result := s.runtime.Start(startCtx, startReq, nil)
	if result.Err != nil {
		if leaseErr := leaseKeeper.Err(); leaseErr != nil {
			return leaseErr
		}
		if leaseErr := leaseKeeper.EnsureOwned(); leaseErr != nil {
			return leaseErr
		}
		if ensured.RestoreFromCheckpoint {
			retireRuntimeResource()
			if err := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, result.Err); err != nil {
				return err
			}
			return result.Err
		}
		retireRuntimeResource()
		s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, result.Err, failureMode)
		return result.Err
	}
	if err := leaseKeeper.EnsureOwned(); err != nil {
		if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
			s.log.Warn("failed to destroy runtime after start lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
			return destroyErr
		}
		retireRuntimeResource()
		return err
	}
	bridgeStartupEvidence := ""
	if runtimeResourceCreated {
		renewRuntimeResourceWorkerLease := func() error {
			now := time.Now().UTC()
			return s.store.RenewRuntimeResourceWorkerLease(ctx, store.RuntimeResourceWorkerLeaseRenewalParams{
				GenerationID:   allocation.GenerationID,
				WorkerID:       resourceWorkerID,
				HostID:         resourceHostID,
				LeaseExpiresAt: now.Add(s.cfg.Harness.Bridge.LeaseTTL.Duration),
				Now:            now,
			})
		}
		if err := renewRuntimeResourceWorkerLease(); err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after resource lease renewal failure", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			if ensured.RestoreFromCheckpoint {
				if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
					return retireErr
				}
			} else {
				s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			}
			return err
		}
		bridgeStartupEvidence, err = s.waitForBridgeStartupReadiness(startCtx, allocation, runtimeResourceInstance)
		if err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
					s.log.Warn("failed to destroy runtime after bridge startup lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
					return destroyErr
				}
				retireRuntimeResource()
				return leaseErr
			}
			if leaseErr := leaseKeeper.EnsureOwned(); leaseErr != nil {
				if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
					s.log.Warn("failed to destroy runtime after bridge startup lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
					return destroyErr
				}
				retireRuntimeResource()
				return leaseErr
			}
			if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after bridge startup probe failure", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			if ensured.RestoreFromCheckpoint {
				if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
					return retireErr
				}
			} else {
				s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			}
			return err
		}
		if err := renewRuntimeResourceWorkerLease(); err != nil {
			if leaseErr := leaseKeeper.Err(); leaseErr != nil {
				return leaseErr
			}
			if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after resource lease renewal failure", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			if ensured.RestoreFromCheckpoint {
				if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
					return retireErr
				}
			} else {
				s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			}
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after bridge startup lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			return err
		}
	}
	if ensured.IsNew {
		postStartProof, err := runtimeResourcePostStartProof(runtimeResourceInstance, result, bridgeStartupEvidence)
		if err != nil {
			if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after missing post-start proof", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := s.store.MarkRuntimeResourceLive(ctx, store.RuntimeResourceWorkerTransitionParams{
			GenerationID: allocation.GenerationID,
			WorkerID:     resourceWorkerID,
			HostID:       resourceHostID,
			PostStart:    &postStartProof,
			Now:          time.Now().UTC(),
		}); err != nil {
			if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after resource live CAS failure", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after resource live lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			return err
		}
		if err := s.store.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
			if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after generation live CAS failure", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after generation live lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			return err
		}
	}
	if ensured.RestoreFromCheckpoint {
		if runtimeResourceCreated {
			postStartProof, err := runtimeResourcePostStartProof(runtimeResourceInstance, result, bridgeStartupEvidence)
			if err != nil {
				if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
					s.log.Warn("failed to destroy runtime after restore missing post-start proof", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
					return destroyErr
				}
				retireRuntimeResource()
				if leaseErr := leaseKeeper.EnsureOwned(); leaseErr != nil {
					return leaseErr
				}
				if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
					return retireErr
				}
				return err
			}
			if err := s.store.MarkRuntimeResourceLive(ctx, store.RuntimeResourceWorkerTransitionParams{
				GenerationID: allocation.GenerationID,
				WorkerID:     resourceWorkerID,
				HostID:       resourceHostID,
				PostStart:    &postStartProof,
				Now:          time.Now().UTC(),
			}); err != nil {
				if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
					s.log.Warn("failed to destroy runtime after restore resource live CAS failure", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
					return destroyErr
				}
				retireRuntimeResource()
				if leaseErr := leaseKeeper.EnsureOwned(); leaseErr != nil {
					return leaseErr
				}
				if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
					return retireErr
				}
				return err
			}
		}
		if err := s.store.MarkGenerationResourcesLive(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
			if destroyErr := s.destroyGenerationRuntime(ctx, generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after restore live CAS failure", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			retireRuntimeResource()
			if leaseErr := leaseKeeper.EnsureOwned(); leaseErr != nil {
				return leaseErr
			}
			if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
				return retireErr
			}
			return err
		}
		if err := leaseKeeper.EnsureOwned(); err != nil {
			if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after restore start lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			return err
		}
	}
	return nil
}

func (s *Server) failGenerationBeforeTurn(session store.Session, generationID, owner string, failure error, failureMode generationStartFailureMode) {
	reason := ""
	if failure != nil {
		reason = failure.Error()
	}
	errorClass := runtimeFailureClass(reason)
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	retryableStatus := string(sessionstate.RunningIdle)
	if failureMode == startFailureInputBlocking {
		retryableStatus = string(sessionstate.RunningActive)
	} else if session.Status == string(sessionstate.Created) {
		retryableStatus = string(sessionstate.Created)
	}
	eventID, err := s.store.FailGenerationStart(ctx, store.FailGenerationStartParams{
		SessionID:      session.ID,
		GenerationID:   generationID,
		Owner:          owner,
		SessionStatus:  retryableStatus,
		ErrorClass:     errorClass,
		Reason:         reason,
		EventType:      "generation.error",
		EventDedupeKey: "generation_error:" + generationID,
		Now:            now,
	})
	if err != nil {
		s.log.Warn("failed to fail generation before turn start", "session_id", session.ID, "generation_id", generationID, "error", err)
		return
	}
	s.publishDurableEvent(ctx, eventID)
}

func (s *Server) retireGenerationForRestoreFallback(sessionID, generationID, owner string, failure error) error {
	reason := ""
	if failure != nil {
		reason = failure.Error()
	}
	errorClass := runtimeFailureClass(reason)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	retired, err := s.store.RetireRestoreFallback(ctx, store.RetireRestoreFallbackParams{
		SessionID:    sessionID,
		GenerationID: generationID,
		Owner:        owner,
		ErrorClass:   errorClass,
		Reason:       reason,
		Now:          time.Now().UTC(),
	})
	if err != nil {
		s.log.Warn("failed to retire generation before restore fallback", "session_id", sessionID, "generation_id", generationID, "error", err)
		return err
	}
	for _, eventID := range retired.EventIDs {
		s.publishDurableEvent(ctx, eventID)
	}
	return nil
}

func (s *Server) ensureActiveGeneration(ctx context.Context, session store.Session, owner string) (ensuredGeneration, error) {
	result, err := (execution.Service{Generations: legacyGenerationRepository{server: s}}).EnsureGeneration(ctx, execution.Session{ID: session.ID, Driver: session.Agent, ActiveGenerationID: session.ActiveGenerationID}, owner)
	if err != nil {
		return ensuredGeneration{}, err
	}
	generation := result.Generation
	return ensuredGeneration{Allocation: store.GenerationAllocation{GenerationID: generation.ID, Owner: generation.Owner, DriverState: store.DriverStateToken{DriverID: generation.DriverState.DriverID, StateDigest: generation.DriverState.Digest, StateVersion: generation.DriverState.Version}}, IsNew: result.New, RestoreFromCheckpoint: result.Resume}, nil
}

// writeEnsureGenerationError maps an ensureActiveGeneration failure onto its HTTP
// response, returning false when it wrote one. Pool exhaustion and a busy
// generation are the caller's normal answers, not faults.
func (s *Server) writeEnsureGenerationError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrPoolExhausted):
		writeErrorClass(w, http.StatusServiceUnavailable, "pool_exhausted", "resource pool exhausted")
	case errors.Is(err, execution.ErrBusy):
		writeError(w, http.StatusConflict, "session is busy")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
	return false
}

// ensureRunnableGenerationHTTP gives a request a live sandbox to dispatch into,
// waking a session whose sandbox was reclaimed, and writes the HTTP error itself
// when it cannot (returning false).
//
// Shared by every handler that puts a turn in front of the agent: a new prompt
// (sendMessage) and releasing one the user parked with Stop
// (releaseQueuedTurn). Releasing needs it for a reason that is easy to miss —
// parked turns are excluded from the pending-turn predicate, precisely so a
// parked queue cannot hold a sandbox open, so auto-checkpoint and idle-release
// are free to reclaim it while those rows are still on screen. Without this the
// ↑ button on a parked row would answer "session is not running" forever, and
// the only way out would be to type the prompt again.
//
// A no-op for a session that is already running_active: it has a generation, and
// re-ensuring one would only risk disturbing the turn in flight.
func (s *Server) ensureRunnableGenerationHTTP(w http.ResponseWriter, r *http.Request, session store.Session) bool {
	if !sessionstate.CanAcceptInput(session.Status) && session.Status != string(sessionstate.RunningActive) {
		writeError(w, http.StatusConflict, "session is "+session.Status)
		return false
	}
	if session.Status == string(sessionstate.RunningActive) {
		return true
	}
	leaseOwner := store.GenerationLeaseOwner(s.ownerUUID)
	ensured, err := s.ensureActiveGeneration(r.Context(), session, leaseOwner)
	if !s.writeEnsureGenerationError(w, err) {
		return false
	}
	startErr := s.startEnsuredGeneration(r.Context(), session, ensured, startFailureInputAcceptable)
	if startErr == nil {
		return true
	}
	if !ensured.RestoreFromCheckpoint {
		writeRuntimeStartError(w, startErr)
		return false
	}
	// A checkpoint that will not restore is not fatal: fall back to a cold start
	// against the retained workspace, exactly as a new prompt does.
	//
	// Deliberately not writeEnsureGenerationError: the restore attempt above just
	// retired that generation, so "busy" here is not the normal answer it is on the
	// first try — it means the state is inconsistent, and reporting 409 would tell
	// the client to retry a session that will never become free on its own.
	ensured, err = s.ensureActiveGeneration(r.Context(), session, leaseOwner)
	if errors.Is(err, store.ErrPoolExhausted) {
		writeErrorClass(w, http.StatusServiceUnavailable, "pool_exhausted", "resource pool exhausted")
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ensured.IsNew {
		writeError(w, http.StatusInternalServerError, "restore fallback did not allocate a replacement generation")
		return false
	}
	if err := s.startEnsuredGeneration(r.Context(), session, ensured, startFailureInputAcceptable); err != nil {
		writeRuntimeStartError(w, err)
		return false
	}
	return true
}
