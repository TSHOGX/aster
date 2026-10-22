package httpapi

import (
	"aster/contracts/evolution"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"volume-studio/backend/internal/authoring"
)

type participantAuthorityFixture struct{ scope EvolutionParticipantScope }

func (a participantAuthorityFixture) AuthorizeEvolutionParticipant(r *http.Request, s EvolutionParticipantScope, action string) (authoring.Actor, error) {
	user, secret, ok := r.BasicAuth()
	if !ok || user != "central" || secret != "test-service-secret" || s != a.scope {
		return authoring.Actor{}, ErrEvolutionParticipantDenied
	}
	return authoring.Actor{Issuer: "https://central", Subject: "manager"}, nil
}
func TestEvolutionParticipantServiceBoundary(t *testing.T) {
	s, err := authoring.Open(context.Background(), authoring.Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 64 << 20, Authority: &scopedTestAuthority{true}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	config := evolution.Configuration{GroupID: "group", Revision: 1, Budget: evolution.Budget{MaxTokens: 1, MaxAttempts: 1, TimeoutSeconds: 1, MaxArtifactBytes: 1}}
	raw, _ := json.Marshal(config)
	scope := EvolutionParticipantScope{"group", "operation", 1, evolution.Digest(raw)}
	api := EvolutionParticipantAPI{Store: s, Authority: participantAuthorityFixture{scope}}
	call := func(action string, body any, credential, cookie, origin string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/volumes-studio/internal/v1/evolution/"+action, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Cookie", cookie)
		r.Header.Set("Origin", origin)
		if credential != "" {
			r.SetBasicAuth("central", credential)
		}
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		return w
	}
	input := authoring.EvolutionReservationOperation{OperationID: scope.OperationID, Digest: scope.Digest, Configuration: config}
	for _, args := range [][3]string{{"", "", ""}, {"wrong", "", ""}, {"test-service-secret", "session=browser", ""}, {"test-service-secret", "", "https://browser"}} {
		if w := call("reserve", input, args[0], args[1], args[2]); w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, action := range []string{"reserve", "lookup", "adopt", "adopt"} {
		var body any = scope
		if action == "reserve" {
			body = input
		}
		w := call(action, body, "test-service-secret", "", "")
		if w.Code != 200 {
			t.Fatal(action, w.Code, w.Body.String())
		}
		var receipt evolution.ActivationReceipt
		if err = json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.OperationID != scope.OperationID || receipt.ConfigurationDigest != scope.Digest {
			t.Fatal(receipt)
		}
	}
	if w := call("cancel", scope, "test-service-secret", "", ""); w.Code != 409 {
		t.Fatal("adopted cancellation", w.Code, w.Body.String())
	}
	changed := scope
	changed.GroupID = "other"
	if w := call("lookup", changed, "test-service-secret", "", ""); w.Code != 403 {
		t.Fatal("scope substitution", w.Code)
	}
	api.Authority = nil
	if w := call("lookup", scope, "test-service-secret", "", ""); w.Code != 503 {
		t.Fatal("missing authority", w.Code)
	}
}
