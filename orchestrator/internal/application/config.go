package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"harness-platform/orchestrator/internal/engineconfig"
	"harness-platform/orchestrator/internal/product"
	"harness-platform/orchestrator/internal/producthttp"
	"harness-platform/orchestrator/internal/runtimerelease"
	"harness-platform/orchestrator/internal/titler"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

type Template struct {
	ID       string `json:"id"`
	Manifest string `json:"manifest"`
	Digest   string `json:"digest"`
}
type Authentication struct {
	UsersFile        string `json:"users_file"`
	DevelopmentActor string `json:"development_actor"`
}
type Content struct {
	AgentConfig  string `json:"agent_config"`
	SharedAssets string `json:"shared_assets"`
	VolumeBytes  int64  `json:"volume_bytes"`
}
type Limits struct {
	Projects            int      `json:"projects"`
	Agents              int      `json:"agents"`
	AgentsPerProject    int      `json:"agents_per_project"`
	Terminals           int      `json:"terminals"`
	TerminalsPerProject int      `json:"terminals_per_project"`
	IdleAfter           Duration `json:"idle_after"`
	IdleAgentAfter      Duration `json:"idle_agent_after"`
	Interval            Duration `json:"interval"`
}
type NotificationConfig struct {
	AgentRunOutcome    bool `json:"agent_run_outcome"`
	ConversationShared bool `json:"conversation_shared"`
	OperatorDirected   bool `json:"operator_directed"`
	OperatorBroadcast  bool `json:"operator_broadcast"`
}
type ServiceGateway struct {
	ScheduleTools bool   `json:"schedule_tools"`
	Audience      string `json:"audience"`
	Listen        string `json:"listen"`
	Upstream      string `json:"upstream"`
	APIKeyEnv     string `json:"api_key_env"`
	TLSCert       string `json:"tls_cert"`
	TLSKey        string `json:"tls_key"`
	MaxRequests   int    `json:"max_requests"`
}
type TitleService struct {
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"api_key_env"`
}
type Config struct {
	HTTPMetrics       bool                    `json:"http_metrics"`
	Version           int                     `json:"version"`
	Listen            string                  `json:"listen"`
	ApplicationSocket string                  `json:"application_socket"`
	Frontend          string                  `json:"frontend"`
	StateDirectory    string                  `json:"state_directory"`
	ControlSocket     string                  `json:"control_socket"`
	Engine            engineconfig.Connection `json:"engine"`
	Template          Template                `json:"template"`
	Project           product.Profile         `json:"project"`
	Auth              Authentication          `json:"auth"`
	Content           Content                 `json:"content"`
	Limits            Limits                  `json:"limits"`
	Notifications     NotificationConfig      `json:"notifications"`
	Gateways          []ServiceGateway        `json:"gateways"`
	AgentAudience     string                  `json:"agent_audience"`
	Titles            TitleService            `json:"titles"`
	TLSCert           string                  `json:"tls_cert"`
	TLSKey            string                  `json:"tls_key"`
	Manifest          runtimerelease.Manifest `json:"-"`
	base              string
	source            string
}

func defaults() Config {
	return Config{Listen: "127.0.0.1:8080", Frontend: "http://127.0.0.1:3000", Content: Content{VolumeBytes: 64 << 20}, Limits: Limits{Projects: 4, Agents: 32, AgentsPerProject: 8, Terminals: 32, TerminalsPerProject: 8, IdleAfter: Duration(5 * time.Minute), IdleAgentAfter: Duration(time.Minute), Interval: Duration(250 * time.Millisecond)}, Notifications: NotificationConfig{ConversationShared: true, OperatorDirected: true, OperatorBroadcast: true}}
}
func LoadConfig(name string) (Config, error) {
	cfg := defaults()
	absolute, err := filepath.Abs(name)
	if err != nil {
		return cfg, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return cfg, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return cfg, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return cfg, errors.New("application configuration must be a regular file of at most one MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("application configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return cfg, errors.New("trailing application configuration")
	}
	cfg.base = filepath.Dir(absolute)
	cfg.source = absolute
	resolve := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(cfg.base, value)
	}
	cfg.StateDirectory = resolve(cfg.StateDirectory)
	cfg.ApplicationSocket = resolve(cfg.ApplicationSocket)
	cfg.ControlSocket = resolve(cfg.ControlSocket)
	cfg.Auth.UsersFile = resolve(cfg.Auth.UsersFile)
	cfg.Template.Manifest = resolve(cfg.Template.Manifest)
	cfg.Content.AgentConfig = resolve(cfg.Content.AgentConfig)
	cfg.Content.SharedAssets = resolve(cfg.Content.SharedAssets)
	cfg.TLSCert = resolve(cfg.TLSCert)
	cfg.TLSKey = resolve(cfg.TLSKey)
	for i := range cfg.Gateways {
		cfg.Gateways[i].TLSCert = resolve(cfg.Gateways[i].TLSCert)
		cfg.Gateways[i].TLSKey = resolve(cfg.Gateways[i].TLSKey)
	}
	if cfg.ApplicationSocket == "" && cfg.StateDirectory != "" {
		cfg.ApplicationSocket = filepath.Join(cfg.StateDirectory, "application.sock")
	}
	if cfg.ControlSocket == "" && cfg.StateDirectory != "" {
		cfg.ControlSocket = filepath.Join(cfg.StateDirectory, "control.sock")
	}
	if err = cfg.Validate(); err != nil {
		return cfg, err
	}
	manifestFile, err := os.Open(cfg.Template.Manifest)
	if err != nil {
		return cfg, err
	}
	defer manifestFile.Close()
	cfg.Manifest, err = runtimerelease.Load(manifestFile, cfg.Template.Digest)
	return cfg, err
}
func (c Config) Validate() error {
	if c.Project.NetworkPolicyRevision != "" {
		return errors.New("network policy revision is application-owned")
	}
	if c.Version != 1 || !filepath.IsAbs(c.StateDirectory) || !filepath.IsAbs(c.ControlSocket) || c.StateDirectory == "/" || c.ControlSocket == "/" {
		return errors.New("version 1 and explicit state/control paths required")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("browser listen address: %w", err)
	}
	if !filepath.IsAbs(c.ApplicationSocket) || c.ApplicationSocket == c.ControlSocket {
		return errors.New("distinct absolute private application and control sockets required")
	}
	frontend, err := url.Parse(c.Frontend)
	if err != nil || frontend.Host == "" || frontend.User != nil || frontend.RawQuery != "" || frontend.Fragment != "" || (frontend.Path != "" && frontend.Path != "/") || (frontend.Scheme != "http" && frontend.Scheme != "https") {
		return errors.New("frontend must be an HTTP(S) origin")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("browser TLS certificate and key must be supplied together")
	}
	if c.Template.ID == "" || strings.ContainsAny(c.Template.ID, "/\\\x00?# ") || !runtimerelease.ValidDigest(c.Template.Digest) || !filepath.IsAbs(c.Template.Manifest) {
		return errors.New("template identity and pinned runtime manifest required")
	}
	if c.Project.StoreID != "" || c.Project.TemplateID != "" || len(c.Project.PublishedMounts) > 0 || c.Project.ReleaseDigest != "" {
		return errors.New("Project store, template and publication bindings are derived by the application")
	}
	p := c.Project
	p.StoreID = c.Engine.StoreID
	p.TemplateID = c.Template.ID
	p.ReleaseDigest = c.Template.Digest
	if err := p.Validate(); err != nil {
		return fmt.Errorf("Project configuration: %w", err)
	}
	if p.CPUMillis <= 0 || p.MemoryBytes <= 0 || p.DiskBytes <= 0 || p.WorkspaceBytes <= 0 || p.RuntimeBytes <= 0 {
		return errors.New("positive Project resource and storage bounds required")
	}
	if c.Content.VolumeBytes <= 0 {
		return errors.New("positive content publication capacity required")
	}
	if (c.Auth.UsersFile == "") == (c.Auth.DevelopmentActor == "") {
		return errors.New("choose a credential file or an explicit development actor")
	}
	if c.Auth.DevelopmentActor != "" {
		host, _, _ := net.SplitHostPort(c.Listen)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return errors.New("development actor requires a loopback browser listener")
		}
	}
	l := c.Limits
	if l.Projects < 1 || l.Projects > 128 || l.Agents < 1 || l.Agents > 256 || l.Terminals < 1 || l.Terminals > 256 || l.AgentsPerProject < 1 || l.AgentsPerProject > l.Agents || l.TerminalsPerProject < 1 || l.TerminalsPerProject > l.Terminals || time.Duration(l.IdleAfter) < time.Second || time.Duration(l.IdleAgentAfter) < time.Second || time.Duration(l.Interval) < 50*time.Millisecond || time.Duration(l.Interval) > 10*time.Second {
		return errors.New("invalid runtime capacity or timing bounds")
	}
	if len(c.Gateways) > 64 {
		return errors.New("at most 64 gateway audiences supported")
	}
	if len(c.Gateways) > 0 {
		if c.GatewaySocket() == c.ControlSocket || c.GatewaySocket() == c.ApplicationSocket {
			return errors.New("gateway control socket must differ from owner sockets")
		}

		for name, origin := range map[string]string{"engine": c.Engine.Endpoint, "frontend": c.Frontend} {
			if gatewayFloorOrigin(origin) {
				return fmt.Errorf("%s origin is blocked by Khora's gateway UID network floor; use a non-loopback endpoint", name)
			}
		}
	}
	audiences := map[string]bool{}
	for _, gateway := range c.Gateways {
		if gatewayFloorOrigin(gateway.Upstream) {
			return errors.New("gateway upstream is blocked by Khora's second-hop floor")
		}

		if gateway.Audience == "" || audiences[gateway.Audience] || gateway.MaxRequests < 1 || gateway.MaxRequests > 256 {
			return errors.New("unique gateway audiences and bounded request capacity required")
		}
		audiences[gateway.Audience] = true
		if _, _, err := net.SplitHostPort(gateway.Listen); err != nil {
			return err
		}
		if (gateway.TLSCert == "") != (gateway.TLSKey == "") {
			return errors.New("gateway TLS certificate/key must be paired")
		}
		if _, declared := c.Project.Reachability.Audience(gateway.Audience); !declared {
			return errors.New("gateway audience is absent from Project reachability")
		}
	}
	if c.AgentAudience != "" && !audiences[c.AgentAudience] {
		return errors.New("Agent audience requires its configured gateway")
	}
	if c.Auth.UsersFile != "" {
		if _, err := ReadUsers(c.Auth.UsersFile); err != nil {
			return err
		}
	}
	return nil
}
func ReadUsers(name string) ([]producthttp.AuthUser, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("credential registry must be a regular file of at most one MiB")
	}
	decoder := yaml.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.KnownFields(true)
	var document struct {
		Users []producthttp.AuthUser `yaml:"users"`
	}
	if err = decoder.Decode(&document); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing credential document")
	}
	if len(document.Users) > 10000 {
		return nil, errors.New("credential registry exceeds bound")
	}
	return document.Users, nil
}
func (c Config) titleGenerator() product.TitleGenerator {
	if c.Titles.BaseURL == "" || c.Titles.Model == "" {
		return nil
	}
	return titler.New(titler.Config{BaseURL: c.Titles.BaseURL, Model: c.Titles.Model, APIKey: os.Getenv(c.Titles.APIKeyEnv)})
}

func (c Config) Profile() product.Profile {
	p := c.Project
	p.StoreID = c.Engine.StoreID
	p.TemplateID = c.Template.ID
	p.ReleaseDigest = c.Template.Digest
	return p
}
func (c Config) DatabasePath() string { return filepath.Join(c.StateDirectory, "product.db") }

func gatewayFloorOrigin(origin string) bool {
	endpoint, err := url.Parse(origin)
	if err != nil {
		return true
	}
	host := endpoint.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
	}
	return false
}
