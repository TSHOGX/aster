package httpapi

import (
	"aster/contracts/appidentity"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

type browserSessionKey struct{}

// BrowserAPI exposes explicitly selected human APIs. Private participant handlers
// are never mounted here, and the grant is carried only in server-owned context.
func BrowserAPI(client *appidentity.Client) func(appidentity.Session, http.ResponseWriter, *http.Request) {
	mux := http.NewServeMux()
	groups := func(w http.ResponseWriter, r *http.Request) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			write(w, 400, map[string]string{"error": "invalid_query"})
			return
		}
		for key, values := range query {
			if len(values) != 1 || (key != "search" && key != "after" && key != "limit") {
				write(w, 400, map[string]string{"error": "invalid_query"})
				return
			}
		}
		limit := 50
		if raw := query.Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 50 {
				write(w, 400, map[string]string{"error": "invalid_limit"})
				return
			}
		}
		group := r.PathValue("group")
		input := appidentity.EvaluationAccessRequest{GroupID: group, Action: "config.read", Search: query.Get("search"), After: query.Get("after"), Limit: limit}
		if !input.ValidScope() {
			write(w, 400, map[string]string{"error": "invalid_query"})
			return
		}
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		result, err := client.EvaluationGroups(r.Context(), session, input)
		if err != nil {
			if errors.Is(err, appidentity.ErrUnauthorized) {
				write(w, 403, map[string]string{"error": "group_access_denied"})
			} else {
				write(w, 503, map[string]string{"error": "group_authority_unavailable"})
			}
			return
		}
		// Subject and bounded authority are transport evidence, not browser credentials.
		write(w, 200, struct {
			Items     []appidentity.EvaluationGroup `json:"items"`
			NextAfter string                        `json:"next_after"`
			HasMore   bool                          `json:"has_more"`
		}{result.Items, result.NextAfter, result.HasMore})
	}
	mux.HandleFunc("GET /evaluation-space/api/v1/groups", groups)
	mux.HandleFunc("GET /evaluation-space/api/v1/groups/{group}", groups)
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), browserSessionKey{}, session)))
	}
}
