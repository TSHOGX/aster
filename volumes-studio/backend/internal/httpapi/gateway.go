package httpapi

import (
	"aster/contracts/appidentity"
	"encoding/json"
	"net/http"
	"volume-studio/backend/internal/identity"
)

const apiPrefix = "/volumes-studio/api/v1/"

type Gateway struct {
	Sessions     *identity.Sessions
	PublicOrigin string
	Frontend     string
	API          func(identity.Session, http.ResponseWriter, *http.Request)
}

func (g Gateway) Handler() (http.Handler, error) {
	var sessions *appidentity.Sessions
	if g.Sessions != nil {
		sessions = g.Sessions.Sessions
	}
	project := func(s appidentity.Session) identity.Session {
		return identity.Session{Session: s, CanCreateLibrary: s.HasEntitlement(identity.CreateEntitlement)}
	}
	return (appidentity.Gateway{ApplicationID: "studio", Sessions: sessions, PublicOrigin: g.PublicOrigin, Frontend: g.Frontend, SessionView: func(s appidentity.Session) any { return project(s) }, API: func(s appidentity.Session, w http.ResponseWriter, r *http.Request) {
		if g.API != nil {
			g.API(project(s), w, r)
		} else {
			http.NotFound(w, r)
		}
	}}).Handler()
}
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
