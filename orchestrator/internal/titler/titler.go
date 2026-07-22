// Package titler generates short session titles from a user's first message
// by calling an OpenAI-compatible chat-completions API.
package titler

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

// systemPrompt instructs the model to produce a concise title. Kept to one line.
const systemPrompt = "You generate a concise session title from the user's message. Respond with only a 3-5 word title in the same language as the user's message. Do not add surrounding quotes, trailing punctuation, or any prefix such as \"Title:\". Output the title on a single line."

// maxTitleRunes bounds the cleaned title length.
const maxTitleRunes = 200

// Config holds the connection settings for the title-generation LLM.
type Config struct {
	BaseURL string // e.g. https://api.modelarts-maas.com/openai/v1
	APIKey  string
	Model   string // e.g. glm-5.2
}

// Client generates session titles via an OpenAI-compatible chat completions API.
type Client struct {
	cfg  Config
	http *http.Client
}

// New builds a Client. It is always safe to construct; call Enabled to check
// whether it can actually generate titles.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Model = strings.TrimSpace(cfg.Model)
	return &Client{
		cfg: cfg,
		// Timeout is a backstop only; callers own the real deadline via ctx.
		// Reasoning models can take tens of seconds, so keep this generous.
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// Enabled reports whether the client has enough config (base URL + api key +
// model) to attempt generation.
func (c *Client) Enabled() bool {
	return c.cfg.BaseURL != "" && c.cfg.APIKey != "" && c.cfg.Model != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// GenerateTitle asks the model to condense the user's first message into a
// short 3-5 word title. Returns the cleaned title, or an error. Respects ctx
// for cancellation/timeout. Callers own the timeout via ctx, but GenerateTitle
// also applies its own internal safety: it performs at most ONE retry on
// transient failure (network error or HTTP 5xx), with a short backoff.
func (c *Client) GenerateTitle(ctx context.Context, firstMessage string) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("titler: client not enabled (missing base url, api key, or model)")
	}

	body, err := json.Marshal(chatRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: firstMessage},
		},
		// Large budget: some providers (e.g. reasoning models like glm-5.2)
		// spend most of the completion budget on hidden reasoning tokens before
		// emitting the short title. A small cap would starve reasoning and yield
		// empty content, so give ample room; the visible title is still tiny.
		MaxTokens:   2048,
		Temperature: 0.3,
		Stream:      false,
	})
	if err != nil {
		return "", fmt.Errorf("titler: marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"

	const maxAttempts = 2
	const backoff = 300 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Short backoff before the single retry, but respect ctx.
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("titler: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		title, retry, err := c.attempt(ctx, url, body)
		if err == nil {
			return title, nil
		}
		lastErr = err
		if !retry {
			return "", err
		}
	}

	return "", lastErr
}

// attempt performs a single request. It returns the cleaned title on success.
// The retry bool indicates whether the error is transient and worth retrying.
func (c *Client) attempt(ctx context.Context, url string, body []byte) (title string, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", false, fmt.Errorf("titler: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Network-level failure; if ctx is done, do not retry.
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("titler: request: %w", err)
		}
		return "", true, fmt.Errorf("titler: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", resp.StatusCode >= 500, fmt.Errorf("titler: read response: %w", err)
	}

	// 429 (rate limited) is transient: retry like a 5xx.
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", true, fmt.Errorf("titler: rate limited: status %d: %s", resp.StatusCode, snippet(respBody))
	}
	if resp.StatusCode >= 500 {
		return "", true, fmt.Errorf("titler: server error: status %d: %s", resp.StatusCode, snippet(respBody))
	}
	if resp.StatusCode >= 400 {
		return "", false, fmt.Errorf("titler: client error: status %d: %s", resp.StatusCode, snippet(respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", false, fmt.Errorf("titler: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", false, fmt.Errorf("titler: response contained no choices")
	}

	cleaned := clean(parsed.Choices[0].Message.Content)
	if cleaned == "" {
		return "", false, fmt.Errorf("titler: model returned an empty title")
	}
	return cleaned, false, nil
}

// clean normalizes the raw model output into a usable title.
func clean(s string) string {
	s = strings.TrimSpace(s)

	// Collapse internal whitespace/newlines to single spaces.
	s = strings.Join(strings.Fields(s), " ")

	// Strip a leading "Title:" (case-insensitive) prefix.
	if len(s) >= len("title:") && strings.EqualFold(s[:len("title:")], "title:") {
		s = strings.TrimSpace(s[len("title:"):])
	}

	// Strip surrounding single/double quotes and backticks, potentially nested.
	for len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}

	// Strip trailing punctuation.
	s = strings.TrimRight(s, ".!?,;:")
	s = strings.TrimSpace(s)

	// Truncate to at most maxTitleRunes runes.
	if utf8.RuneCountInString(s) > maxTitleRunes {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:maxTitleRunes]))
	}

	return s
}

// snippet returns a short, single-line excerpt of a response body for error
// messages. It never includes request headers.
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
