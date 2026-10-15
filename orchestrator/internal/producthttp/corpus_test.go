package producthttp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aster/contracts/evolution"
	"harness-platform/orchestrator/internal/product"
)

func TestCorpusRoutesRequireCurrentEvidenceAndOrigin(t *testing.T) {
	store, err := product.Open(ctx, filepath.Join(t.TempDir(), "central.db"))
	must(t, err)
	defer store.Close()
	admin, err := store.BootstrapAccount(ctx, "admin", "permanent-admin-password", identitySeed(t))
	must(t, err)
	_, token, err := store.Authenticate(ctx, admin.Username, "permanent-admin-password")
	must(t, err)
	_, err = store.CreateProject(ctx, admin.ID, "source-project", "Source")
	must(t, err)
	source := evolution.SourceBinding{ID: "source", Kind: "project", ResourceID: "source-project", PolicyRevision: 1, AudienceGroups: []string{product.StandardUsersGroup}, DerivativePublication: true}
	_, err = store.SetEvolutionSourcePolicy(ctx, token, product.StandardUsersGroup, product.EvolutionSourcePolicyInput{RequestID: "source", Source: source})
	must(t, err)
	handler := NewAuthentication(store, Config{}).Handler(http.NotFoundHandler())
	call := func(path, cookie, origin string, input any) int {
		raw, _ := json.Marshal(input)
		request := httptest.NewRequest("POST", "https://app"+path, bytes.NewReader(raw))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			request.AddCookie(&http.Cookie{Name: loginCookie, Value: cookie})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	path := "/api/admin/groups/" + product.StandardUsersGroup + "/corpus/capture"
	input := product.CorpusCaptureInput{RequestID: "capture", SourceID: "source", SourceRevision: 1, ExecutionID: "missing"}
	if code := call(path, "", "https://app", input); code != 401 {
		t.Fatal("anonymous capture", code)
	}
	if code := call(path, token, "https://foreign", input); code != 403 {
		t.Fatal("cross-origin capture", code)
	}
	if code := call(path, token, "https://app", input); code != 403 {
		t.Fatal("admin implicitly reads corpus", code)
	}
	_, err = store.SetEvolutionGroupGrant(ctx, token, product.StandardUsersGroup, product.EvolutionGroupGrantInput{RequestID: "source-read", AccountID: admin.ID, Action: "source.read", Effect: "allow"})
	must(t, err)
	if code := call(path, token, "https://app", input); code != 404 {
		t.Fatal("authorized missing episode", code)
	}
	if code := call("/api/admin/groups/"+product.StandardUsersGroup+"/corpus/snapshots", token, "https://app", product.CorpusSnapshotInput{RequestID: "seal", SourceID: "source", SourceRevision: 1, Limit: 10}); code != 404 {
		t.Fatal("empty source snapshot", code)
	}
}
