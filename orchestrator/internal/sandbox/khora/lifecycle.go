// Package khora translates the public engine APIs into application-owned ports.
package khora

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	sdk "github.com/TSHOGX/khora/pkg/khora"
	"harness-platform/orchestrator/internal/sandbox"
)

const EngineRevision = "e8cd021a9c7c"

type Config struct {
	Endpoint, StoreID, Version string
	HTTPClient                 *http.Client
}

type Backend struct {
	client     *sdk.Client
	identity   sandbox.Identity
	endpoint   string
	httpClient *http.Client
}

func New(ctx context.Context, config Config) (*Backend, error) {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, failure("configure", sandbox.Invalid, fmt.Errorf("absolute engine origin without credentials or path required"))
	}
	if parsed.Scheme == "http" {
		address, err := netip.ParseAddr(parsed.Hostname())
		if parsed.Hostname() != "localhost" && (err != nil || !address.IsLoopback()) {
			return nil, failure("configure", sandbox.Invalid, fmt.Errorf("remote engine access requires an authenticated TLS gateway"))
		}
	} else if parsed.Scheme != "https" {
		return nil, failure("configure", sandbox.Invalid, fmt.Errorf("HTTP or HTTPS engine origin required"))
	}
	if config.Version == "" {
		config.Version = EngineRevision
	}
	if config.Version != EngineRevision {
		return nil, failure("configure", sandbox.Incompatible, fmt.Errorf("unsupported engine build revision"))
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	client, err := sdk.New(strings.TrimRight(config.Endpoint, "/"), sdk.WithHTTPClient(httpClient))
	if err != nil {
		return nil, err
	}
	backend := &Backend{client: client, identity: sandbox.Identity{StoreID: config.StoreID, Version: config.Version}, endpoint: strings.TrimRight(config.Endpoint, "/"), httpClient: httpClient}
	observed, err := backend.Identity(ctx)
	if err != nil {
		return nil, err
	}
	backend.identity = observed
	return backend, nil
}

func (b *Backend) Identity(ctx context.Context) (sandbox.Identity, error) {
	info, err := b.client.Info(ctx)
	if err != nil {
		return sandbox.Identity{}, mapError("identity", err)
	}
	observed := sandbox.Identity{StoreID: info.StoreID, Version: info.Version, ManualColdRecovery: info.ManualColdRecovery}
	if info.StoreID == "" {
		return sandbox.Identity{}, failure("identity", sandbox.Incompatible, fmt.Errorf("engine omitted durable store identity"))
	}
	if b.identity.StoreID != "" && b.identity.StoreID != info.StoreID {
		return sandbox.Identity{}, failure("identity", sandbox.IdentityChanged, fmt.Errorf("engine durable store was replaced"))
	}
	if info.Version != b.identity.Version || !info.ManualColdRecovery {
		return sandbox.Identity{}, failure("identity", sandbox.Incompatible, fmt.Errorf("engine build and manual cold recovery must match deployment contract"))
	}
	return observed, nil
}

func (b *Backend) Create(ctx context.Context, request sandbox.CreateRequest) (sandbox.Observation, error) {
	if request.IdempotencyKey == "" || request.TemplateID == "" || request.CPUMillis <= 0 || request.MemoryBytes <= 0 || request.DiskBytes <= 0 {
		return sandbox.Observation{}, failure("create", sandbox.Invalid, fmt.Errorf("key, template and positive resource caps required"))
	}
	network, err := networkDeclaration(request.Network)
	if err != nil {
		return sandbox.Observation{}, err
	}
	mounts, err := volumeBindings(request.Mounts)
	if err != nil {
		return sandbox.Observation{}, err
	}
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.Observation{}, err
	}
	response, err := b.client.CreateWithKey(ctx, sdk.CreateRequest{TemplateID: request.TemplateID, IdleTimeout: "0s", Retention: "0s", CPUMillis: request.CPUMillis, MemoryByte: request.MemoryBytes, DiskByte: request.DiskBytes, Volumes: mounts, Network: &network}, sdk.IdempotencyKey(request.IdempotencyKey))
	if err != nil {
		return sandbox.Observation{}, mapError("create", err)
	}
	if response.SandboxID == "" {
		return sandbox.Observation{}, failure("create", sandbox.Uncertain, fmt.Errorf("engine omitted created sandbox identity"))
	}
	// Replayed create responses can name an already-deleted object. Only a
	// current inspection can establish the binding and placement identity.
	observed, err := b.Inspect(ctx, sandbox.Reference{StoreID: b.identity.StoreID, SandboxID: response.SandboxID})
	if err != nil {
		return sandbox.Observation{}, err
	}
	if observed.State != "running" || observed.PlacementState != "active" {
		return observed, failure("create", sandbox.Uncertain, fmt.Errorf("created sandbox has no active placement"))
	}
	return observed, nil
}

