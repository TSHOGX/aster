package retainedapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"harness-platform/orchestrator/internal/content"
	"harness-platform/orchestrator/internal/egress"
	"harness-platform/orchestrator/internal/engineconfig"
	"harness-platform/orchestrator/internal/execution"
	"harness-platform/orchestrator/internal/gateway"
	"harness-platform/orchestrator/internal/launchplan"
	khorabackend "harness-platform/orchestrator/internal/sandbox/khora"
)

type Deployment struct {
	BackendName                                                                            string
	Engine                                                                                 khorabackend.Config
	Gateways                                                                               []gateway.RemoteConfig
	Plans                                                                                  *launchplan.Source
	Connectivity                                                                           egress.Catalog
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
	Version        int                     `json:"version"`
	BackendName    string                  `json:"backend_name"`
	Engine         engineconfig.Connection `json:"engine"`
	Manifest       string                  `json:"manifest"`
	ManifestDigest string                  `json:"manifest_digest"`
	Plans          map[string]struct {
		Template       string `json:"template"`
		CPUMillis      int64  `json:"cpu_millis"`
		MemoryBytes    int64  `json:"memory_bytes"`
		DiskBytes      int64  `json:"disk_bytes"`
		WorkspaceBytes int64  `json:"workspace_bytes"`
		JournalBytes   int64  `json:"journal_bytes"`
		Config         string `json:"config"`
		Assets         string `json:"assets"`
	} `json:"plans"`
	ContentBytes int64 `json:"content_bytes"`
	Gateways     []struct {
		ID      string  `json:"id"`
		Socket  string  `json:"socket"`
		PeerUID *uint32 `json:"peer_uid"`
		// Listener is where this gateway accepts steered guest traffic. It is the
		// endpoint the engine's route sends packets to, distinct from the Unix
		// control socket the application reaches it on.
		Listener string `json:"listener"`
	} `json:"gateways"`
	// Connectivity declares opaque destinations and per-driver reachability. It is
	// product authority; guest content never contributes to it. A deployment may
	// omit it entirely, and every driver then runs without network access.
	Connectivity struct {
		Revision       string                         `json:"revision"`
		SyntheticRange string                         `json:"synthetic_range"`
		Protected      []string                       `json:"protected"`
		Catalog        []egress.Endpoint              `json:"catalog"`
		Policies       map[string]egress.EndpointList `json:"policies"`
	} `json:"connectivity"`
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
	if wire.Version != 1 || strings.TrimSpace(wire.BackendName) == "" || wire.Engine.StoreID == "" || wire.MaxActive <= 0 || wire.InventoryLimit <= 0 || wire.InventoryLimit > 10000 || len(wire.Gateways) > 16 {
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
	if d.PreparedRoot == "" {
		return nil, fmt.Errorf("prepared content root required")
	}
	if len(wire.Gateways) != 0 || d.AuthorizationSocket != "" || wire.Authorization.PeerUID != nil || wire.Authorization.Group != nil {
		if d.AuthorizationSocket == "" || wire.Authorization.PeerUID == nil || wire.Authorization.Group == nil || *wire.Authorization.Group < 0 {
			return nil, fmt.Errorf("gateway authorization requires explicit socket, peer UID and group")
		}
		d.AuthorizationUID, d.AuthorizationGroup = *wire.Authorization.PeerUID, *wire.Authorization.Group
	}

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
	if d.LeaseTTL < time.Minute {
		return nil, fmt.Errorf("lease_ttl must be at least 1m to cover the retained guest heartbeat")
	}
	d.Engine, err = wire.Engine.Build(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}

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
		ids[g.ID], sockets[socket] = true, true
		d.Gateways = append(d.Gateways, gateway.RemoteConfig{ID: g.ID, Socket: socket, PeerUID: *g.PeerUID, Timeout: d.ActionTimeout})
		if g.Listener != "" {
			listener, err := netip.ParseAddrPort(g.Listener)
			if err != nil || !listener.Addr().Is4() || listener.Port() == 0 {
				return nil, fmt.Errorf("gateway %q listener must be an IPv4 address and port", g.ID)
			}
			listeners = append(listeners, egress.Gateway{ID: g.ID, Endpoint: listener})
		}
	}
	if catalog, err := connectivityCatalog(wire, listeners); err != nil {
		return nil, err
	} else {
		d.Connectivity = catalog
	}
	if wire.ContentBytes <= 0 || wire.ContentBytes > 1<<30 {
		return nil, fmt.Errorf("positive bounded content volume capacity required")
	}
	d.ContentBytes = wire.ContentBytes
	plans := map[string]execution.LaunchPlan{}
	revisions := map[string]content.Revision{}
	for driver, p := range wire.Plans {
		plan := execution.LaunchPlan{TemplateID: p.Template, CPUMillis: p.CPUMillis, MemoryBytes: p.MemoryBytes, DiskBytes: p.DiskBytes, WorkspaceBytes: p.WorkspaceBytes, JournalBytes: p.JournalBytes}
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
	return Options{Backend: backend, Plans: d.Plans, Content: d.Content, ContentBytes: d.ContentBytes, Connectivity: d.Connectivity, Routes: gateways, Activator: gateways, Grants: gateways, GatewayMaintenance: gateways, BackendName: d.BackendName, OwnerUUID: owner, PreparedRoot: d.PreparedRoot, MaxActive: d.MaxActive, InventoryLimit: d.InventoryLimit, LeaseTTL: d.LeaseTTL, PollInterval: d.PollInterval, EventInterval: d.EventInterval, ActionTimeout: d.ActionTimeout, CaptureWindow: d.CaptureWindow, InventoryInterval: d.InventoryInterval}
}

// Connect validates the engine and configures lazy, destination-scoped gateway clients.
func (d *Deployment) Connect(ctx context.Context, directory gateway.RouteDirectory) (Backend, *gateway.Remotes, error) {
	action, cancel := context.WithTimeout(ctx, d.ActionTimeout)
	defer cancel()
	backend, err := khorabackend.New(action, d.Engine)
	if err != nil {
		return nil, nil, err
	}
	gateways, err := gateway.NewRemotes(action, d.Gateways, directory)
	if err != nil {
		return nil, nil, err
	}
	return backend, gateways, nil
}

// connectivityCatalog builds the declared service inventory. An absent services block
// is a deployment with no external service access, which is a valid and useful
// configuration; a present but unsatisfiable one must stop startup, so the
// catalog is fully compiled here rather than at the first launch that needs it.
func connectivityCatalog(wire deploymentFile, listeners []egress.Gateway) (egress.Catalog, error) {
	declared := wire.Connectivity
	empty := declared.Revision == "" && declared.SyntheticRange == "" && len(declared.Catalog) == 0 && len(declared.Policies) == 0 && len(declared.Protected) == 0
	if empty {
		if len(listeners) != 0 {
			return egress.Catalog{}, fmt.Errorf("gateway listeners are declared but no services are")
		}
		return egress.Catalog{}, nil
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
	catalog := egress.Catalog{Revision: declared.Revision, Endpoints: declared.Catalog, Deployment: deployment, Policies: map[string]egress.Policy{}}
	for driver, list := range declared.Policies {
		if driver != "pi" && driver != "sh" {
			return egress.Catalog{}, fmt.Errorf("service policy names unsupported driver %q", driver)
		}
		// The driver and revision come from the document's structure, never repeated
		// inside the entry, so a policy cannot claim to belong to another driver.
		catalog.Policies[driver] = egress.Policy{Revision: declared.Revision, Driver: driver, Endpoints: list.Endpoints, Direct: list.Direct, Resolvers: list.Resolvers}
	}
	if err := egress.ValidateCatalog(catalog); err != nil {
		return egress.Catalog{}, err
	}
	return catalog, nil
}
