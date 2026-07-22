package titler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// respondWith writes a standard OpenAI chat-completions response carrying the
// given content string.
func respondWith(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}},
		},
	})
}

func newTestClient(baseURL string) *Client {
	return New(Config{BaseURL: baseURL, APIKey: "test-key", Model: "test-model"})
}

func TestGenerateTitle_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer test-key")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %q, want suffix /chat/completions", r.URL.Path)
		}
		respondWith(w, "Fix login bug")
	}))
	defer srv.Close()

	got, err := newTestClient(srv.URL).GenerateTitle(context.Background(), "help me fix the login bug")
	if err != nil {
		t.Fatalf("GenerateTitle: %v", err)
	}
	if got != "Fix login bug" {
		t.Errorf("title = %q, want %q", got, "Fix login bug")
	}
}

func TestGenerateTitle_Cleaning(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"quotes and period", "\"Fix login bug.\"\n", "Fix login bug"},
		{"title prefix", "Title: Deploy the service", "Deploy the service"},
		{"backticks", "`refactor parser`", "refactor parser"},
		{"single quotes", "'Add cache layer'", "Add cache layer"},
		{"collapse whitespace", "Fix   the\n\tbug", "Fix the bug"},
		{"trailing punctuation", "Update docs!!!", "Update docs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				respondWith(w, tc.content)
			}))
			defer srv.Close()

			got, err := newTestClient(srv.URL).GenerateTitle(context.Background(), "msg")
			if err != nil {
				t.Fatalf("GenerateTitle: %v", err)
			}
			if got != tc.want {
				t.Errorf("title = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGenerateTitle_Truncation(t *testing.T) {
	long := strings.Repeat("word ", 500) // ~2500 runes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondWith(w, long)
	}))
	defer srv.Close()

	got, err := newTestClient(srv.URL).GenerateTitle(context.Background(), "msg")
	if err != nil {
		t.Fatalf("GenerateTitle: %v", err)
	}
	if n := utf8.RuneCountInString(got); n > maxTitleRunes {
		t.Errorf("title length = %d runes, want <= %d", n, maxTitleRunes)
	}
}

func TestGenerateTitle_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).GenerateTitle(context.Background(), "msg")
	if err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
}

func TestGenerateTitle_EmptyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondWith(w, "  \n  ")
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).GenerateTitle(context.Background(), "msg")
	if err == nil {
		t.Fatal("expected error for blank content, got nil")
	}
}

func TestGenerateTitle_RetryOn500(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		respondWith(w, "Recovered title")
	}))
	defer srv.Close()

	got, err := newTestClient(srv.URL).GenerateTitle(context.Background(), "msg")
	if err != nil {
		t.Fatalf("GenerateTitle: %v", err)
	}
	if got != "Recovered title" {
		t.Errorf("title = %q, want %q", got, "Recovered title")
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("call count = %d, want 2 (one retry)", n)
	}
}

func TestGenerateTitle_NoRetryOn400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).GenerateTitle(context.Background(), "msg")
	if err == nil {
		t.Fatal("expected error for HTTP 400, got nil")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("call count = %d, want 1 (no retry on 4xx)", n)
	}
}

func TestGenerateTitle_Retries500ThenFails(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "still broken", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := newTestClient(srv.URL).GenerateTitle(context.Background(), "msg")
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("call count = %d, want 2 (initial + one retry)", n)
	}
}

func TestEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"all set", Config{BaseURL: "u", APIKey: "k", Model: "m"}, true},
		{"missing base url", Config{APIKey: "k", Model: "m"}, false},
		{"missing api key", Config{BaseURL: "u", Model: "m"}, false},
		{"missing model", Config{BaseURL: "u", APIKey: "k"}, false},
		{"whitespace only", Config{BaseURL: "  ", APIKey: "  ", Model: "  "}, false},
		{"empty", Config{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.cfg).Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGenerateTitle_DisabledReturnsError(t *testing.T) {
	_, err := New(Config{}).GenerateTitle(context.Background(), "msg")
	if err == nil {
		t.Fatal("expected error when client disabled, got nil")
	}
}

func TestGenerateTitle_CanceledContext(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		respondWith(w, "should not reach")
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestClient(srv.URL).GenerateTitle(ctx, "msg")
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("call count = %d, want 0 (request should not fire with canceled ctx)", n)
	}
}
