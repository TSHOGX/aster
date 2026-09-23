package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type providerConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

// providerReport contains no request/response bodies, credentials or headers.
type providerReport struct {
	Provider                            string    `json:"provider"`
	Model                               string    `json:"model"`
	Requests                            int       `json:"requests"`
	Status                              int       `json:"status"`
	Started                             time.Time `json:"started"`
	FirstDelta                          time.Time `json:"first_delta"`
	Ended                               time.Time `json:"ended"`
	Bytes                               int64     `json:"bytes"`
	Deltas                              int       `json:"deltas"`
	FinishReason                        string    `json:"finish_reason"`
	Done                                bool      `json:"done"`
	Error                               string    `json:"error,omitempty"`
	Project, Session, Incarnation, Turn string
}

type providerRelay struct {
	config     providerConfig
	client     *http.Client
	reportPath string
	mu         sync.Mutex
	requests   int
	latest     providerReport
}

func (p *providerRelay) report(value providerReport) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value.Requests = p.requests
	p.latest = value
	p.saveReport()
}
func (p *providerRelay) saveReport() {
	raw, _ := json.MarshalIndent(p.latest, "", "  ")
	temporary := p.reportPath + ".tmp"
	if err := os.WriteFile(temporary, raw, 0600); err == nil {
		_ = os.Rename(temporary, p.reportPath)
	}
}

func (p *providerRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.URL.RawQuery != "" {
		http.Error(w, "unsupported qualification request", 400)
		return
	}
	var body map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body["model"] != p.config.Model || body["stream"] != true {
		http.Error(w, "invalid qualification request", 400)
		return
	}
	p.mu.Lock()
	p.requests++
	requests := p.requests
	if requests > 1 {
		p.latest.Requests = requests
		p.saveReport()
	}
	p.mu.Unlock()
	if requests != 1 {
		http.Error(w, "qualification permits one provider request", 429)
		return
	}
	report := providerReport{Model: p.config.Model, Requests: requests, Started: time.Now().UTC(), Project: r.Header.Get("X-Project-Id"), Session: r.Header.Get("X-Session-Id"), Incarnation: r.Header.Get("X-Incarnation-Id"), Turn: r.Header.Get("X-Turn-Id")}
	target, _ := url.Parse(p.config.BaseURL)
	report.Provider = target.Hostname()
	p.report(report)
	// The fixture never permits unbounded generation or native tool side effects.
	body["max_tokens"] = 1536
	delete(body, "max_completion_tokens")
	delete(body, "tools")
	body["tool_choice"] = "none"
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	target.Path = strings.TrimRight(target.Path, "/") + "/chat/completions"
	request, err := http.NewRequestWithContext(ctx, "POST", target.String(), bytes.NewReader(raw))
	if err != nil {
		http.Error(w, "provider request invalid", 500)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	response, err := p.client.Do(request)
	if err != nil {
		report.Error = "provider connection failed"
		report.Ended = time.Now().UTC()
		p.report(report)
		http.Error(w, report.Error, 502)
		return
	}
	defer response.Body.Close()
	report.Status = response.StatusCode
	if response.StatusCode != 200 {
		report.Error = "provider rejected qualification"
		report.Ended = time.Now().UTC()
		p.report(report)
		http.Error(w, report.Error, response.StatusCode)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	w.(http.Flusher).Flush()
	reader := bufio.NewReaderSize(response.Body, 64<<10)
	for {
		line, readErr := reader.ReadSlice('\n')
		report.Bytes += int64(len(line))
		if report.Bytes > 2<<20 || errors.Is(readErr, bufio.ErrBufferFull) {
			report.Error = "provider stream exceeded fixture bound"
			break
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.HasPrefix(line, []byte("data:")) {
			if bytes.Equal(data, []byte("[DONE]")) {
				report.Done = true
			} else {
				var chunk struct {
					Choices []struct {
						Delta struct {
							Content   string `json:"content"`
							Reasoning string `json:"reasoning_content"`
						}
						Finish *string `json:"finish_reason"`
					}
				}
				_ = json.Unmarshal(data, &chunk)
				for _, choice := range chunk.Choices {
					if choice.Delta.Content != "" || choice.Delta.Reasoning != "" {
						report.Deltas++
						if report.FirstDelta.IsZero() {
							report.FirstDelta = time.Now().UTC()
							p.report(report)
						}
					}
					if choice.Finish != nil {
						report.FinishReason = *choice.Finish
					}
				}
			}
		}
		if len(line) > 0 {
			if _, err = w.Write(line); err != nil {
				report.Error = "downstream disconnected"
				break
			}
			w.(http.Flusher).Flush()
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				report.Error = "provider stream read failed"
			}
			break
		}
	}
	report.Ended = time.Now().UTC()
	p.report(report)
}

func runProviderRelay(args []string) error {
	flags := flag.NewFlagSet("provider-relay", flag.ContinueOnError)
	configPath := flags.String("config", "", "host-only provider YAML")
	socket := flags.String("socket", "", "new protected Unix socket")
	reportPath := flags.String("report", "", "metadata-only report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !filepath.IsAbs(*configPath) || !filepath.IsAbs(*socket) || !filepath.IsAbs(*reportPath) || flags.NArg() != 0 {
		return errors.New("absolute config, socket and report paths required")
	}
	file, err := os.Open(*configPath)
	if err != nil {
		return errors.New("provider configuration unavailable")
	}
	defer file.Close()
	var cfg providerConfig
	if err = yaml.NewDecoder(io.LimitReader(file, 64<<10)).Decode(&cfg); err != nil {
		return errors.New("invalid provider configuration")
	}
	endpoint, err := url.Parse(cfg.BaseURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || cfg.Model == "" || cfg.APIKey == "" {
		return errors.New("explicit HTTPS provider, model and credential required")
	}
	listener, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(*socket, 0600); err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 45 * time.Second
	defer transport.CloseIdleConnections()
	relay := &providerRelay{config: cfg, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, reportPath: *reportPath}
	server := &http.Server{Handler: relay, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); server.Close() }()
	relay.report(providerReport{Provider: endpoint.Hostname(), Model: cfg.Model})
	fmt.Println("provider-relay-ready")
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// This bridge runs inside the disposable network namespace. Only its protected
// host Unix peer has the real credential; the guest reaches the regular gateway.
func providerBridge() {
	socket := os.Getenv("PROJECT_PROVIDER_SOCKET")
	if !filepath.IsAbs(socket) {
		panic("protected provider socket required")
	}
	target, _ := url.Parse("http://provider-relay")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "provider relay unavailable", 502)
	}
	if err := http.ListenAndServe(modelAddress(), proxy); err != nil {
		panic(err)
	}
}
