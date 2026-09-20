package retainedapp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/launchplan"
	"harness-platform/orchestrator/internal/sandbox"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
)

type Deployment struct {
	BackendName                                                                            string
	Engine                                                                                 khorabackend.Config
	Gateways                                                                               []gateway.RemoteConfig
	Plans                                                                                  *launchplan.Source
	Services                                                                               egress.Catalog
	Content                                                                                []AuthoredContent
	ContentBytes                                                                           int64
	PreparedRoot, AuthorizationSocket                                                      string
	AuthorizationUID                                                                       uint32
	AuthorizationGroup                                                                     int
	MaxActive, InventoryLimit                                                              int
	LeaseTTL, PollInterval, EventInterval, ActionTimeout, CaptureWindow, InventoryInterval time.Duration
	IdleInterval, IdleThreshold                                                            time.Duration
}

// AuthoredContent is one reviewed source tree the deployment publishes before it
// admits a launch. Its revision is read from the tree, never declared, so the
// document cannot claim an identity its bytes do not have.
type AuthoredContent struct {
	Purpose  string
	Root     string
	Revision content.Revision
}
type deploymentFile struct {
	Version     int    `json:"version"`
	BackendName string `json:"backend_name"`
	Engine      struct {
		Endpoint   string `json:"endpoint"`
		StoreID    string `json:"store_id"`
		CAFile     string `json:"ca_file"`
		ClientCert string `json:"client_cert"`
		ClientKey  string `json:"client_key"`
	} `json:"engine"`
	Manifest       string `json:"manifest"`
	ManifestDigest string `json:"manifest_digest"`
	Plans          map[string]struct {
		Template       string          `json:"template"`
		CPUMillis      int64           `json:"cpu_millis"`
		MemoryBytes    int64           `json:"memory_bytes"`
		DiskBytes      int64           `json:"disk_bytes"`
		WorkspaceBytes int64           `json:"workspace_bytes"`
		JournalBytes   int64           `json:"journal_bytes"`
		Config         string          `json:"config"`
		Assets         string          `json:"assets"`
		Network        sandbox.Network `json:"network"`
		Environment    []string        `json:"environment"`
	} `json:"plans"`
	ContentBytes int64 `json:"content_bytes"`
	Gateways     []struct {
		ID       string  `json:"id"`
		Protocol string  `json:"protocol"`
		Socket   string  `json:"socket"`
		PeerUID  *uint32 `json:"peer_uid"`
		// Listener is where this gateway accepts steered guest traffic. It is the
		// endpoint the engine's route sends packets to, distinct from the Unix
		// control socket the application reaches it on.
		Listener string `json:"listener"`
	} `json:"gateways"`
	// Services is the declared service inventory and per-driver entitlements. It is
	// product authority; guest content never contributes to it. A deployment may
	// omit it entirely, and every driver then runs with no service access.
	Services struct {
		Revision       string                        `json:"revision"`
		SyntheticRange string                        `json:"synthetic_range"`
		Protected      []string                      `json:"protected"`
		Catalog        []egress.Service              `json:"catalog"`
		Policies       map[string]egress.ServiceList `json:"policies"`
	} `json:"services"`
	Authorization struct {
		Socket  string  `json:"socket"`
		PeerUID *uint32 `json:"peer_uid"`
		Group   *int    `json:"group"`
	} `json:"authorization"`
	PreparedRoot      string `json:"prepared_root"`
	MaxActive         int    `json:"max_active"`
	InventoryLimit    int    `json:"inventory_limit"`
	LeaseTTL          string `json:"lease_ttl"`
	PollInterval      string `json:"poll_interval"`
	EventInterval     string `json:"event_interval"`
	ActionTimeout     string `json:"action_timeout"`
	CaptureWindow     string `json:"capture_window"`
	InventoryInterval string `json:"inventory_interval"`
	IdleInterval      string `json:"idle_interval"`
	IdleThreshold     string `json:"idle_threshold"`
}