func (b *Backend) Inspect(ctx context.Context, reference sandbox.Reference) (sandbox.Observation, error) {
	if err := b.validateReference(ctx, reference, false); err != nil {
		return sandbox.Observation{}, err
	}
	current, err := b.client.Get(ctx, reference.SandboxID)
	if err != nil {
		return sandbox.Observation{}, mapError("inspect", err)
	}
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.Observation{}, err
	}
	if current.SandboxID != reference.SandboxID || current.PlacementEpoch == 0 || current.MutationRevision == "" {
		return sandbox.Observation{}, failure("inspect", sandbox.Uncertain, fmt.Errorf("incomplete engine observation"))
	}
	return sandbox.Observation{Reference: sandbox.Reference{StoreID: b.identity.StoreID, SandboxID: current.SandboxID, MutationRevision: current.MutationRevision, Epoch: current.PlacementEpoch}, State: string(current.State), PlacementState: current.PlacementState, DeathCause: current.DeathCause, DiskBytes: current.UsedByte}, nil
}

func (b *Backend) Pause(ctx context.Context, reference sandbox.Reference) (sandbox.Observation, error) {
	if err := b.validateReference(ctx, reference, true); err != nil {
		return sandbox.Observation{}, err
	}
	if _, err := b.client.PauseIfMatch(ctx, reference.SandboxID, reference.MutationRevision); err != nil {
		return sandbox.Observation{}, mapError("pause", err)
	}
	observed, err := b.Inspect(ctx, reference)
	if err != nil {
		return observed, err
	}
	if observed.State != "paused" || observed.PlacementState != "lost" || observed.Epoch != reference.Epoch {
		return observed, failure("pause", sandbox.Uncertain, fmt.Errorf("previous writer termination was not established"))
	}
	return observed, nil
}

func (b *Backend) Resume(ctx context.Context, reference sandbox.Reference) (sandbox.Observation, error) {
	if err := b.validateReference(ctx, reference, true); err != nil {
		return sandbox.Observation{}, err
	}
	if _, err := b.client.ResumeIfMatch(ctx, reference.SandboxID, reference.MutationRevision); err != nil {
		return sandbox.Observation{}, mapError("resume", err)
	}
	observed, err := b.Inspect(ctx, reference)
	if err != nil {
		return observed, err
	}
	if observed.State != "running" || observed.PlacementState != "active" || observed.Epoch <= reference.Epoch {
		return observed, failure("resume", sandbox.Uncertain, fmt.Errorf("new placement was not established"))
	}
	return observed, nil
}

func (b *Backend) Configure(ctx context.Context, reference sandbox.Reference, network sandbox.Network, mounts []sandbox.Mount) (sandbox.Observation, error) {
	if err := b.validateReference(ctx, reference, true); err != nil {
		return sandbox.Observation{}, err
	}
	declaration, err := networkDeclaration(network)
	if err != nil {
		return sandbox.Observation{}, err
	}
	bindings, err := volumeBindings(mounts)
	if err != nil {
		return sandbox.Observation{}, err
	}
	zero := "0s"
	_, patchErr := b.client.PatchIfMatch(ctx, reference.SandboxID, reference.MutationRevision, sdk.PatchRequest{Network: &declaration, Volumes: &bindings, IdleTimeout: &zero, Retention: &zero})
	mapped := mapError("configure", patchErr)
	if patchErr != nil && !sandbox.Is(mapped, sandbox.Precondition) {
		return sandbox.Observation{}, mapped
	}
	observed, err := b.InspectConfiguration(ctx, reference)
	if err != nil {
		return sandbox.Observation{}, err
	}
	if observed.State != "paused" || observed.PlacementState != "lost" || observed.Epoch != reference.Epoch || !sandbox.SameConfiguration(observed, network, mounts) {
		if patchErr != nil {
			return observed.Observation, mapped
		}
		return observed.Observation, failure("configure", sandbox.Uncertain, fmt.Errorf("observed binding differs from requested paused configuration"))
	}
	// A 412 retry may follow a lost successful PATCH response. Only the exact
	// complete declaration at the original epoch settles it; never refresh the
	// validator and redispatch a stale replacement.
	return observed.Observation, nil
}

