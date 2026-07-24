// Package memory implements the nightly trajectory-distillation pipeline: it
// reads normalized session trajectories, runs LLM extraction "lenses" over them
// (facts, failures, skills), consolidates the results into semantic memory, and
// promotes stable, well-supported knowledge into the shared /agents knowledge
// base that every new sandbox mounts read-only.
//
// The LLM client here deliberately mirrors internal/titler: a self-contained,
// OpenAI-compatible chat-completions client with its own secret config, Bearer
// auth, a generous backstop timeout (callers own the real deadline via ctx),
// and at most one retry on transient failure. The in-repo model proxy is a
// separate process that serves sandbox traffic only and is NOT a server-side
// client, so this pipeline calls the upstream API directly like the titler.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Config holds the connection settings for the distillation LLM.
type Config struct {
	BaseURL string // e.g. https://api.modelarts-maas.com/openai/v1
	APIKey  string
	Model   string // e.g. glm-5.2
}

// Extractor runs lens prompts against an OpenAI-compatible chat API and decodes
// the model's JSON reply into a caller-supplied struct.
type Extractor struct {
	cfg  Config
	http *http.Client
}

// NewExtractor builds an Extractor. It is always safe to construct; call
// Enabled to check whether it can actually make requests.
func NewExtractor(cfg Config) *Extractor {
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	return &Extractor{
		cfg: cfg,
		// Backstop only; the pipeline owns the real per-call deadline via ctx.
		http: &http.Client{Timeout: 120 * time.Second},
	}
}

// Enabled reports whether the extractor has enough config to attempt a call.
func (e *Extractor) Enabled() bool {
	return e.cfg.BaseURL != "" && e.cfg.APIKey != "" && e.cfg.Model != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string          `json:"model"`
	Messages    []chatMessage   `json:"messages"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature float64         `json:"temperature"`
	Stream      bool            `json:"stream"`
	Format      *responseFormat `json:"response_format,omitempty"`
}

// responseFormat requests JSON output. Many OpenAI-compatible providers honor
// {"type":"json_object"}; when unsupported it is ignored and we still parse the
// content as JSON (extractJSON tolerates prose/fenced wrappers).
type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Extract runs a single lens: it sends systemPrompt + transcript and unmarshals
// the model's JSON reply into out (a pointer). Respects ctx; performs at most
// one retry on transient failure (network / HTTP 5xx / 429) with a short
// backoff. Returns an error the caller can log-and-skip on.
func (e *Extractor) Extract(ctx context.Context, systemPrompt, transcript string, out any) error {
	if !e.Enabled() {
		return fmt.Errorf("memory: extractor not enabled (missing base url, api key, or model)")
	}

	body, err := json.Marshal(chatRequest{
		Model: e.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: transcript},
		},
		// Generous budget: reasoning models spend most of the completion budget
		// on hidden reasoning before emitting the (small) JSON payload.
		MaxTokens:   4096,
		Temperature: 0.2,
		Stream:      false,
		Format:      &responseFormat{Type: "json_object"},
	})
	if err != nil {
		return fmt.Errorf("memory: marshal request: %w", err)
	}

	url := strings.TrimRight(e.cfg.BaseURL, "/") + "/chat/completions"

	const maxAttempts = 2
	const backoff = 500 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("memory: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		content, retry, err := e.attempt(ctx, url, body)
		if err != nil {
			lastErr = err
			if !retry {
				return err
			}
			continue
		}
		payload := extractJSON(content)
		if payload == "" {
			return fmt.Errorf("memory: model returned no JSON payload")
		}
		if err := json.Unmarshal([]byte(payload), out); err != nil {
			// A malformed body is not worth retrying — the prompt is deterministic.
			return fmt.Errorf("memory: decode lens output: %w", err)
		}
		return nil
	}
	return lastErr
}

// attempt performs a single request and returns the raw message content.
func (e *Extractor) attempt(ctx context.Context, url string, body []byte) (content string, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", false, fmt.Errorf("memory: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("memory: request: %w", err)
		}
		return "", true, fmt.Errorf("memory: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", resp.StatusCode >= 500, fmt.Errorf("memory: read response: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", true, fmt.Errorf("memory: rate limited: status %d: %s", resp.StatusCode, snippet(respBody))
	}
	if resp.StatusCode >= 500 {
		return "", true, fmt.Errorf("memory: server error: status %d: %s", resp.StatusCode, snippet(respBody))
	}
	if resp.StatusCode >= 400 {
		return "", false, fmt.Errorf("memory: client error: status %d: %s", resp.StatusCode, snippet(respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", false, fmt.Errorf("memory: decode response envelope: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", false, fmt.Errorf("memory: response contained no choices")
	}
	return parsed.Choices[0].Message.Content, false, nil
}

// extractJSON pulls the first balanced JSON object out of a model reply,
// tolerating ```json fences and leading/trailing prose that some models emit
// even when asked for pure JSON.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// Strip a leading ```json / ``` fence if present.
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// snippet returns a short single-line excerpt of a response body for errors.
func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max])
	}
	return s
}
