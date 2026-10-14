package httpapi

import (
	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"errors"
	"mime"
	"net/http"
	"volume-studio/backend/internal/authoring"
)

type EvolutionParticipantScope struct {
	GroupID     string `json:"group_id"`
	OperationID string `json:"operation_id"`
	Revision    int64  `json:"revision"`
	Digest      string `json:"digest"`
}

// Authority must authenticate the central service, check current exact operation
// scope and resolve its authorized actor. Request bodies never choose the actor.
type EvolutionParticipantAuthority interface {
	AuthorizeEvolutionParticipant(*http.Request, EvolutionParticipantScope, string) (authoring.Actor, error)
}

var ErrEvolutionParticipantDenied = errors.New("evolution_participant_denied")

type EvolutionParticipantAPI struct {
	Store     *authoring.Store
	Authority EvolutionParticipantAuthority
}

func (a EvolutionParticipantAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, action := range []string{"reserve", "lookup", "adopt", "cancel"} {
		mux.HandleFunc("POST /volumes-studio/internal/v1/evolution/"+action, func(w http.ResponseWriter, r *http.Request) { a.call(w, r, action) })
	}
	return mux
}
func (a EvolutionParticipantAPI) call(w http.ResponseWriter, r *http.Request, action string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		http.Error(w, "service identity required", 403)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "JSON required", 415)
		return
	}
	var scope EvolutionParticipantScope
	var input authoring.EvolutionReservationOperation
	if action == "reserve" {
		input, err = evolution.Decode[authoring.EvolutionReservationOperation](http.MaxBytesReader(w, r.Body, evolution.MaxDocumentBytes))
		scope = EvolutionParticipantScope{input.Configuration.GroupID, input.OperationID, input.Configuration.Revision, input.Digest}
	} else {
		scope, err = evolution.Decode[EvolutionParticipantScope](http.MaxBytesReader(w, r.Body, 1<<16))
	}
	if err != nil || !manifest.ValidID(scope.GroupID) || !manifest.ValidID(scope.OperationID) || scope.Revision < 1 || !manifest.ValidDigest(scope.Digest) {
		http.Error(w, "invalid operation scope", 400)
		return
	}
	if a.Authority == nil || a.Store == nil {
		http.Error(w, "participant unavailable", 503)
		return
	}
	actor, err := a.Authority.AuthorizeEvolutionParticipant(r, scope, action)
	if err != nil {
		if errors.Is(err, ErrEvolutionParticipantDenied) {
			http.Error(w, "operation denied", 403)
		} else {
			http.Error(w, "authority unavailable", 503)
		}
		return
	}
	if action == "adopt" || action == "cancel" {
		existing, lookupErr := a.Store.ScopedEvolutionReservation(r.Context(), actor, scope.GroupID, scope.OperationID, scope.Digest, scope.Revision)
		if lookupErr != nil {
			authoringError(w, lookupErr)
			return
		}
		if existing.Generation != scope.Revision {
			http.Error(w, "operation revision conflict", 409)
			return
		}
	}
	var receipt evolution.ActivationReceipt
	switch action {
	case "reserve":
		receipt, err = a.Store.ReserveEvolutionConfiguration(r.Context(), actor, input)
	case "lookup":
		receipt, err = a.Store.ScopedEvolutionReservation(r.Context(), actor, scope.GroupID, scope.OperationID, scope.Digest, scope.Revision)
	case "adopt":
		receipt, err = a.Store.AdoptEvolutionConfiguration(r.Context(), actor, scope.OperationID, scope.Digest)
	case "cancel":
		receipt, err = a.Store.CancelEvolutionReservation(r.Context(), actor, scope.OperationID, scope.Digest)
	}
	if err != nil {
		authoringError(w, err)
		return
	}
	if receipt.Generation != scope.Revision {
		http.Error(w, "operation revision conflict", 409)
		return
	}
	writeJSON(w, 200, receipt)
}
