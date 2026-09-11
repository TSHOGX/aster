package server

// HTTP surface for per-entry KB review.
//
// These endpoints exist here rather than in the Next.js app because the KB root
// is root-owned and the frontend runs unprivileged: it can read the snapshots but
// cannot write one. Just as importantly, the aggregation has to demote the
// rejected memory rows in the same operation, and the memory DB is the
// orchestrator's — a rejection that only edits snapshot bytes is undone by the
// next nightly cycle.
//
// The routes sit behind requireAuth like the rest of /api/. There is no separate
// reviewer role in this deployment (auth-users.yaml has no roles), so any logged
// in user can review; the actor is recorded in approvals.log either way.

import (
	"context"
	"net/http"
	"strings"

	"harness-platform/orchestrator/internal/memory"
)

// kbReviewStore is the slice of the memory store this surface needs.
type kbReviewStore interface {
	DeprecateSemanticMemories(ctx context.Context, ids []int64) (int, error)
}

// SetKBReview installs the KB writer and memory store the review endpoints use.
// Injected rather than built in New so the server package keeps working when the
// KB is disabled (nil kb ⇒ the endpoints report 503).
func (s *Server) SetKBReview(kb *memory.KB, store kbReviewStore) {
	s.kb = kb
	s.kbMemories = store
}

func (s *Server) kbReviewRoute(w http.ResponseWriter, r *http.Request) {
	if s.kb == nil {
		writeError(w, http.StatusServiceUnavailable, "knowledge base is disabled on this deployment")
		return
	}
	switch {
	case r.URL.Path == "/api/kb-review" && r.Method == http.MethodGet:
		s.getKBReview(w, r)
	case r.URL.Path == "/api/kb-review/decisions" && r.Method == http.MethodPost:
		s.postKBReviewDecisions(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// getKBReview returns the staged snapshot decomposed into per-entry decisions.
func (s *Server) getKBReview(w http.ResponseWriter, r *http.Request) {
	review, err := s.kb.Review()
	if err != nil {
		s.log.Error("kb review failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the knowledge base snapshots")
		return
	}
	if review == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"pending": nil,
			"current": s.kb.CurrentDigest(),
			"files":   []any{},
		})
		return
	}
	writeJSON(w, http.StatusOK, review)
}

// postKBReviewDecisions re-aggregates the approved entries into a new snapshot
// and demotes the rejected facts so the next cycle does not resurrect them.
func (s *Server) postKBReviewDecisions(w http.ResponseWriter, r *http.Request) {
	var body memory.ReviewDecisions
	if !decodeJSONBody(w, r, maxControlBodyBytes, &body) {
		return
	}
	body.Actor = userIDFromContext(r.Context())

	result, err := s.kb.ApplyReview(body)
	if err != nil {
		// A digest mismatch means the reviewer's page is stale — that is a client
		// error worth distinguishing, since retrying the same payload cannot help.
		status := http.StatusInternalServerError
		message := "could not apply the review decisions"
		if strings.Contains(err.Error(), "not the pending snapshot") || strings.Contains(err.Error(), "nothing pending") {
			status, message = http.StatusConflict, err.Error()
		}
		s.log.Error("kb review apply failed", "error", err, "actor", body.Actor)
		writeError(w, status, message)
		return
	}

	// Demote the rejected rows. A failure here is reported but does NOT fail the
	// request: the snapshot is already re-staged correctly, and telling the
	// reviewer their approval failed would invite a retry that re-approves an
	// already-applied set. The warning names the ids so it can be done by hand.
	deprecated := 0
	if len(result.RejectedFactIDs) > 0 && s.kbMemories != nil {
		n, demoteErr := s.kbMemories.DeprecateSemanticMemories(r.Context(), result.RejectedFactIDs)
		if demoteErr != nil {
			s.log.Error("kb review: could not demote rejected memories — they will return next cycle",
				"error", demoteErr, "ids", result.RejectedFactIDs)
		}
		deprecated = n
	}

	s.log.Info("kb review applied", "actor", body.Actor, "digest", result.Digest,
		"approved_facts", result.ApprovedFacts, "rejected_facts", result.RejectedFacts,
		"approved_files", result.ApprovedFiles, "rejected_files", result.RejectedFiles,
		"published", result.Published, "deprecated_rows", deprecated)

	writeJSON(w, http.StatusOK, map[string]any{
		"digest":           result.Digest,
		"staged":           result.Staged,
		"published":        result.Published,
		"approved_facts":   result.ApprovedFacts,
		"rejected_facts":   result.RejectedFacts,
		"approved_files":   result.ApprovedFiles,
		"rejected_files":   result.RejectedFiles,
		"deprecated_rows":  deprecated,
		"current":          s.kb.CurrentDigest(),
		"rejected_durable": deprecated == len(result.RejectedFactIDs),
	})
}
