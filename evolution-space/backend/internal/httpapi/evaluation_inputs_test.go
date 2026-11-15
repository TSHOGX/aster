package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	manifest "aster/contracts/volumemanifest"
)

type failAtEOF struct {
	payload []byte
	sent    bool
}

func (f *failAtEOF) Read(p []byte) (int, error) {
	if f.sent {
		return 0, errors.New("Studio file does not match its pinned digest")
	}
	f.sent = true
	return copy(p, f.payload), nil
}

func TestPinnedInputIsWithheldWhenTheStreamFailsAfterAFullRead(t *testing.T) {
	payload := []byte("tampered-candidate")
	entry := manifest.Entry{Path: "candidate.txt", Kind: "file", Size: "18", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	w := httptest.NewRecorder()
	writePinnedFile(w, &failAtEOF{payload: payload}, entry)
	if w.Code != 503 || bytes.Contains(w.Body.Bytes(), payload) || w.Header().Get("Content-Length") == entry.Size {
		t.Fatalf("substituted file was delivered: %d %q headers=%v", w.Code, w.Body.String(), w.Header())
	}
}

func TestPinnedInputWritesOnlyAVerifiedBody(t *testing.T) {
	payload := []byte("reviewed-candidate")
	entry := manifest.Entry{Path: "candidate.txt", Kind: "file", Size: "18", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	w := httptest.NewRecorder()
	writePinnedFile(w, bytes.NewReader(payload), entry)
	response := w.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if response.StatusCode != 200 || err != nil || !bytes.Equal(body, payload) || response.Header.Get("X-Content-Digest") != entry.Digest || response.Header.Get("Content-Length") != entry.Size {
		t.Fatalf("verified file not delivered: %d %q %v", response.StatusCode, body, err)
	}
}
