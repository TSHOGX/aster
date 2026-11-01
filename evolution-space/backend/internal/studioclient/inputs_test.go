package studioclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
)

func TestFileRejectsSameLengthSubstitution(t *testing.T) {
	pinned := []byte("reviewed-candidate")
	swapped := []byte("tampered-candidat!")
	if len(pinned) != len(swapped) {
		t.Fatalf("fixture lengths differ: %d %d", len(pinned), len(swapped))
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(pinned))
	entry := manifest.Entry{Path: "candidate.txt", Kind: "file", Size: strconv.Itoa(len(pinned)), Digest: digest}
	secret := strings.Repeat("s", 32)
	body := swapped
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "evolution-space" || password != secret {
			t.Error("incorrect machine identity")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("X-Content-Digest", digest)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	client, err := New(server.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	read := func() ([]byte, error) {
		t.Helper()
		stream, err := client.File(context.Background(), evolution.EvaluationGrant{}, "candidate", entry)
		if err != nil {
			return nil, err
		}
		defer stream.Close()
		return io.ReadAll(stream)
	}
	if _, err = read(); err == nil {
		t.Fatal("same-length substitution was accepted")
	}
	body = pinned
	got, err := read()
	if err != nil || !bytes.Equal(got, pinned) {
		t.Fatalf("pinned file rejected: %q %v", got, err)
	}
}
