package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProviderRelayCapsRequestsAndNeverReportsSecretsOrBodies(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/openai/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer real-fixture-key" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Error("relay did not isolate credentials or path")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["max_tokens"] != float64(1536) || body["max_completion_tokens"] != nil || body["tools"] != nil || body["tool_choice"] != "none" {
			t.Error("generation cap or tool policy missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"private response\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	report := filepath.Join(t.TempDir(), "report.json")
	p := &providerRelay{config: providerConfig{BaseURL: upstream.URL + "/openai/v1", APIKey: "real-fixture-key", Model: "fixture"}, client: upstream.Client(), reportPath: report}
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fixture","stream":true,"messages":[{"role":"user","content":"private prompt"}],"max_tokens":999999,"max_completion_tokens":999999,"tools":[]}`))
		r.Header.Set("Cookie", "private cookie")
		r.Header.Set("Authorization", "Bearer guest-key")
		r.Header.Set("X-Api-Key", "guest-key")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	if w := call(); w.Code != 200 || !strings.Contains(w.Body.String(), "private response") {
		t.Fatal("relay changed provider stream", w.Code)
	}
	if w := call(); w.Code != 429 || calls.Load() != 1 {
		t.Fatal("provider request repeated", w.Code, calls.Load())
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private prompt", "private response", "real-fixture-key", "guest-key", "private cookie"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("sensitive content in provider report")
		}
	}
	var value providerReport
	if json.Unmarshal(raw, &value) != nil || value.Requests != 2 || !value.Done || value.Deltas != 1 || value.FinishReason != "stop" || value.FirstDelta.IsZero() || value.Ended.IsZero() {
		t.Fatal("provider report lost stream evidence", string(raw))
	}
}
