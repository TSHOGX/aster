package httpapi

import (
	"net/http"
	"strconv"
	"volume-studio/backend/internal/authoring"
)

func workbenchQuery(r *http.Request) authoring.Query {
	q := r.URL.Query()
	limit := 0
	if q.Has("limit") {
		var err error
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			limit = -1
		}
	}
	return authoring.Query{Profile: q.Get("profile"), Q: q.Get("q"), Status: q.Get("status"), Changes: q.Get("changes"), Sort: q.Get("sort"), Direction: q.Get("direction"), Parent: q.Get("parent"), Cursor: q.Get("cursor"), Limit: limit, Revision: revision(r), BaseVersion: q.Get("base_version")}
}
func (a *AuthoringAPI) search(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.WorkbenchTree(r.Context(), r.PathValue("library"), r.PathValue("version"), workbenchQuery(r), true)
	respond(w, 200, p, err)
}
func (a *AuthoringAPI) entry(w http.ResponseWriter, r *http.Request) {
	p, err := a.Store.Entry(r.Context(), r.PathValue("library"), r.URL.Query().Get("path"))
	respond(w, 200, p, err)
}
