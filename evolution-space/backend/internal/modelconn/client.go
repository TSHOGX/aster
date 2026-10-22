package modelconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Client calls OpenAI-compatible chat completions. It never follows redirects
// or a proxy from the environment.
type Client struct{ http *http.Client }

func NewClient() *Client {
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: time.Minute, MaxIdleConnsPerHost: 8}
	return &Client{http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Reply struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

// Complete sends one non-streaming completion. A non-200 status is returned
// without an error so callers can classify it.
func (c *Client) Complete(ctx context.Context, settings Settings, apiKey string, messages []Message, maxTokens int64) (Reply, int, error) {
	var reply Reply
	body := map[string]any{"model": settings.Model, "messages": messages, "max_tokens": maxTokens, "stream": false}
	if settings.Temperature != nil {
		body["temperature"] = *settings.Temperature
	}
	if settings.JSONMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	raw, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return reply, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return reply, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return reply, 0, err
	}
	if response.StatusCode != 200 {
		return reply, response.StatusCode, nil
	}
	if err = json.Unmarshal(data, &reply); err != nil {
		return reply, 502, nil
	}
	return reply, 200, nil
}

// TestResult reports one small completion. It never includes the API key.
type TestResult struct {
	OK            bool     `json:"ok"`
	Status        int      `json:"status"`
	LatencyMillis int64    `json:"latency_ms"`
	ResponseModel string   `json:"response_model,omitempty"`
	Reply         string   `json:"reply,omitempty"`
	Tokens        int64    `json:"tokens"`
	Error         string   `json:"error,omitempty"`
	Models        []string `json:"models,omitempty"`
}

// Test lists available models and runs a minimal JSON completion with exactly
// these settings, so administrators can verify a connection before saving.
func (c *Client) Test(ctx context.Context, settings Settings, apiKey string) TestResult {
	settings, err := settings.Normalize()
	if err != nil {
		return TestResult{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result := TestResult{Models: c.models(ctx, settings.BaseURL, apiKey)}
	started := time.Now()
	limit := min(settings.MaxOutputTokens, 1024)
	reply, status, err := c.Complete(ctx, settings, apiKey, []Message{{Role: "system", Content: "Reply with exactly one JSON object."}, {Role: "user", Content: `Return {"ok":true}`}}, limit)
	result.LatencyMillis, result.Status = time.Since(started).Milliseconds(), status
	switch {
	case err != nil:
		result.Error = "the model service is unreachable or timed out"
	case status == 401 || status == 403:
		result.Error = fmt.Sprintf("HTTP %d: the API key was rejected", status)
	case status != 200:
		result.Error = fmt.Sprintf("HTTP %d from the model service; check the base URL and model name", status)
	case len(reply.Choices) == 0:
		result.Error = "the model returned no choices"
	default:
		result.ResponseModel, result.Tokens, result.Reply = Bound(reply.Model), reply.Usage.TotalTokens, Bound(reply.Choices[0].Message.Content)
		result.OK = strings.Contains(result.Reply, "{")
		if !result.OK {
			result.Error = "the reply was not JSON; try turning JSON mode on or off, or a larger output limit"
		}
	}
	return result
}
func (c *Client) models(ctx context.Context, base, apiKey string) []string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := c.http.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	var page struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&page) != nil {
		return nil
	}
	result := []string{}
	for _, item := range page.Data {
		if item.ID != "" && len(item.ID) <= 200 && len(result) < 200 {
			result = append(result, item.ID)
		}
	}
	sort.Strings(result)
	return result
}

// Bound limits provider text kept in evidence and API errors.
func Bound(text string) string {
	if len(text) > 500 {
		return text[:500]
	}
	return text
}
