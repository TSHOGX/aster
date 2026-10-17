package evolutionspaceclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aster/contracts/evolution"
)

type authority struct{}

func (authority) AuthorizeEvaluation(context.Context, evolution.EvaluationAssignment) (evolution.EvaluationGrant, error) {
	return evolution.EvaluationGrant{}, nil
}
func TestEvaluationTransportAuthenticatesAndRejectsMismatchedResults(t *testing.T) {
	ref := evolution.Revision{ID: "definition", Revision: 1, Digest: evolution.Digest([]byte("definition"))}
	profile := evolution.EvaluationProfile{Ref: ref, Dataset: evolution.ArtifactRef{ID: "dataset", GroupID: "group", SchemaDigest: evolution.Digest([]byte("schema")), Digest: evolution.Digest([]byte("data")), GrantID: "grant", PolicyRevision: 1, MediaType: "application/json"}, Scorer: ref, Runtime: ref, Model: ref, Thresholds: []evolution.MetricThreshold{{Name: "metric", Unit: "ratio", Operator: "gte", Value: 1, MinimumSamples: 1}}}
	profile.Ref.Digest, _ = evolution.DefinitionDigest(profile)
	subject := evolution.EvaluationSubject{Candidate: evolution.ContentRef{LibraryID: "library", CommitID: strings.Repeat("a", 64), Profile: "authored-v1", TreeDigest: evolution.Digest([]byte("tree"))}, PreparedDigest: evolution.Digest([]byte("prepared")), RequirementDigest: evolution.Digest([]byte("requirement")), Profile: profile.Ref, Dataset: profile.Dataset, Scorer: ref, Runtime: ref, Model: ref}
	digest, _ := subject.Digest()
	grant := evolution.EvaluationGrant{ID: "grant", JobID: "job", GroupID: "group", AssignmentDigest: evolution.Digest([]byte("assignment")), SubjectDigest: digest, Service: ref}
	secret := strings.Repeat("s", 32)
	mode := "profile"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "studio" || password != secret || r.Header.Get("Cookie") != "" {
			t.Error("service identity missing")
			w.WriteHeader(403)
			return
		}
		switch mode {
		case "profile":
			_ = json.NewEncoder(w).Encode(profile)
		case "pending":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"evaluation_not_found"}`))
		case "gateway404":
			http.NotFound(w, r)
		case "denied":
			w.WriteHeader(403)
		case "redirect":
			http.Redirect(w, r, "https://forbidden.example", 302)
		case "verify":
			_, _ = w.Write([]byte(`{"allowed":true}`))
		case "duplicate":
			_, _ = w.Write([]byte(`{"allowed":true,"allowed":false}`))
		default:
			result := evolution.EvaluationResult{JobID: "job", Fence: 1, Service: ref, Subject: subject, State: "completed", Outcome: "pass"}
			if mode == "wrong" {
				result.Subject.Seed++
			}
			_ = json.NewEncoder(w).Encode(struct {
				Result evolution.EvaluationResult `json:"result"`
				Usage  struct {
					Tokens     int64 `json:"tokens"`
					CostMicros int64 `json:"cost_micros"`
				} `json:"usage"`
			}{Result: result})
		}
	}))
	defer server.Close()
	client, err := New(server.URL, secret, authority{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Profile(context.Background(), evolution.EvaluationProfileScope{GroupID: "group", Profile: profile.Ref})
	if err != nil || got.Ref != profile.Ref {
		t.Fatal(got, err)
	}
	mode = "result"
	if _, err = client.Result(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		mode string
		err  error
	}{{"pending", ErrPending}, {"gateway404", ErrUnavailable}, {"denied", ErrDenied}, {"redirect", ErrUnavailable}, {"wrong", ErrUnavailable}} {
		mode = test.mode
		if _, err = client.Result(context.Background(), grant); !errors.Is(err, test.err) {
			t.Fatal(mode, err)
		}
	}
	mode = "verify"
	if err = client.Verify(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	mode = "duplicate"
	if err = client.Verify(context.Background(), grant); err == nil {
		t.Fatal("duplicate decision accepted")
	}
}

func TestRunResultArtifactTransportChecksEnvelopeAndDeniesRedirect(t *testing.T) {
	secret := strings.Repeat("s", 32)
	data := []byte("result bytes")
	ref := evolution.ArtifactRef{ID: "file", GroupID: "group", Digest: evolution.Digest(data), Size: int64(len(data))}
	mode := "ok"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "studio" || password != secret || r.URL.Path != "/evolution-space/internal/v1/run-result-artifact" {
			t.Error("wrong result artifact identity")
		}
		switch mode {
		case "redirect":
			http.Redirect(w, r, "https://denied.example", 302)
			return
		case "denied":
			w.WriteHeader(403)
			return
		}
		w.Header().Set("X-Content-Digest", ref.Digest)
		if mode == "wrong-digest" {
			w.Header().Set("X-Content-Digest", evolution.Digest([]byte("wrong")))
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		_, _ = w.Write(data)
	}))
	defer server.Close()
	client, err := New(server.URL, secret, authority{})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := client.ReadRunResultArtifact(context.Background(), evolution.RunResultScope{}, ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(raw) != string(data) {
		t.Fatal(string(raw), err)
	}
	for _, value := range []string{"redirect", "denied", "wrong-digest"} {
		mode = value
		if reader, err = client.ReadRunResultArtifact(context.Background(), evolution.RunResultScope{}, ref); err == nil {
			reader.Close()
			t.Fatal("invalid artifact transport", mode)
		}
	}
}
