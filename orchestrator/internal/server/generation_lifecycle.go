package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"harness-platform/orchestrator/internal/agents"
	"harness-platform/orchestrator/internal/runtime"
	"harness-platform/orchestrator/internal/sessionstate"
	"harness-platform/orchestrator/internal/store"
)

func (s *Server) startEnsuredGeneration(ctx context.Context, session store.Session, ensured ensuredGeneration, failureMode generationStartFailureMode) error {
	allocation := ensured.Allocation
	startCtx := ctx
	leaseKeeper := noopStartLeaseKeeper()
	if ensured.IsNew || ensured.RestoreFromCheckpoint {
		var err error
		startCtx, leaseKeeper, err = s.beginGenerationStartLease(ctx, session.ID, allocation.GenerationID, allocation.Owner)
		if err != nil {
			return err
		}
	}
	defer leaseKeeper.stop()
	generationDetails, err := s.runtimeGenerationDetails(ctx, session.ID, allocation.GenerationID)
	if err != nil {
		return err
	}
	dataVolumes, err := s.ensureSessionRuntimeDataVolumes(ctx, session)
	if err != nil {
		if leaseErr := leaseKeeper.err(); leaseErr != nil {
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
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			if leaseErr := leaseKeeper.ensureOwned(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.ensureOwned(); err != nil {
			return err
		}
		runtimeResourceParams, err := s.runtimeResourceInstanceParams(generationDetails, preparedArtifacts, resourceHostID)
		if err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		_, resourceIdentityDigest, err := store.RuntimeResourceIdentityForParams(runtimeResourceParams)
		if err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		contractPayload, err := s.sandboxContractPayload(session, generationDetails, preparedArtifacts, resourceIdentityDigest, dataVolumes)
		if err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		inputEvidence, err := s.sandboxContractInputEvidenceFor(session, generationDetails.Agent)
		if err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
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
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.ensureOwned(); err != nil {
			return err
		}
		if err := s.store.RecordGenerationRuntimeArtifactDigests(ctx, allocation.GenerationID, runtimeArtifactDigests(preparedArtifacts)); err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.ensureOwned(); err != nil {
			return err
		}
		if _, err := s.store.RecordSandboxContractArtifacts(ctx, store.RecordSandboxContractArtifactsParams{
			ContractID:            sandboxContractID(allocation.GenerationID),
			ControlManifestDigest: preparedArtifacts.ManifestDigest,
			OCISpecDigest:         preparedArtifacts.SpecDigest,
			BundleDigest:          preparedArtifacts.BundleDigest,
			Now:                   time.Now().UTC(),
		}); err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.ensureOwned(); err != nil {
			return err
		}
		instance, err := s.createRuntimeResourceInstance(ctx, runtimeResourceParams)
		if err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
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
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.ensureOwned(); err != nil {
			retireRuntimeResource()
			return err
		}
		if err := s.store.MarkRuntimeResourceReady(ctx, store.RuntimeResourceWorkerTransitionParams{
			GenerationID: allocation.GenerationID,
			WorkerID:     resourceWorkerID,
			HostID:       resourceHostID,
			Now:          time.Now().UTC(),
		}); err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.ensureOwned(); err != nil {
			retireRuntimeResource()
			return err
		}
		if err := s.store.MarkGenerationStarting(ctx, session.ID, allocation.GenerationID, allocation.Owner, time.Now().UTC()); err != nil {
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				return leaseErr
			}
			retireRuntimeResource()
			s.failGenerationBeforeTurn(session, allocation.GenerationID, allocation.Owner, err, failureMode)
			return err
		}
		if err := leaseKeeper.ensureOwned(); err != nil {
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
		if leaseErr := leaseKeeper.err(); leaseErr != nil {
			return leaseErr
		}
		if leaseErr := leaseKeeper.ensureOwned(); leaseErr != nil {
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
	if err := leaseKeeper.ensureOwned(); err != nil {
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
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
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
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
				if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
					s.log.Warn("failed to destroy runtime after bridge startup lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
					return destroyErr
				}
				retireRuntimeResource()
				return leaseErr
			}
			if leaseErr := leaseKeeper.ensureOwned(); leaseErr != nil {
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
			if leaseErr := leaseKeeper.err(); leaseErr != nil {
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
		if err := leaseKeeper.ensureOwned(); err != nil {
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
		if err := leaseKeeper.ensureOwned(); err != nil {
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
		if err := leaseKeeper.ensureOwned(); err != nil {
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
				if leaseErr := leaseKeeper.ensureOwned(); leaseErr != nil {
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
				if leaseErr := leaseKeeper.ensureOwned(); leaseErr != nil {
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
			if leaseErr := leaseKeeper.ensureOwned(); leaseErr != nil {
				return leaseErr
			}
			if retireErr := s.retireGenerationForRestoreFallback(session.ID, allocation.GenerationID, allocation.Owner, err); retireErr != nil {
				return retireErr
			}
			return err
		}
		if err := leaseKeeper.ensureOwned(); err != nil {
			if destroyErr := s.destroyGenerationRuntime(context.Background(), generationDetails); destroyErr != nil && !errors.Is(destroyErr, context.Canceled) {
				s.log.Warn("failed to destroy runtime after restore start lease loss", "session_id", session.ID, "generation_id", allocation.GenerationID, "error", destroyErr)
				return destroyErr
			}
			return err
		}
	}
	return nil
}

type startLeaseKeeper struct {
	cancel context.CancelFunc
	done   chan struct{}
	renew  func() error

	mu      sync.Mutex
	failure error
}

func noopStartLeaseKeeper() *startLeaseKeeper {
	done := make(chan struct{})
	close(done)
	return &startLeaseKeeper{
		cancel: func() {},
		done:   done,
		renew:  func() error { return nil },
	}
}

func (s *Server) beginGenerationStartLease(ctx context.Context, sessionID, generationID, owner string) (context.Context, *startLeaseKeeper, error) {
	startCtx, cancel := context.WithCancel(ctx)
	ttl := s.cfg.Harness.Bridge.LeaseTTL.Duration
	keeper := &startLeaseKeeper{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	keeper.renew = func() error {
		renewCtx, renewCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer renewCancel()
		return s.store.RenewGenerationStartLease(renewCtx, store.RenewGenerationStartLeaseParams{
			SessionID:    sessionID,
			GenerationID: generationID,
			Owner:        owner,
			LeaseTTL:     ttl,
			Now:          time.Now().UTC(),
		})
	}
	if err := keeper.ensureOwned(); err != nil {
		cancel()
		close(keeper.done)
		return startCtx, keeper, err
	}
	go func() {
		defer close(keeper.done)
		ticker := time.NewTicker(startLeaseRenewalInterval(ttl))
		defer ticker.Stop()
		for {
			select {
			case <-startCtx.Done():
				return
			case <-ticker.C:
				if err := keeper.ensureOwned(); err != nil {
					return
				}
			}
		}
	}()
	return startCtx, keeper, nil
}

func startLeaseRenewalInterval(ttl time.Duration) time.Duration {
	interval := ttl / 2
	if interval < time.Millisecond {
		return time.Millisecond
	}
	return interval
}

func (k *startLeaseKeeper) ensureOwned() error {
	if err := k.getErr(); err != nil {
		return err
	}
	if k.renew == nil {
		return nil
	}
	if err := k.renew(); err != nil {
		wrapped := fmt.Errorf("%w: %v", errGenerationStartLeaseLost, err)
		k.setErr(wrapped)
		return wrapped
	}
	return k.getErr()
}

func (k *startLeaseKeeper) err() error {
	return k.getErr()
}

func (k *startLeaseKeeper) getErr() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.failure
}

func (k *startLeaseKeeper) setErr(err error) {
	if err == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failure != nil {
		return
	}
	k.failure = err
	k.cancel()
}

func (k *startLeaseKeeper) stop() {
	k.cancel()
	<-k.done
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
	return s.ensureActiveGenerationWithRestoreRefetch(ctx, session, owner, true)
}

func (s *Server) ensureActiveGenerationWithRestoreRefetch(ctx context.Context, session store.Session, owner string, allowRestoreRefetch bool) (ensuredGeneration, error) {
	activeGenerationID := strings.TrimSpace(session.ActiveGenerationID)
	if activeGenerationID != "" {
		status, err := s.store.GetRuntimeGenerationStatus(ctx, session.ID, activeGenerationID)
		if err != nil {
			return ensuredGeneration{}, err
		}
		if status == "checkpointed" {
			allocation, err := s.store.ClaimCheckpointedGenerationForRestore(ctx, store.ClaimCheckpointedGenerationParams{
				SessionID:    session.ID,
				GenerationID: activeGenerationID,
				Owner:        owner,
				LeaseTTL:     s.cfg.Harness.Bridge.LeaseTTL.Duration,
				Now:          time.Now().UTC(),
			})
			if err != nil {
				if allowRestoreRefetch && errors.Is(err, store.ErrStaleCheckpointRestore) {
					refreshed, refreshErr := s.store.GetSession(ctx, session.ID)
					if refreshErr != nil {
						return ensuredGeneration{}, refreshErr
					}
					return s.ensureActiveGenerationWithRestoreRefetch(ctx, refreshed, owner, false)
				}
				return ensuredGeneration{}, err
			}
			return ensuredGeneration{
				Allocation:            allocation,
				RestoreFromCheckpoint: true,
			}, nil
		}
		if generationLifecycleBusy(status) {
			return ensuredGeneration{}, errGenerationBusy
		}
		if status != "failed" {
			return ensuredGeneration{
				Allocation: store.GenerationAllocation{
					GenerationID: activeGenerationID,
					Owner:        owner,
				},
				IsNew: false,
			}, nil
		}
		if _, capabilityErr := s.resolveDriverDeployment(store.ModeForDriver(session.Agent), agents.ID(session.Agent)); capabilityErr != nil {
			return ensuredGeneration{}, capabilityErr
		}
		allocation, err := s.store.AllocateGeneration(ctx, store.AllocateGenerationParams{
			SessionID:            session.ID,
			ExpectedGenerationID: sql.NullString{String: activeGenerationID, Valid: true},
			Owner:                owner,
			LeaseTTL:             s.cfg.Harness.Bridge.LeaseTTL.Duration,
			Now:                  time.Now().UTC(),
			Config:               s.resourceAllocatorConfig(session.Agent),
		})
		if err != nil {
			return ensuredGeneration{}, err
		}
		return ensuredGeneration{Allocation: allocation, IsNew: true}, nil
	}
	if _, capabilityErr := s.resolveDriverDeployment(store.ModeForDriver(session.Agent), agents.ID(session.Agent)); capabilityErr != nil {
		return ensuredGeneration{}, capabilityErr
	}
	allocation, err := s.store.AllocateGeneration(ctx, store.AllocateGenerationParams{
		SessionID: session.ID,
		Owner:     owner,
		LeaseTTL:  s.cfg.Harness.Bridge.LeaseTTL.Duration,
		Now:       time.Now().UTC(),
		Config:    s.resourceAllocatorConfig(session.Agent),
	})
	if err != nil {
		return ensuredGeneration{}, err
	}
	return ensuredGeneration{Allocation: allocation, IsNew: true}, nil
}

func generationLifecycleBusy(status string) bool {
	switch status {
	case "allocating", "starting", "probing", "checkpointing", "restoring":
		return true
	default:
		return false
	}
}
