package httpapi

import (
	"aster/contracts/appidentity"
	"context"
	"net/http/httptest"
	"testing"
	"volume-studio/backend/internal/authoring"
	"volume-studio/backend/internal/identity"
)

func TestEventCursorRejectsMalformedValues(t *testing.T) {
	store, err := authoring.Open(context.Background(), authoring.Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 32 << 20, Authority: &scopedTestAuthority{true}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := NewAuthoringAPI(store)
	session := identity.Session{Session: appidentity.Session{Issuer: "https://example.com", Subject: "expert"}}
	for _, cursor := range []string{"invalid", "-1", "01", "%2B1", "9223372036854775808"} {
		request := httptest.NewRequest("GET", apiPrefix+"libraries/library/events?after="+cursor, nil)
		response := httptest.NewRecorder()
		api.Serve(session, response, request)
		if response.Code != 422 {
			t.Fatal(cursor, response.Code, response.Body.String())
		}
	}
}
