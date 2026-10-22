package httpapi

import (
	"aster/contracts/appidentity"
	"aster/evolution-space/internal/projections"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type browserSessionKey struct{}

// BrowserAPI exposes explicitly selected human APIs. Private participant handlers
// are never mounted here, and the grant is carried only in server-owned context.
func BrowserAPI(client *appidentity.Client, store *projections.Store) func(appidentity.Session, http.ResponseWriter, *http.Request) {
	return BrowserAPIWith(client, store, BrowserOptions{})
}

// BrowserAPIWith adds operator registry facts, such as registered models.
func BrowserAPIWith(client *appidentity.Client, store *projections.Store, options BrowserOptions) func(appidentity.Session, http.ResponseWriter, *http.Request) {
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
		input := appidentity.EvolutionSpaceAccessRequest{GroupID: group, Action: "config.read", Search: query.Get("search"), After: query.Get("after"), Limit: limit}
		if !input.ValidScope() {
			write(w, 400, map[string]string{"error": "invalid_query"})
			return
		}
		session := r.Context().Value(browserSessionKey{}).(appidentity.Session)
		result, err := client.EvolutionSpaceGroups(r.Context(), session, input)
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
			Items     []appidentity.EvolutionSpaceGroup `json:"items"`
			NextAfter string                            `json:"next_after"`
			HasMore   bool                              `json:"has_more"`
		}{result.Items, result.NextAfter, result.HasMore})
	}
	mux.HandleFunc("GET /evolution-space/api/v1/groups", groups)
	mux.HandleFunc("GET /evolution-space/api/v1/groups/{group}", groups)
	workflow := BrowserWorkflowAPI(client, store)
	runs := BrowserRunAPI(client, store)
	evaluations := BrowserEvaluationAPI(client, store)
	profiles := BrowserProfileAPI(client, store)
	templates := BrowserTemplateAPI(client, store)
	catalogExamples := BrowserExampleAPI(client, store, options)
	modelConnections := BrowserModelConnectionAPI(options)
	return func(session appidentity.Session, w http.ResponseWriter, r *http.Request) {
		request := r.WithContext(context.WithValue(r.Context(), browserSessionKey{}, session))
		if r.URL.Path == "/evolution-space/api/v1/model-connections" || strings.HasPrefix(r.URL.Path, "/evolution-space/api/v1/model-connections/") {
			modelConnections(session, w, r)
			return
		}
		// Dispatch on the resource segment right after the group ID, so group
		// or resource IDs that look like resource names cannot misroute.
		switch groupResource(r.URL.Path) {
		case "workflows":
			workflow(session, w, r)
			return
		case "runs":
			runs(session, w, r)
			return
		case "evaluations":
			evaluations(session, w, r)
			return
		case "profiles":
			profiles(session, w, r)
			return
		case "templates", "published-workflows":
			templates(session, w, r)
			return
		case "examples", "models", "datasets":
			catalogExamples(session, w, r)
			return
		}
		mux.ServeHTTP(w, request)
	}
}

// groupResource returns the path segment after /groups/{group}/, or "".
func groupResource(path string) string {
	rest, ok := strings.CutPrefix(path, "/evolution-space/api/v1/groups/")
	if !ok {
		return ""
	}
	_, rest, ok = strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	resource, _, _ := strings.Cut(rest, "/")
	return resource
}
