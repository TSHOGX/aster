package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrBusy           = errors.New("generation lifecycle is busy")
	ErrResumeConflict = errors.New("retained generation changed")
)

type GenerationState string

const (
	GenerationUsable        GenerationState = "usable"
	GenerationFailed        GenerationState = "failed"
	GenerationRetained      GenerationState = "retained"
	GenerationTransitioning GenerationState = "transitioning"
)

type Session struct{ ID, Driver, ActiveGenerationID string }
type DriverState struct {
	DriverID, Digest string
	Version          int
}
type Generation struct {
	ID, Owner   string
	DriverState DriverState
}
type EnsuredGeneration struct {
	Generation  Generation
	New, Resume bool
}

// GenerationRepository exposes application decisions and compare-and-swap
// reservations. Provider configuration and infrastructure proofs stay behind
// the repository/backend adapters; they are not lifecycle service vocabulary.
type GenerationRepository interface {
	Session(context.Context, string) (Session, error)
	State(context.Context, string, string) (GenerationState, error)
	Reserve(context.Context, Session, string, string) (Generation, error)
	Resume(context.Context, Session, string) (Generation, error)
}

type Service struct{ Generations GenerationRepository }

// EnsureGeneration is shared by interactive and scheduled work. A conflicting
// retained-generation claim gets one fresh observation, never an unbounded
// retry that could keep reallocating under another lifecycle owner.
func (s Service) EnsureGeneration(ctx context.Context, session Session, owner string) (EnsuredGeneration, error) {
	if s.Generations == nil || session.ID == "" || owner == "" {
		return EnsuredGeneration{}, fmt.Errorf("generation repository, session and owner required")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return EnsuredGeneration{}, err
		}
		session.ActiveGenerationID = strings.TrimSpace(session.ActiveGenerationID)
		active := session.ActiveGenerationID
		if active == "" {
			generation, err := s.Generations.Reserve(ctx, session, owner, "")
			return EnsuredGeneration{Generation: generation, New: true}, err
		}
		state, err := s.Generations.State(ctx, session.ID, active)
		if err != nil {
			return EnsuredGeneration{}, err
		}
		switch state {
		case GenerationRetained:
			generation, err := s.Generations.Resume(ctx, session, owner)
			if errors.Is(err, ErrResumeConflict) && attempt == 0 {
				sessionID := session.ID
				session, err = s.Generations.Session(ctx, sessionID)
				if err == nil && session.ID != sessionID {
					return EnsuredGeneration{}, fmt.Errorf("generation refresh changed session identity")
				}
				if err != nil {
					return EnsuredGeneration{}, err
				}
				continue
			}
			return EnsuredGeneration{Generation: generation, Resume: true}, err
		case GenerationTransitioning:
			return EnsuredGeneration{}, ErrBusy
		case GenerationUsable:
			return EnsuredGeneration{Generation: Generation{ID: active, Owner: owner}}, nil
		case GenerationFailed:
			generation, err := s.Generations.Reserve(ctx, session, owner, active)
			return EnsuredGeneration{Generation: generation, New: true}, err
		default:
			return EnsuredGeneration{}, fmt.Errorf("unknown application generation state %q", state)
		}
	}
	return EnsuredGeneration{}, ErrResumeConflict
}
