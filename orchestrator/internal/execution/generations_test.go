package execution

import (
	"context"
	"errors"
	"testing"
)

type generationRepository struct {
	state                        GenerationState
	refreshed                    Session
	resumeError                  error
	reserves, resumes, refreshes int
	previous                     string
}

func (r *generationRepository) Session(context.Context, string) (Session, error) {
	r.refreshes++
	return r.refreshed, nil
}
func (r *generationRepository) State(context.Context, string, string) (GenerationState, error) {
	return r.state, nil
}
func (r *generationRepository) Reserve(_ context.Context, _ Session, owner, previous string) (Generation, error) {
	r.reserves++
	r.previous = previous
	return Generation{ID: "new-generation", Owner: owner, DriverState: DriverState{DriverID: "pi", Digest: "native-history", Version: 3}}, nil
}
func (r *generationRepository) Resume(_ context.Context, session Session, owner string) (Generation, error) {
	r.resumes++
	return Generation{ID: session.ActiveGenerationID, Owner: owner}, r.resumeError
}

func TestEnsureGenerationPreservesReservationFenceAndDriverState(t *testing.T) {
	for _, active := range []string{"", "old-generation"} {
		t.Run(active, func(t *testing.T) {
			repository := &generationRepository{state: GenerationFailed}
			got, err := (Service{Generations: repository}).EnsureGeneration(context.Background(), Session{ID: "session", Driver: "pi", ActiveGenerationID: active}, "owner")
			if err != nil || !got.New || got.Resume || got.Generation.DriverState.Digest != "native-history" || repository.reserves != 1 || repository.previous != active {
				t.Fatalf("reservation lost application state/fence: %+v %+v %v", got, repository, err)
			}
		})
	}
}
func TestEnsureGenerationDoesNotReplaceUsableOrTransitioningExecution(t *testing.T) {
	for _, state := range []GenerationState{GenerationUsable, GenerationTransitioning} {
		t.Run(string(state), func(t *testing.T) {
			repository := &generationRepository{state: state}
			got, err := (Service{Generations: repository}).EnsureGeneration(context.Background(), Session{ID: "session", ActiveGenerationID: "existing"}, "owner")
			if state == GenerationUsable && (err != nil || got.Generation.ID != "existing" || got.New || got.Resume) {
				t.Fatalf("usable execution changed: %+v %v", got, err)
			}
			if state == GenerationTransitioning && !errors.Is(err, ErrBusy) {
				t.Fatalf("transition was reused: %v", err)
			}
			if repository.reserves != 0 || repository.resumes != 0 {
				t.Fatal("conflicting lifecycle side effect")
			}
		})
	}
}
func TestEnsureGenerationBoundsResumeConflictRecovery(t *testing.T) {
	repository := &generationRepository{state: GenerationRetained, resumeError: ErrResumeConflict, refreshed: Session{ID: "session", ActiveGenerationID: "new-retained"}}
	_, err := (Service{Generations: repository}).EnsureGeneration(context.Background(), Session{ID: "session", ActiveGenerationID: "old-retained"}, "owner")
	if !errors.Is(err, ErrResumeConflict) || repository.resumes != 2 || repository.refreshes != 1 || repository.reserves != 0 {
		t.Fatalf("unbounded/conflicting retry: %+v %v", repository, err)
	}
	repository = &generationRepository{state: GenerationRetained, resumeError: ErrResumeConflict, refreshed: Session{ID: "session", Driver: "pi"}}
	got, err := (Service{Generations: repository}).EnsureGeneration(context.Background(), Session{ID: "session", ActiveGenerationID: "old-retained"}, "owner")
	if err != nil || !got.New || repository.reserves != 1 || repository.resumes != 1 || repository.previous != "" {
		t.Fatalf("refresh did not govern new reservation: %+v %+v %v", got, repository, err)
	}
}
