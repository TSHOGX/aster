package server

import (
	"context"
	"encoding/json"
	"time"

	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
	localworkspace "harness-platform/orchestrator/internal/workspace/local"
)

// The interfaces in this file are the server's own view of persistence. They
// follow the consumer-side rule: the package that needs the behaviour declares
// the interface it needs, and *store.Store satisfies all of them implicitly.
// This mirrors the pre-existing bridgeStore/runtimeDriver precedent in
// server.go and bridge.Store in internal/bridge.
//
// The 73 methods the server reaches for are grouped by concern rather than
// declared as one god interface, so that a handler file, a test double, or a
// future extraction only has to satisfy the slice it actually uses. serverStore
// composes the roles and is what the Server struct holds; handlers that only
// need one role can take the narrow role type directly.

// sessionStore covers the sessions table: lifecycle, ownership, status, titles,
// expiry sweeps, and the quota/idle bookkeeping that reads session rows.
type sessionStore interface {
	CreateSession(context.Context, store.Session) error
	GetSession(context.Context, string) (store.Session, error)
	ListSessionsByUser(context.Context, string) ([]store.Session, error)
	UpdateSessionStatusAndActivity(context.Context, string, string, *int64, time.Time) error
	UpdateSessionTitle(context.Context, string, string, bool) error
	DestroySession(context.Context, string, time.Time) (store.DestroySessionResult, error)
	InterruptSession(context.Context, store.InterruptSessionParams) (store.InterruptSessionResult, error)
	SessionHasActiveTurn(context.Context, string) (bool, error)
	GetSessionRuntimeSnapshot(context.Context, string) (store.SessionRuntimeSnapshot, error)
	SetTurnPhase(context.Context, store.SetTurnPhaseParams) (store.SessionRuntimeSnapshot, error)
	CancelQueuedTurn(context.Context, store.CancelQueuedTurnParams) (store.SessionRuntimeSnapshot, error)
	// ReleaseParkedTurn hands one turn parked by Stop back to the worker.
	ReleaseParkedTurn(context.Context, store.ReleaseParkedTurnParams) (store.SessionRuntimeSnapshot, error)
	// RecordSteeredTurn retires a queued turn whose content was injected into the
	// turn already running, and writes the transcript entry for it.
	RecordSteeredTurn(context.Context, store.RecordSteeredTurnParams) (store.RecordSteeredTurnResult, error)
	CountSandboxOccupyingSessions(context.Context) (int, error)
	GetResourceQuota(context.Context) (store.ResourceQuota, error)
	SweepExpiredSessions(context.Context, time.Time) (int64, error)
	ClearActiveSessionExpiry(context.Context, time.Time) (int64, error)
	ListColdFallbackSessions(context.Context) ([]store.ColdFallbackSession, error)
	ListIdleReleaseCandidates(context.Context, string, time.Time, time.Duration) ([]store.CheckpointCandidate, error)
}

// messageStore covers messages, turn enqueueing, per-message traces, feedback,
// and search. stream_parser.go reaches this role through p.srv.
type messageStore interface {
	ArtifactPage(context.Context, store.ArtifactPageParams) (store.ArtifactPage, error)
	AddMessage(context.Context, string, string, string) (store.Message, error)
	AddAssistantMessage(context.Context, string, string, string, string) (store.Message, error)
	// ListMessages has no per-viewer feedback join. The public-link path uses it
	// because an anonymous viewer has no identity to join against.
	ListMessages(context.Context, string) ([]store.Message, error)
	ListMessagesForUser(context.Context, string, string) ([]store.Message, error)
	ListMessagesPage(context.Context, store.MessagePageParams) (store.MessagePage, error)
	MessageContent(context.Context, store.MessagePageParams, int64, int, int) (store.ContentPage, error)
	MessageTrace(context.Context, store.MessagePageParams, int64, int, int) (store.TracePage, error)
	TraceContent(context.Context, store.MessagePageParams, int64, int, int, int) (store.ContentPage, error)
	TurnUserMessage(context.Context, string, int64) (store.Message, bool, error)
	MessageSession(context.Context, int64) (string, bool, error)
	SetMessageFeedback(context.Context, int64, string, string) (string, error)
	SaveMessageTrace(context.Context, int64, string, json.RawMessage) error
	SearchMessages(context.Context, string, string, int) ([]store.SearchHit, error)
	EnqueueTurnMessage(context.Context, store.EnqueueTurnMessageParams) (store.EnqueueTurnMessageResult, error)
	CancelTerminalSessionPendingTurns(context.Context, time.Time) (int64, error)
	// ResumeFailedTurn appends a continuation turn for a turn that failed on a
	// transient upstream fault. It writes no message row: the user did not type
	// the continuation prompt.
	ResumeFailedTurn(context.Context, store.ResumeFailedTurnParams) (store.ResumeFailedTurnResult, error)
}

