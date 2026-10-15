package producthttp

import (
	"aster/contracts/appidentity"
	"aster/contracts/evolution"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"harness-platform/orchestrator/internal/identityconfig"
	"harness-platform/orchestrator/internal/product"
)

func (a *Authentication) identityRoutes(mux *http.ServeMux) {
	if a.Config.Identity == nil {
		return
	}
	mux.HandleFunc("GET /api/identity/v1/authorize", a.identityAuthorize)
}

// IdentityServiceHandler is mounted only on the separately configured private
// listener. The public application edge exposes only authorize, never services.
func (a *Authentication) IdentityServiceHandler() http.Handler {
	mux := http.NewServeMux()
	if a.Config.Identity == nil {
		return mux
	}
	mux.HandleFunc("POST /api/identity/v1/token", a.identityToken)
	mux.HandleFunc("POST /api/identity/v1/introspect", a.identityIntrospect)
	mux.HandleFunc("POST /api/identity/v1/revoke", a.identityRevoke)
	mux.HandleFunc("POST /api/identity/v1/studio-access", a.identityStudioAccess)
	mux.HandleFunc("POST /api/identity/v1/evaluation-space-access", a.identityEvaluationSpaceAccess)
	mux.HandleFunc("POST /api/identity/v1/evaluation-space-workflow-access", a.identityEvaluationSpaceWorkflowAccess)
	mux.HandleFunc("POST /api/identity/v1/projection-authority", a.identityProjectionAuthority)
	mux.HandleFunc("POST /api/identity/v1/catalog-authority", a.identityCatalogAuthority)
	mux.HandleFunc("POST /api/identity/v1/studio-output-reader", a.identityOutputReader)
	mux.HandleFunc("POST /api/identity/v1/evaluation-authorizations", a.identityEvaluationAdmission)
	mux.HandleFunc("POST /api/identity/v1/generation/{action}", a.identityGeneration)
	mux.HandleFunc("POST /api/identity/v1/evaluation-authority", a.identityEvaluationAuthority)
	mux.HandleFunc("POST /api/identity/v1/evaluation-settlements", a.identityEvaluationSettlement)
	mux.HandleFunc("POST /api/identity/v1/evaluation-profile-authority", a.identityEvaluationProfile)
	mux.HandleFunc("POST /api/identity/v1/run-admission-authority", a.identityRunAdmissionAuthority)
	mux.HandleFunc("POST /api/identity/v1/studio-participant-authority", a.identityStudioParticipantAuthority)
	return mux
}
func (a *Authentication) identityEvaluationSpaceAccess(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "evaluation-space" {
		writeError(w, 403, "evaluation service required")
		return
	}
	var input appidentity.EvaluationAccessRequest
	if !decode(w, r, 4096, &input) {
		return
	}
	result, err := a.Store.EvaluationSpaceAccess(r.Context(), input)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (a *Authentication) identityEvaluationSpaceWorkflowAccess(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "evaluation-space" {
		writeError(w, 403, "evaluation service required")
		return
	}
	var input appidentity.EvaluationWorkflowAccessRequest
	if !decode(w, r, 4096, &input) {
		return
	}
	result, err := a.Store.EvaluationSpaceWorkflowAccess(r.Context(), input)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func identityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func (a *Authentication) identityAuthorize(w http.ResponseWriter, r *http.Request) {
	identityHeaders(w)
	// The only redirectable callback comes from reviewed client registration. No
	// error redirects to an unvalidated request destination.
	if len(r.URL.RawQuery) > 4096 {
		writeError(w, 400, "invalid authorization request")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "invalid authorization request")
		return
	}
	allowed := map[string]bool{"client_id": true, "redirect_uri": true, "state": true, "code_challenge": true, "code_challenge_method": true}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			writeError(w, 400, "invalid authorization request")
			return
		}
	}
	client, ok := a.Config.Identity.Client(q.Get("client_id"))
	state := q.Get("state")
	if !ok || q.Get("redirect_uri") != client.Callback || q.Get("code_challenge_method") != "S256" || len(state) < 32 || len(state) > 128 || strings.IndexFunc(state, unicode.IsControl) >= 0 {
		writeError(w, 400, "invalid registered authorization request")
		return
	}
	// A same-site GET is the registered code flow. A cross-site navigation must
	// not select or fix a session on behalf of an unrelated origin.
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, 403, "cross-site authorization refused")
		return
	}
	code, err := a.Store.AdmitIdentityCode(r.Context(), requestToken(r), client.ID, client.Callback, q.Get("code_challenge"))
	if errors.Is(err, product.ErrUnauthorized) {
		location := "/?return_to=" + url.QueryEscape("/api/identity/v1/authorize?"+q.Encode())
		http.Redirect(w, r, location, http.StatusSeeOther)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	callback, _ := url.Parse(client.Callback)
	callback.RawQuery = url.Values{"code": {code}, "state": {state}}.Encode()
	http.Redirect(w, r, callback.String(), http.StatusSeeOther)
}
func (a *Authentication) identityService(w http.ResponseWriter, r *http.Request) (identityconfig.Client, bool) {
	identityHeaders(w)
	// These endpoints never accept browser ambient credentials. Client secrets
	// travel only over private authenticated transport, independently of cookies.
	if r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, 403, "service client required")
		return identityconfig.Client{}, false
	}
	id, secret, ok := r.BasicAuth()
	client, registered := a.Config.Identity.Client(id)
	if !ok || !registered || !client.Authenticate(secret) {
		writeError(w, 401, "service client authentication failed")
		return identityconfig.Client{}, false
	}
	return client, true
}
func (a *Authentication) identityResponse(w http.ResponseWriter, assertion product.IdentityAssertion, grant string) {
	body := struct {
		Issuer string `json:"issuer"`
		product.IdentityAssertion
		Grant string `json:"grant,omitempty"`
	}{a.Config.Identity.Issuer, assertion, grant}
	writeJSON(w, 200, body)
}
func (a *Authentication) identityToken(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	var b struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
		Verifier    string `json:"code_verifier"`
	}
	if !decode(w, r, 4096, &b) {
		return
	}
	if b.RedirectURI != client.Callback {
		writeError(w, 401, "invalid code exchange")
		return
	}
	assertion, grant, err := a.Store.ExchangeIdentityCode(r.Context(), client.ID, b.RedirectURI, b.Code, b.Verifier)
	if err != nil {
		fail(w, err)
		return
	}
	a.identityResponse(w, assertion, grant)
}
func (a *Authentication) identityIntrospect(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	var b struct {
		Grant string `json:"grant"`
	}
	if !decode(w, r, 1024, &b) {
		return
	}
	assertion, err := a.Store.IntrospectIdentity(r.Context(), client.ID, b.Grant)
	if err != nil {
		fail(w, err)
		return
	}
	a.identityResponse(w, assertion, "")
}
func (a *Authentication) identityRevoke(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	var b struct {
		Grant string `json:"grant"`
	}
	if !decode(w, r, 1024, &b) {
		return
	}
	if err := a.Store.RevokeIdentity(r.Context(), client.ID, b.Grant); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *Authentication) identityStudioAccess(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.identityService(w, r); !ok {
		return
	}
	var request evolution.AccessRequest
	if !decode(w, r, 4096, &request) {
		return
	}
	if request.Principal.Issuer != a.Config.Identity.Issuer {
		writeError(w, 403, "issuer mismatch")
		return
	}
	decision, err := a.Store.StudioAccess(r.Context(), request)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, decision)
}