// LoadDeployment reads only exported application release metadata. Relative file
// paths resolve beside this file; no engine-owned template or runtime path is read.
func LoadDeployment(path string) (*Deployment, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("Khora deployment exceeds 1 MiB")
	}
	var wire deploymentFile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("Khora deployment: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("Khora deployment contains trailing data")
	}
	if wire.Version != 1 || strings.TrimSpace(wire.BackendName) == "" || wire.Engine.StoreID == "" || wire.MaxActive <= 0 || wire.InventoryLimit <= 0 || wire.InventoryLimit > 10000 || len(wire.Gateways) == 0 || len(wire.Gateways) > 16 {
		return nil, fmt.Errorf("version 1, backend/store identity, gateways and positive deployment bounds required")
	}
	resolve := func(value string) string {
		if value == "" {
			return ""
		}
		if filepath.IsAbs(value) {
			return filepath.Clean(value)
		}
		return filepath.Join(filepath.Dir(absolute), value)
	}
	d := &Deployment{BackendName: wire.BackendName, MaxActive: wire.MaxActive, InventoryLimit: wire.InventoryLimit, PreparedRoot: resolve(wire.PreparedRoot), AuthorizationSocket: resolve(wire.Authorization.Socket)}
	if d.PreparedRoot == "" || d.AuthorizationSocket == "" || wire.Authorization.PeerUID == nil || wire.Authorization.Group == nil || *wire.Authorization.Group < 0 {
		return nil, fmt.Errorf("prepared content root and explicit authorization socket/peer/group required")
	}
	d.AuthorizationUID, d.AuthorizationGroup = *wire.Authorization.PeerUID, *wire.Authorization.Group
	for _, p := range []struct {
		name, value string
		target      *time.Duration
	}{
		{"lease_ttl", wire.LeaseTTL, &d.LeaseTTL}, {"poll_interval", wire.PollInterval, &d.PollInterval}, {"event_interval", wire.EventInterval, &d.EventInterval}, {"action_timeout", wire.ActionTimeout, &d.ActionTimeout}, {"capture_window", wire.CaptureWindow, &d.CaptureWindow}, {"inventory_interval", wire.InventoryInterval, &d.InventoryInterval}, {"idle_interval", wire.IdleInterval, &d.IdleInterval}, {"idle_threshold", wire.IdleThreshold, &d.IdleThreshold},
	} {
		duration, err := time.ParseDuration(p.value)
		if err != nil || duration <= 0 {
			return nil, fmt.Errorf("positive %s required", p.name)
		}
		*p.target = duration
	}
	endpoint, err := url.Parse(wire.Engine.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, fmt.Errorf("engine must be an origin without credentials, path, query or fragment")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	switch endpoint.Scheme {
	case "http":
		address, err := netip.ParseAddr(endpoint.Hostname())
		if err != nil || !address.IsLoopback() {
			return nil, fmt.Errorf("plain HTTP engine must use a loopback IP literal")
		}
		if wire.Engine.CAFile != "" || wire.Engine.ClientCert != "" || wire.Engine.ClientKey != "" {
			return nil, fmt.Errorf("TLS files require an HTTPS engine")
		}
	case "https":
		if wire.Engine.CAFile == "" || wire.Engine.ClientCert == "" || wire.Engine.ClientKey == "" {
			return nil, fmt.Errorf("HTTPS engine requires CA and client certificate/key")
		}
		ca, err := os.ReadFile(resolve(wire.Engine.CAFile))
		if err != nil {
			return nil, fmt.Errorf("read engine CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("invalid engine CA")
		}
		cert, err := tls.LoadX509KeyPair(resolve(wire.Engine.ClientCert), resolve(wire.Engine.ClientKey))
		if err != nil {
			return nil, fmt.Errorf("load engine client certificate: %w", err)
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{cert}}
	default:
		return nil, fmt.Errorf("HTTP or authenticated HTTPS engine required")
	}
	// Process streams have their own lifetime contexts; a global HTTP timeout
	// would detach healthy long turns. Startup and control operations are bounded.
	d.Engine = khorabackend.Config{Endpoint: wire.Engine.Endpoint, StoreID: wire.Engine.StoreID, HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ids, sockets := map[string]bool{}, map[string]bool{d.AuthorizationSocket: true}
	var listeners []egress.Gateway
	for _, g := range wire.Gateways {
		socket := resolve(g.Socket)
		if g.ID == "" || g.PeerUID == nil || socket == "" || ids[g.ID] || sockets[socket] {
			return nil, fmt.Errorf("unique gateway IDs/sockets and explicit peer UIDs required")
		}
		if *g.PeerUID != d.AuthorizationUID {
			return nil, fmt.Errorf("gateway controls must use the configured authorization peer UID")
		}
		if g.Protocol != "model_http" && g.Protocol != "doris_mysql" && g.Protocol != "obs_http" {
			return nil, fmt.Errorf("unsupported gateway protocol")
		}
		ids[g.ID], sockets[socket] = true, true
		d.Gateways = append(d.Gateways, gateway.RemoteConfig{ID: g.ID, Socket: socket, PeerUID: *g.PeerUID, Protocol: egress.Protocol(g.Protocol), Timeout: d.ActionTimeout})
		if g.Listener != "" {
			listener, err := netip.ParseAddrPort(g.Listener)
			if err != nil || !listener.Addr().Is4() || listener.Port() == 0 {
				return nil, fmt.Errorf("gateway %q listener must be an IPv4 address and port", g.ID)
			}
			listeners = append(listeners, egress.Gateway{Endpoint: listener, Protocol: egress.Protocol(g.Protocol)})
		}
	}
	if catalog, err := serviceCatalog(wire, listeners); err != nil {
		return nil, err
	} else {
		d.Services = catalog
	}
	if wire.ContentBytes <= 0 || wire.ContentBytes > 1<<30 {
		return nil, fmt.Errorf("positive bounded content volume capacity required")
	}
	d.ContentBytes = wire.ContentBytes
	plans := map[string]execution.LaunchPlan{}
	revisions := map[string]content.Revision{}
	for driver, p := range wire.Plans {
		plan := execution.LaunchPlan{TemplateID: p.Template, CPUMillis: p.CPUMillis, MemoryBytes: p.MemoryBytes, DiskBytes: p.DiskBytes, WorkspaceBytes: p.WorkspaceBytes, JournalBytes: p.JournalBytes, Network: p.Network, Environment: p.Environment}
		for _, authored := range []struct{ purpose, root, destination string }{{"config", p.Config, "/harness-config"}, {"assets", p.Assets, "/root/.agents"}} {
			if authored.root == "" {
				continue
			}
			root := resolve(authored.root)
			revision, known := revisions[authored.purpose+"\x00"+root]
			if !known {
				// The revision is read from the reviewed tree. Two plans naming one root
				// share its identity, and a document cannot assert a digest it lacks.
				revision, err = content.Read(root)
				if err != nil {
					return nil, fmt.Errorf("read authored %s content: %w", authored.purpose, err)
				}
				revisions[authored.purpose+"\x00"+root] = revision
				d.Content = append(d.Content, AuthoredContent{Purpose: authored.purpose, Root: root, Revision: revision})
			}
			plan.Content = append(plan.Content, execution.ContentRequirement{Purpose: authored.purpose, Destination: authored.destination, RevisionDigest: revision.Digest})
		}
		// The guest's model origin is derived from the catalog, not declared per
		// plan: the URL a guest resolves and the route the engine steers must
		// describe one destination, and two independent declarations would drift.
		if origin, model, entitled := d.Services.ModelEndpoint(driver); entitled {
			if len(plan.Environment) != 0 {
				return nil, fmt.Errorf("driver %q model environment is derived from the service catalog", driver)
			}
			plan.Environment = []string{"ANTHROPIC_BASE_URL=" + origin}
			if model != "" {
				plan.Environment = append(plan.Environment, "PI_MODEL="+model)
			}
			sort.Strings(plan.Environment)
		}
		plans[driver] = plan
	}
	// Publication order must not depend on map iteration: startup publishes this
	// list, and a deterministic order keeps a failure reproducible.
	sort.Slice(d.Content, func(i, j int) bool {
		if d.Content[i].Purpose != d.Content[j].Purpose {
			return d.Content[i].Purpose < d.Content[j].Purpose
		}
		return d.Content[i].Root < d.Content[j].Root
	})
	manifest, err := os.Open(resolve(wire.Manifest))
	if err != nil {
		return nil, fmt.Errorf("open exported template manifest: %w", err)
	}
	defer manifest.Close()
	d.Plans, err = launchplan.Load(manifest, wire.ManifestDigest, plans)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Deployment) Options(backend Backend, gateways *gateway.Remotes, owner string) Options {
	return Options{Backend: backend, Plans: d.Plans, Content: d.Content, ContentBytes: d.ContentBytes, Services: d.Services, Routes: gateways, Activator: gateways, Grants: gateways, BackendName: d.BackendName, OwnerUUID: owner, PreparedRoot: d.PreparedRoot, MaxActive: d.MaxActive, InventoryLimit: d.InventoryLimit, LeaseTTL: d.LeaseTTL, PollInterval: d.PollInterval, EventInterval: d.EventInterval, ActionTimeout: d.ActionTimeout, CaptureWindow: d.CaptureWindow, InventoryInterval: d.InventoryInterval}
}

// Connect validates the pinned engine and every gateway before server mutation.
func (d *Deployment) Connect(ctx context.Context) (Backend, *gateway.Remotes, error) {
	action, cancel := context.WithTimeout(ctx, d.ActionTimeout)
	defer cancel()
	backend, err := khorabackend.New(action, d.Engine)
	if err != nil {
		return nil, nil, err
	}
	gateways, err := gateway.NewRemotes(action, d.Gateways)
	if err != nil {
		return nil, nil, err
	}
	return backend, gateways, nil
}

// serviceCatalog builds the declared service inventory. An absent services block
// is a deployment with no external service access, which is a valid and useful
// configuration; a present but unsatisfiable one must stop startup, so the
// catalog is fully compiled here rather than at the first launch that needs it.
func serviceCatalog(wire deploymentFile, listeners []egress.Gateway) (egress.Catalog, error) {
	declared := wire.Services
	empty := declared.Revision == "" && declared.SyntheticRange == "" && len(declared.Catalog) == 0 && len(declared.Policies) == 0
	if empty {
		if len(listeners) != 0 {
			return egress.Catalog{}, fmt.Errorf("gateway listeners are declared but no services are")
		}
		return egress.Catalog{}, nil
	}
	if len(listeners) == 0 {
		return egress.Catalog{}, fmt.Errorf("declared services require at least one gateway listener")
	}
	synthetic, err := netip.ParsePrefix(declared.SyntheticRange)
	if err != nil {
		return egress.Catalog{}, fmt.Errorf("service synthetic range must be a canonical IPv4 prefix")
	}
	deployment := egress.Deployment{BackendID: wire.Engine.StoreID, SyntheticRange: synthetic, Gateways: listeners}
	for _, value := range declared.Protected {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return egress.Catalog{}, fmt.Errorf("protected range %q must be a canonical IPv4 prefix", value)
		}
		deployment.Protected = append(deployment.Protected, prefix)
	}
	catalog := egress.Catalog{Revision: declared.Revision, Services: declared.Catalog, Deployment: deployment, Policies: map[string]egress.Policy{}}
	for driver, list := range declared.Policies {
		if driver != "pi" && driver != "sh" {
			return egress.Catalog{}, fmt.Errorf("service policy names unsupported driver %q", driver)
		}
		// The driver and revision come from the document's structure, never repeated
		// inside the entry, so a policy cannot claim to belong to another driver.
		catalog.Policies[driver] = egress.Policy{Revision: declared.Revision, Driver: driver, Services: list.Services, Direct: list.Direct, Resolvers: list.Resolvers}
	}
	if err := egress.ValidateCatalog(catalog); err != nil {
		return egress.Catalog{}, err
	}
	return catalog, nil
}