// authStore covers login/auth sessions: issue, look up, revoke, sweep.
type authStore interface {
	CreateAuthSession(context.Context, store.AuthSession) error
	LookupAuthSession(context.Context, string, time.Time) (string, error)
	RevokeAuthSession(context.Context, string, time.Time) error
	SweepExpiredAuthSessions(context.Context, time.Time) (int64, error)
}

type platformStore interface {
	EnsureUserWithRole(context.Context, string, string, string) error
	UserRole(context.Context, string) (string, error)
	ListFeatureFlags(context.Context) ([]store.FeatureFlag, error)
	GetFeatureFlag(context.Context, string) (store.FeatureFlag, bool, error)
	FeatureEnabled(context.Context, string) bool
	SetFeatureFlag(context.Context, string, bool, string, string, time.Time) (store.FeatureFlag, error)
	AppendAudit(context.Context, store.AuditEntry) error
	ListAudit(context.Context, int, int) ([]store.AuditEntry, error)
}

type scheduleStore interface {
	CreateSchedule(context.Context, store.AgentSchedule) error
	GetSchedule(context.Context, string) (store.AgentSchedule, error)
	ListSchedulesByUser(context.Context, string) ([]store.AgentSchedule, error)
	ListDueSchedules(context.Context, time.Time, int) ([]store.AgentSchedule, error)
	AdvanceSchedule(context.Context, string, int, time.Time, time.Time) error
	ClaimScheduleDue(context.Context, string, int, time.Time, time.Time, store.ScheduleRun) error
	ListQueuedScheduleRuns(context.Context, time.Time, int) ([]store.ScheduleRun, error)
	ListRunningScheduleRuns(context.Context, int) ([]store.ScheduleRun, error)
	UpdateSchedule(context.Context, store.AgentSchedule, int) error
	DeleteSchedule(context.Context, string, string) error
	CreateScheduleRun(context.Context, store.ScheduleRun) error
	GetScheduleRun(context.Context, string, string) (store.ScheduleRun, error)
	GetScheduleRunByID(context.Context, string) (store.ScheduleRun, error)
	ListScheduleRuns(context.Context, string, string, int) ([]store.ScheduleRun, error)
	CountPendingScheduleRuns(context.Context, string) (int, error)
	CancelPendingScheduleRuns(context.Context, string, string, time.Time) (int, error)
	ListStaleSchedules(context.Context, time.Time, int) ([]store.AgentSchedule, error)
	ClaimScheduleRunStart(context.Context, string, time.Time) error
	AttachScheduleRunTurn(context.Context, string, string, int64, time.Time) error
	FinishScheduleRun(context.Context, string, string, string, string, string, time.Time) error
	TurnStatus(context.Context, string, int64) (string, string, error)
	LatestAssistantMessage(context.Context, string) (string, error)
}

