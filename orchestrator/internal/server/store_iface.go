package server

import (
	"context"
	"encoding/json"
	"harness-platform/orchestrator/internal/execution"
	"time"

	"harness-platform/orchestrator/internal/store"
	"harness-platform/orchestrator/internal/workspace"
)

// These consumer-owned roles expose product persistence to HTTP and projection.
type sessionStore interface {
	CreateSession(context.Context, store.Session) error
	GetSession(context.Context, string) (store.Session, error)
	ListSessionsByUser(context.Context, string) ([]store.Session, error)
	UpdateSessionTitle(context.Context, string, string, bool) error
	SessionHasActiveTurn(context.Context, string) (bool, error)
	GetSessionRuntimeSnapshot(context.Context, string) (store.SessionRuntimeSnapshot, error)
	SetTurnPhase(context.Context, store.SetTurnPhaseParams) (store.SessionRuntimeSnapshot, error)
	CancelQueuedTurn(context.Context, store.CancelQueuedTurnParams) (store.SessionRuntimeSnapshot, error)
	// ReleaseParkedTurn hands one turn parked by Stop back to the worker.
	ReleaseParkedTurn(context.Context, store.ReleaseParkedTurnParams) (store.SessionRuntimeSnapshot, error)
	CountSandboxOccupyingSessions(context.Context) (int, error)
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
	PendingTurnContinuations(context.Context, string, time.Time, int) ([]store.TurnContinuation, error)
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
	ListFeatureFlags(context.Context) ([]store.FeatureFlag, error)
	GetFeatureFlag(context.Context, string) (store.FeatureFlag, bool, error)
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

// generationStore exposes the public execution availability projection.
type generationStore interface {
	SessionSandboxStates(context.Context) (map[string]store.SandboxState, error)
}

// eventStore covers the durable event log behind SSE replay.
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

// workspaceStore exposes artifact metadata and durable mutation outcomes.
type workspaceStore interface {
	workspace.MutationRepository
	ListArtifacts(context.Context, string) ([]store.Artifact, error)
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
	RequestBoundSteer(context.Context, string, string, string, int64, int64) (execution.TurnCommand, error)
	RequestBoundInterrupt(context.Context, string, string, string) (*execution.TurnCommand, store.SessionRuntimeSnapshot, error)
	DriverStateValidationInput(context.Context, store.CompleteTurnParams) (store.DriverStateValidationInput, error)
	PendingBridgeTurnRecords(context.Context, string, string, int64, int) ([]store.BridgeRecord, error)
	PendingBridgeRecords(context.Context, int) ([]store.BridgeRecord, error)
	RecordBridgeProjectionFailure(context.Context, store.BridgeRecord, string, time.Time) error
	PendingBridgeExecutionRecords(context.Context, string, int) ([]store.BridgeRecord, error)
	LoadBridgeProjection(context.Context, store.BridgeRecord) (store.BridgeProjection, error)
	CommitBridgeProjection(context.Context, store.BridgeRecord, int64, json.RawMessage, []store.ProjectedMessage) ([]store.ProjectedMessageResult, error)
	PrepareBridgeCompletion(context.Context, store.CompleteTurnParams) error

	sessionStore
	scheduleStore
	messageStore
	authStore
	platformStore
	generationStore
	eventStore
	notificationStore
	workspaceStore
	shareStore
	bridgeStore
}
