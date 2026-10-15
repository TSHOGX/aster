package httpapi

import (
	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"aster/evaluation-space/internal/projections"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// BrowserProfileAPI is the group-scoped evaluator profile editor boundary.
// Profiles are immutable catalog entries: editing always publishes the next
// revision and never rewrites evidence referenced by an existing run.
func BrowserProfileAPI(client *appidentity.Client, store *projections.Store) func(appidentity.Session, http.ResponseWriter, *http.Request) {
	mux := http.NewServeMux()
	authorize := func(r *http.Request, session appidentity.Session) error {
		result, err := client.EvaluationGroups(r.Context(), session, appidentity.EvaluationAccessRequest{GroupID: r.PathValue("group"), Action: "config.manage", Limit: 1})
		if err != nil {
			return err
		}
		if len(result.Items) != 1 || result.Items[0].ID != r.PathValue("group") {
			return appidentity.ErrUnauthorized
		}
		return nil
	}
	list := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session); err != nil {
			workflowAuthError(w, err)
			return
		}
		afterRevision, err := strconv.ParseInt(r.URL.Query().Get("after_revision"), 10, 64)
		if r.URL.Query().Get("after_revision") == "" {
			afterRevision = 0
		}
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_cursor"})
			return
		}
		page, err := scopedProfiles(r.Context(), store, r.PathValue("group"), r.URL.Query().Get("after_id"), afterRevision, 50)
		if err != nil {
			write(w, 422, map[string]string{"error": "profiles_unavailable"})
			return
		}
		write(w, 200, page)
	}
	read := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session); err != nil {
			workflowAuthError(w, err)
			return
		}
		ref, err := profileRef(r)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_profile_reference"})
			return
		}
		raw, err := store.CatalogDefinition(r.Context(), "profile", ref)
		if errors.Is(err, projections.ErrNotFound) {
			write(w, 404, map[string]string{"error": "profile_not_found"})
			return
		}
		if err != nil {
			write(w, 409, map[string]string{"error": "profile_identity_conflict"})
			return
		}
		profile, err := evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(raw))
		if err != nil || profile.Dataset.GroupID != r.PathValue("group") {
			write(w, 403, map[string]string{"error": "profile_scope_denied"})
			return
		}
		write(w, 200, profile)
	}
	publish := func(w http.ResponseWriter, r *http.Request) {
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		if err := authorize(r, session); err != nil {
			workflowAuthError(w, err)
			return
		}
		profile, err := evolution.Decode[evolution.EvaluationProfile](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		if err != nil || profile.Dataset.GroupID != r.PathValue("group") {
			write(w, 400, map[string]string{"error": "invalid_profile"})
			return
		}
		body, marshalErr := json.Marshal(profile)
		if marshalErr != nil {
			write(w, 422, map[string]string{"error": "profile_rejected"})
			return
		}
		if err = store.PublishDefinition(r.Context(), "profile", body); errors.Is(err, projections.ErrConflict) {
			write(w, 409, map[string]string{"error": "profile_revision_conflict"})
			return
		} else if err != nil {
			write(w, 422, map[string]string{"error": "profile_rejected", "detail": err.Error()})
			return
		}
		write(w, 201, profile.Ref)
	}
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/profiles", list)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}/profiles/{id}/{revision}", read)
	mux.HandleFunc("POST /evaluation-space/api/v1/groups/{group}/profiles", publish)
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, contextWithBrowserSession(r, session))
	}
}

func scopedProfiles(ctx context.Context, store *projections.Store, group, after string, afterRevision int64, limit int) (projections.CatalogPage, error) {
	result := projections.CatalogPage{Revisions: []evolution.Revision{}, NextID: after}
	cursorID, cursorRevision := after, afterRevision
	for len(result.Revisions) < limit {
		page, err := store.CatalogRevisions(ctx, projections.CatalogListInput{Kind: "profile", AfterID: cursorID, AfterRevision: cursorRevision, Limit: 100})
		if err != nil {
			return result, err
		}
		for index, ref := range page.Revisions {
			raw, err := store.CatalogDefinition(ctx, "profile", ref)
			if err != nil {
				return result, err
			}
			profile, err := evolution.Decode[evolution.EvaluationProfile](bytes.NewReader(raw))
			if err != nil {
				return result, err
			}
			if profile.Dataset.GroupID == group {
				result.Revisions = append(result.Revisions, ref)
				result.NextID, result.NextRevision = ref.ID, ref.Revision
				if len(result.Revisions) == limit {
					result.HasMore = page.HasMore || index+1 < len(page.Revisions)
					return result, nil
				}
			}
			cursorID, cursorRevision = ref.ID, ref.Revision
		}
		if !page.HasMore {
			break
		}
	}
	return result, nil
}

func profileRef(r *http.Request) (evolution.Revision, error) {
	var ref evolution.Revision
	n, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil {
		return ref, err
	}
	ref.Revision = n
	ref.ID, ref.Digest = r.PathValue("id"), r.URL.Query().Get("digest")
	return ref, ref.Validate()
}
