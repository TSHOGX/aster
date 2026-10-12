package producthttp

import (
	"errors"
	"harness-platform/orchestrator/internal/product"
	"net/http"
)

func settingsFailure(w http.ResponseWriter, err error) {
	code := "settings_invalid"
	var admission *product.AdmissionError
	if errors.As(err, &admission) {
		code = admission.Code
	}
	if errors.Is(err, product.ErrInvalid) {
		writeJSON(w, 422, map[string]string{"code": code, "error": "invalid account settings"})
		return
	}
	if errors.Is(err, product.ErrConflict) {
		writeJSON(w, 409, map[string]string{"code": "revision_conflict", "error": "settings changed; reload before reapplying your changes"})
		return
	}
	authFailure(w, err)
}
func (a *Authentication) preferences(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := a.Store.AccountPreferences(r.Context(), requestToken(r))
		if err != nil {
			settingsFailure(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	var body struct {
		ExpectedRevision *int64  `json:"expected_revision"`
		ThemeMode        *string `json:"theme_mode"`
		Accent           *string `json:"accent"`
	}
	if !decode(w, r, 4096, &body) {
		return
	}
	if body.ExpectedRevision == nil {
		settingsFailure(w, product.ErrInvalid)
		return
	}
	result, err := a.Store.UpdateAccountPreferences(r.Context(), requestToken(r), *body.ExpectedRevision, product.PreferenceChanges{ThemeMode: body.ThemeMode, Accent: body.Accent})
	if err != nil {
		settingsFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) profile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		result, err := a.Store.SelfProfile(r.Context(), requestToken(r))
		if err != nil {
			settingsFailure(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	var body struct {
		ExpectedRevision *int64 `json:"expected_revision"`
		DisplayName      string `json:"display_name"`
	}
	if !decode(w, r, 4096, &body) {
		return
	}
	if body.ExpectedRevision == nil {
		settingsFailure(w, product.ErrInvalid)
		return
	}
	result, err := a.Store.UpdateSelfProfile(r.Context(), requestToken(r), *body.ExpectedRevision, body.DisplayName)
	if err != nil {
		settingsFailure(w, err)
		return
	}
	writeJSON(w, 200, result)
}
