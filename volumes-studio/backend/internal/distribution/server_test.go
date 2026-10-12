package distribution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"volume-studio/backend/internal/authoring"
)

func TestAuthenticatedDistributionManifestRangeAndRevocation(t *testing.T) {
	ctx := context.Background()
	store, err := authoring.Open(ctx, authoring.Config{Directory: t.TempDir(), PublisherID: "publisher", QuotaBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := authoring.Actor{Issuer: "https://example.com", Subject: "author"}
	l, err := store.Create(ctx, actor, authoring.CreateInput{RequestID: "create", Name: "Library"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Mutate(ctx, actor, l.ID, authoring.Mutation{RequestID: "text", ExpectedRevision: 1, Action: "text", Path: "file", Text: "source bytes"}); err != nil {
		t.Fatal(err)
	}
	v, err := store.Freeze(ctx, actor, l.ID, authoring.PublishInput{RequestID: "publish", ExpectedRevision: 2, Label: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ValidateNext(ctx, func(context.Context, authoring.Actor) error { return nil }); err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("credential", 4)
	t.Setenv("DISTRIBUTION_TEST_SECRET", secret)
	h, err := New(store, Config{Clients: []Client{{ID: "consumer", SecretEnvs: []string{"DISTRIBUTION_TEST_SECRET"}, GrantRevision: 1, Libraries: []string{l.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, rangeHeader, credential string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://internal/distribution/v1"+path, bytes.NewReader(raw))
		r.SetBasicAuth("consumer", credential)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Range", rangeHeader)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := call("GET", "/capabilities", nil, "", "wrong"); got.Code != 401 {
		t.Fatal("missing authority", got.Code)
	}
	manifest := call("GET", "/versions/"+v.ID+"/manifest", nil, "", secret)
	if manifest.Code != 200 || manifest.Header().Get("ETag") != `"`+v.DescriptorDigest+`"` {
		t.Fatal("descriptor", manifest.Code)
	}
	response := call("POST", "/exports", authoring.ExportInput{RequestID: "export", VersionID: v.ID, DescriptorDigest: v.DescriptorDigest}, "", secret)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var e authoring.Export
	if err = json.Unmarshal(response.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("source bytes"))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	path := "/exports/" + e.ID + "/blobs/" + digest
	if got := call("GET", path, nil, "bytes=2-5", secret); got.Code != 206 || got.Body.String() != "urce" || got.Header().Get("Content-Range") != "bytes 2-5/12" {
		t.Fatal(got.Code, got.Body.String(), got.Header())
	}
	for _, r := range []string{"bytes=0-1,3-4", "bytes=-2", "bytes=4-", "bytes=0-100"} {
		if got := call("GET", path, nil, r, secret); got.Code != 416 {
			t.Fatal("invalid range accepted", r, got.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://internal/distribution/v1"+path, nil)
	r.SetBasicAuth("consumer", secret)
	r.Header.Set("Cookie", "project_login=ambient")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("browser cookie admitted")
	}
	t.Setenv("DISTRIBUTION_TEST_SECRET", strings.Repeat("rotated", 6))
	if got := call("GET", path, nil, "bytes=0-1", secret); got.Code != 401 {
		t.Fatal("revoked service credential accepted")
	}
}
func TestRangeBoundaries(t *testing.T) {
	for _, c := range []struct {
		value string
		size  int64
		valid bool
	}{{"", 0, true}, {"", authoring.MaxChunkBytes, true}, {"", authoring.MaxChunkBytes + 1, false}, {"bytes=0-8388607", authoring.MaxChunkBytes + 1, true}, {"bytes=0-8388608", authoring.MaxChunkBytes + 1, false}, {"bytes=9223372036854775807-9223372036854775807", 1, false}} {
		_, _, _, err := byteRange(c.value, c.size)
		if (err == nil) != c.valid {
			t.Fatal(c, err)
		}
	}
}