// generationStore covers the runtime_generations lifecycle: allocation, start
// leases, live/destroyed resource transitions, checkpoint restore and retirement.
type generationStore interface {
	AllocateGeneration(context.Context, store.AllocateGenerationParams) (store.GenerationAllocation, error)
	ClaimCheckpointedGenerationForRestore(context.Context, store.ClaimCheckpointedGenerationParams) (store.GenerationAllocation, error)
	GetRuntimeGenerationDetails(context.Context, string, string) (store.RuntimeGenerationDetails, error)
	GetRuntimeGenerationStatus(context.Context, string, string) (string, error)
	SessionSandboxStates(context.Context) (map[string]store.SandboxState, error)
	MarkGenerationStarting(context.Context, string, string, string, time.Time) error
	MarkGenerationResourcesLive(context.Context, string, string, string, time.Time) error
	MarkGenerationResourcesDestroyed(context.Context, store.DestroyGenerationResourcesParams) error
	FailGenerationStart(context.Context, store.FailGenerationStartParams) (int64, error)
	FailGenerationWithEvent(context.Context, store.FailGenerationParams) (store.FailGenerationResult, error)
	InflightTurnForGeneration(context.Context, string, string) (int64, string, bool, error)
	RenewGenerationStartLease(context.Context, store.RenewGenerationStartLeaseParams) error
	RenewLiveGenerationLeases(context.Context, store.RenewLiveGenerationsParams) (int64, error)
	ReleaseIdleGeneration(context.Context, string, string, string, time.Time) error
	RetireExpiredCheckpoints(context.Context, store.RetireExpiredCheckpointsParams) ([]store.RetiredCheckpoint, error)
	RetireRestoreFallback(context.Context, store.RetireRestoreFallbackParams) (store.RetiredRestoreFallback, error)
	ListDestroyableReclaimableGenerations(context.Context, time.Time, time.Duration) ([]store.ReclaimableGeneration, error)
	ListExpiredRuntimeRecoveryCandidates(context.Context, store.StartupRecoveryParams) ([]store.ExpiredRuntimeRecoveryCandidate, error)
	RepairExpiredRuntimeRecovery(context.Context, store.StartupRecoveryParams, []store.ExpiredRuntimeRecoveryCandidate) (store.StartupRecoveryResult, error)
	ReconcileStaleSessionStates(context.Context, store.SessionRuntimeReconciliationParams) ([]store.SessionRuntimeReconciliation, error)
	RecordGenerationRuntimeArtifactDigests(context.Context, string, store.GenerationRuntimeArtifactDigests) error
	ReapResources(context.Context, store.ReaperParams) (store.ReaperResult, error)
}

// runtimeResourceStore covers the runtime_resource_instances worker state
// machine driven by runtime_resource_http.go and generation_lifecycle.go.
type runtimeResourceStore interface {
	CreateRuntimeResourceInstance(context.Context, store.RuntimeResourceInstanceParams) (store.RuntimeResourceInstance, error)
	GetRuntimeResourceInstance(context.Context, string) (store.RuntimeResourceInstance, error)
	GetRuntimeResourceCleanupIdentity(context.Context, string) (store.RuntimeResourceInstance, error)
	ClaimRuntimeResourceMaterialization(context.Context, store.RuntimeResourceMaterializationClaimParams) error
	ClaimRuntimeResourceCheckpointRestore(context.Context, store.RuntimeResourceMaterializationClaimParams) error
	ClaimRuntimeResourceRetiring(context.Context, store.RuntimeResourceRetireParams) error
	MarkRuntimeResourceReady(context.Context, store.RuntimeResourceWorkerTransitionParams) error
	MarkRuntimeResourceLive(context.Context, store.RuntimeResourceWorkerTransitionParams) error
	MarkRuntimeResourceReconciling(context.Context, store.RuntimeResourceEvidenceParams) error
	MarkRuntimeResourceAbsentVerified(context.Context, store.RuntimeResourceEvidenceParams) error
	MarkRuntimeResourceDestroyed(context.Context, store.RuntimeResourceRetireParams) error
	RenewRuntimeResourceWorkerLease(context.Context, store.RuntimeResourceWorkerLeaseRenewalParams) error
}

// eventStore covers the durable event log behind SSE/WebSocket replay.
type eventStore interface {
	GetEvent(context.Context, int64) (store.EventRecord, bool, error)
	ListEvents(context.Context, store.ListEventsParams) ([]store.EventRecord, error)
	OldestEventID(context.Context, string) (int64, bool, error)
	PruneEvents(context.Context, store.PruneEventsParams) (int64, error)
}

