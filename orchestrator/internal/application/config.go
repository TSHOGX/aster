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

	"harness-platform/orchestrator/internal/engineconfig"
	"harness-platform/orchestrator/internal/product"
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
	Version  string `json:"version"`
	ID       string `json:"id"`
	Manifest string `json:"manifest"`
	Digest   string `json:"digest"`
}
type Content struct {
	AgentConfig  string `json:"agent_config"`
	SharedAssets string `json:"shared_assets"`
	VolumeBytes  int64  `json:"volume_bytes"`
}
type Limits struct {
	MaxMountsPerProject int      `json:"max_mounts_per_project"`
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
type ProjectDefaults struct {
	CPUMillis      int64                `json:"cpu_millis"`
	MemoryBytes    int64                `json:"memory_bytes"`
	DiskBytes      int64                `json:"disk_bytes"`
	WorkspaceBytes int64                `json:"workspace_bytes"`
	RuntimeBytes   int64                `json:"runtime_bytes"`
	Reachability   product.Reachability `json:"reachability"`
}
type Config struct {
	HTTPMetrics       bool                    `json:"http_metrics"`
	Version           int                     `json:"version"`
	Listen            string                  `json:"listen"`
	ApplicationSocket string                  `json:"application_socket"`
	Frontend          string                  `json:"frontend"`
	AdminFrontend     string                  `json:"admin_frontend"`
	StateDirectory    string                  `json:"state_directory"`
	ControlSocket     string                  `json:"control_socket"`
	Engine            engineconfig.Connection `json:"engine"`
	Template          Template                `json:"template"`
	Project           ProjectDefaults         `json:"project"`
	Content           Content                 `json:"content"`
	Limits            Limits                  `json:"limits"`
	Notifications     NotificationConfig      `json:"notifications"`
	Gateways          []ServiceGateway        `json:"gateways"`
	AgentAudience     string                  `json:"agent_audience"`
	Titles            TitleService            `json:"titles"`
	TLSCert           string                  `json:"tls_cert"`
	TLSKey            string                  `json:"tls_key"`
	base              string
	source            string
}

func defaults() Config {
	return Config{Listen: "127.0.0.1:8080", Frontend: "http://127.0.0.1:3000", AdminFrontend: "http://127.0.0.1:3001", Content: Content{VolumeBytes: 64 << 20}, Limits: Limits{Projects: 4, Agents: 32, AgentsPerProject: 8, Terminals: 32, TerminalsPerProject: 8, IdleAfter: Duration(5 * time.Minute), IdleAgentAfter: Duration(time.Minute), Interval: Duration(250 * time.Millisecond)}, Notifications: NotificationConfig{ConversationShared: true, OperatorDirected: true, OperatorBroadcast: true}}
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
	return cfg, nil
}
func (c Config) Validate() error {

	if c.Version != 1 || !filepath.IsAbs(c.StateDirectory) || !filepath.IsAbs(c.ControlSocket) || c.StateDirectory == "/" || c.ControlSocket == "/" {
		return errors.New("version 1 and explicit state/control paths required")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("browser listen address: %w", err)
	}
	if !filepath.IsAbs(c.ApplicationSocket) || c.ApplicationSocket == c.ControlSocket {
		return errors.New("distinct absolute private application and control sockets required")
	}
	for name, origin := range map[string]string{"frontend": c.Frontend, "admin_frontend": c.AdminFrontend} {
		frontend, err := url.Parse(origin)
		if err != nil || frontend.Host == "" || frontend.User != nil || frontend.RawQuery != "" || frontend.Fragment != "" || (frontend.Path != "" && frontend.Path != "/") || (frontend.Scheme != "http" && frontend.Scheme != "https") {
			return fmt.Errorf("%s must be an HTTP(S) origin", name)
		}
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("browser TLS certificate and key must be supplied together")
	}
	l := c.Limits
	if (l.MaxMountsPerProject != 0 && (l.MaxMountsPerProject < 2 || l.MaxMountsPerProject > 128)) || l.Projects < 1 || l.Projects > 128 || l.Agents < 1 || l.Agents > 256 || l.Terminals < 1 || l.Terminals > 256 || l.AgentsPerProject < 1 || l.AgentsPerProject > l.Agents || l.TerminalsPerProject < 1 || l.TerminalsPerProject > l.Terminals || time.Duration(l.IdleAfter) < time.Second || time.Duration(l.IdleAgentAfter) < time.Second || time.Duration(l.Interval) < 50*time.Millisecond || time.Duration(l.Interval) > 10*time.Second {
		return errors.New("invalid runtime capacity or timing bounds")
	}
	if len(c.Gateways) > 64 {
		return errors.New("at most 64 gateway audiences supported")
	}
	if len(c.Gateways) > 0 {
		if c.GatewaySocket() == c.ControlSocket || c.GatewaySocket() == c.ApplicationSocket {
			return errors.New("gateway control socket must differ from owner sockets")
		}

		for name, origin := range map[string]string{"engine": c.Engine.Endpoint, "frontend": c.Frontend, "admin_frontend": c.AdminFrontend} {
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
	return nil
}
func (c Config) titleGenerator() product.TitleGenerator {
	if c.Titles.BaseURL == "" || c.Titles.Model == "" {
		return nil
	}
	return titler.New(titler.Config{BaseURL: c.Titles.BaseURL, Model: c.Titles.Model, APIKey: os.Getenv(c.Titles.APIKeyEnv)})
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

func (c Config) RuntimeBindings() product.RuntimeBindings {
	return product.RuntimeBindings{StoreID: c.Engine.StoreID, Services: c.Project.Reachability.Services, Hosts: c.Project.Reachability.Hosts}
}