func (a *Authentication) identityProjectionAuthority(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "evaluation-space" {
		writeError(w, 403, "evaluation service required")
		return
	}
	var scope product.ProjectionScope
	if !decode(w, r, 4096, &scope) {
		return
	}
	allowed, err := a.Store.AuthorizeEvolutionProjection(r.Context(), scope)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"allowed": allowed})
}

func (a *Authentication) identityStudioParticipantAuthority(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "studio" {
		writeError(w, 403, "Studio service required")
		return
	}
	var input struct {
		Scope  product.ProjectionScope `json:"scope"`
		Action string                  `json:"action"`
	}
	if !decode(w, r, 4096, &input) {
		return
	}
	subject, err := a.Store.EvolutionParticipantActor(r.Context(), input.Scope, input.Action)
	if err != nil {
		adminFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"issuer": a.Config.Identity.Issuer, "subject": subject})
}

func (a *Authentication) identityRunAdmissionAuthority(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "evaluation-space" {
		writeError(w, 403, "evaluation service required")
		return
	}
	var scope product.EvolutionRunAdmissionScope
	if !decode(w, r, 4096, &scope) {
		return
	}
	allowed, err := a.Store.AuthorizeEvolutionRunAdmission(r.Context(), scope)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"allowed": allowed})
}

func (a *Authentication) identityCatalogAuthority(w http.ResponseWriter, r *http.Request) {
	client, ok := a.identityService(w, r)
	if !ok {
		return
	}
	if client.ID != "evaluation-space" {
		writeError(w, 403, "evaluation service required")
		return
	}
	var scope struct {
		Kind   string              `json:"kind"`
		Action string              `json:"action"`
		Ref    *evolution.Revision `json:"ref,omitempty"`
	}
	if !decode(w, r, 4096, &scope) {
		return
	}
	validKind := scope.Kind == "workflow" || scope.Kind == "service" || scope.Kind == "template" || scope.Kind == "profile"
	allowed := validKind && ((scope.Action == "list" && scope.Ref == nil) || (scope.Action == "read" && scope.Ref != nil && scope.Ref.Validate() == nil))
	writeJSON(w, 200, map[string]bool{"allowed": allowed})
}