// notificationStore covers user-facing notifications.
type notificationStore interface {
	CreateNotification(context.Context, store.Notification) error
	ListNotificationsForUser(context.Context, string) ([]store.NotificationWithReadState, error)
	MarkNotificationRead(context.Context, string, string, time.Time) error
}

// contractVolumeStore covers sandbox contracts, artifact listings, the data
// volumes backing a session workspace/driver home, and proxy request accounting.
type contractVolumeStore interface {
	workspace.MutationRepository
	localworkspace.VolumeRecords
	StoreSandboxContract(context.Context, store.StoreSandboxContractParams) (store.SandboxContractRecord, error)
	RecordSandboxContractArtifacts(context.Context, store.RecordSandboxContractArtifactsParams) (store.SandboxContractArtifacts, error)
	ListArtifacts(context.Context, string) ([]store.Artifact, error)
	AppendEventsBatch(context.Context, []store.AppendEventParams) ([]store.AppendEventsBatchResult, error)
	StartProxyRequest(context.Context, store.StartProxyRequestParams) (store.StartProxyRequestResult, error)
	// Data-plane authorization is a pure decision — no events, no counters, and
	// deliberately no next_request_sequence bump. See store.AuthorizeDataProxyRequest.
	AuthorizeDataProxyRequest(context.Context, store.AuthorizeDataProxyRequestParams) (store.DataProxyAuthorization, error)
	DataProxyDenialReason(context.Context, string) string
	FinishProxyRequest(context.Context, store.FinishProxyRequestParams) (store.FinishProxyRequestResult, error)
}

// shareStore covers session_shares / share_links / share_link_files: grants,
// public link tokens, the owner-facing listings that manage them, and the
// revocation sweep `user rm` depends on.
//
// Revokes and the token rotate are scoped by (sessionID, id) rather than id
// alone: the route layer's owner gate is not the only thing standing between a
// guessed id and a cross-session write.
type shareStore interface {
	CreateSessionShare(context.Context, store.SessionShare) error
	ListSessionShares(context.Context, string) ([]store.SessionShare, error)
	ListSessionsSharedWith(context.Context, string) ([]store.SharedSession, error)
	ResolveSessionShare(context.Context, string, string) (store.SessionShare, bool, error)
	RevokeSessionShare(context.Context, store.RevokeShareParams) error
	RevokeSharesForUser(context.Context, string, time.Time) (int64, error)
	CreateShareLink(context.Context, store.ShareLink, []store.ShareLinkFile) error
	LookupShareLink(context.Context, string, time.Time) (store.ShareLink, bool, error)
	ListShareLinkFiles(context.Context, string) ([]store.ShareLinkFile, error)
	ListShareLinks(context.Context, string) ([]store.ShareLink, error)
	RotateShareLinkToken(context.Context, store.RevokeShareParams, string) error
	RevokeShareLink(context.Context, store.RevokeShareParams) error
	TouchShareLinkAccess(context.Context, []store.ShareLinkAccess) error
	SweepExpiredShareLinks(context.Context, time.Time) ([]string, error)
}

// serverStore is the union of the roles the Server needs. It embeds bridge.Store
// (via bridgeStore) so the bridge handoff keeps compiling off the same value.
type serverStore interface {
	PendingBridgeTurnRecords(context.Context, string, string, int64, int) ([]store.BridgeRecord, error)
	PendingBridgeRecords(context.Context, int) ([]store.BridgeRecord, error)
	LoadBridgeProjection(context.Context, store.BridgeRecord) (store.BridgeProjection, error)
	CommitBridgeProjection(context.Context, store.BridgeRecord, int64, json.RawMessage, []store.ProjectedMessage) ([]store.ProjectedMessageResult, error)
	PrepareBridgeCompletion(context.Context, store.CompleteTurnParams) error

	sessionStore
	scheduleStore
	messageStore
	authStore
	platformStore
	generationStore
	runtimeResourceStore
	eventStore
	notificationStore
	contractVolumeStore
	shareStore
	bridgeStore
}
