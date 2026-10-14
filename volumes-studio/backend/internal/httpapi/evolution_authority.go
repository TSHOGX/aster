package httpapi

import (
	"aster/contracts/evolution"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/identity"
)

type EvolutionActorResolver interface {
	ResolveEvolutionParticipant(context.Context, string, string, int64, string, string) (evolution.Principal, error)
}
type centralEvolutionAuthority struct {
	resolver EvolutionActorResolver
	secret   [32]byte
}

func NewCentralEvolutionAuthority(resolver EvolutionActorResolver, secret string) (EvolutionParticipantAuthority, error) {
	if resolver == nil || len(secret) < 32 {
		return nil, fmt.Errorf("central participant credential and actor resolver required")
	}
	return &centralEvolutionAuthority{resolver: resolver, secret: sha256.Sum256([]byte(secret))}, nil
}
func (a *centralEvolutionAuthority) AuthorizeEvolutionParticipant(r *http.Request, scope EvolutionParticipantScope, action string) (authoring.Actor, error) {
	user, password, ok := r.BasicAuth()
	actual := sha256.Sum256([]byte(password))
	if !ok || user != "central" || subtle.ConstantTimeCompare(actual[:], a.secret[:]) != 1 {
		return authoring.Actor{}, ErrEvolutionParticipantDenied
	}
	principal, err := a.resolver.ResolveEvolutionParticipant(r.Context(), scope.GroupID, scope.OperationID, scope.Revision, scope.Digest, action)
	if errors.Is(err, identity.ErrUnauthorized) {
		return authoring.Actor{}, ErrEvolutionParticipantDenied
	}
	if err != nil {
		return authoring.Actor{}, err
	}
	if principal.Kind != "human" || principal.Issuer == "" || principal.Subject == "" {
		return authoring.Actor{}, identity.ErrUnavailable
	}
	return authoring.Actor{Issuer: principal.Issuer, Subject: principal.Subject}, nil
}