func (b *Backend) CreateVolume(ctx context.Context, key string, capacity int64) (sandbox.Volume, error) {
	if key == "" || capacity <= 0 {
		return sandbox.Volume{}, failure("create volume", sandbox.Invalid, fmt.Errorf("key and positive volume capacity required"))
	}
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.Volume{}, err
	}
	created, err := b.client.CreateVolumeWithKey(ctx, capacity, sdk.IdempotencyKey(key))
	if err != nil {
		return sandbox.Volume{}, mapError("create volume", err)
	}
	if created.VolumeID == "" {
		return sandbox.Volume{}, failure("create volume", sandbox.Uncertain, fmt.Errorf("engine omitted volume identity"))
	}
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.Volume{}, err
	}
	volume, err := b.client.Volume(ctx, created.VolumeID)
	if err != nil {
		return sandbox.Volume{}, mapError("inspect volume", err)
	}
	if _, err := b.Identity(ctx); err != nil {
		return sandbox.Volume{}, err
	}
	if volume.VolumeID != created.VolumeID || volume.Size != capacity {
		return sandbox.Volume{}, failure("inspect volume", sandbox.Uncertain, fmt.Errorf("volume observation does not match intent"))
	}
	return sandbox.Volume{VolumeReference: sandbox.VolumeReference{StoreID: b.identity.StoreID, VolumeID: volume.VolumeID}, Capacity: volume.Size, UsedBytes: volume.UsedByte}, nil
}

func (b *Backend) validateReference(ctx context.Context, reference sandbox.Reference, mutation bool) error {
	if reference.StoreID != b.identity.StoreID {
		return failure("reference", sandbox.IdentityChanged, fmt.Errorf("reference belongs to another durable store"))
	}
	if reference.SandboxID == "" || strings.ContainsAny(reference.SandboxID, "/\\?#") {
		return failure("reference", sandbox.Invalid, fmt.Errorf("opaque sandbox identity required"))
	}
	if mutation && (reference.MutationRevision == "" || reference.Epoch == 0) {
		return failure("reference", sandbox.Invalid, fmt.Errorf("observed revision and epoch required for mutation"))
	}
	_, err := b.Identity(ctx)
	return err
}

func networkDeclaration(network sandbox.Network) (sdk.Network, error) {
	deny := "deny"
	result := sdk.Network{Egress: sdk.Egress{Default: &deny}}
	tuple := func(rule sandbox.Tuple) (sdk.Tuple, error) {
		if !rule.CIDR.IsValid() || !rule.CIDR.Addr().Is4() || rule.Port == 0 || (rule.Protocol != "tcp" && rule.Protocol != "udp") {
			return sdk.Tuple{}, failure("network", sandbox.Invalid, fmt.Errorf("explicit IPv4 prefix, port and protocol required"))
		}
		return sdk.Tuple{CIDR: rule.CIDR.Masked().String(), Port: rule.Port, Proto: rule.Protocol}, nil
	}
	for _, rule := range network.Allow {
		value, err := tuple(rule)
		if err != nil {
			return result, err
		}
		result.Egress.Allow = append(result.Egress.Allow, value)
	}
	for _, route := range network.Via {
		match, err := tuple(route.Match)
		if err != nil {
			return result, err
		}
		if match.Proto != "tcp" || route.Match.CIDR.Bits() != 32 || !route.Endpoint.IsValid() || !route.Endpoint.Addr().Is4() || route.Endpoint.Addr().IsLoopback() || route.Endpoint.Port() == 0 {
			return result, failure("network", sandbox.Invalid, fmt.Errorf("exact TCP route and non-loopback gateway required"))
		}
		result.Egress.Via = append(result.Egress.Via, sdk.Via{Match: []sdk.Tuple{match}, Endpoint: route.Endpoint.String()})
	}
	return result, nil
}

func volumeBindings(mounts []sandbox.Mount) ([]sdk.VolumeBinding, error) {
	result := make([]sdk.VolumeBinding, 0, len(mounts))
	for _, mount := range mounts {
		if mount.VolumeID == "" || mount.Destination == "" {
			return nil, failure("mount", sandbox.Invalid, fmt.Errorf("volume and guest destination required"))
		}
		switch mount.Access {
		case "exclusive", "shared-ro", "shared-rw":
		default:
			return nil, failure("mount", sandbox.Invalid, fmt.Errorf("invalid volume access mode"))
		}
		result = append(result, sdk.VolumeBinding{VolumeID: mount.VolumeID, Path: mount.Destination, Access: mount.Access})
	}
	return result, nil
}

func failure(operation string, class sandbox.ErrorClass, cause error) error {
	return &sandbox.Error{Operation: operation, Class: class, Cause: cause}
}
func mapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return failure(operation, sandbox.Unavailable, err)
	}
	var remote *sdk.Error
	if errors.As(err, &remote) {
		class := sandbox.Unavailable
		switch remote.Code {
		case http.StatusBadRequest:
			class = sandbox.Invalid
		case http.StatusNotFound:
			class = sandbox.NotFound
		case http.StatusConflict:
			class = sandbox.Conflict
		case http.StatusPreconditionFailed:
			class = sandbox.Precondition
		case http.StatusInsufficientStorage:
			class = sandbox.Capacity
		case http.StatusGone:
			class = sandbox.CursorExpired
		}
		return failure(operation, class, err)
	}
	return failure(operation, sandbox.Unavailable, err)
}

var _ sandbox.Lifecycle = (*Backend)(nil)
