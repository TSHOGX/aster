package egress

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
)

var ErrTupleExhausted = errors.New("synthetic service destination range exhausted")

type Allocator interface {
	ReserveEgressCompilation(context.Context, string, AllocationRequest) (Compilation, error)
}

// AllocationRequest describes an immutable planned execution, before networking
// or a process is started. Gateways selects a listener for each entitled service.
type AllocationRequest struct {
	SessionID, EnvironmentID, GenerationID string
	Epoch                                  uint64
	EnvironmentFence                       int64
	Policy                                 Policy
	Catalog                                []Endpoint
	Deployment                             Deployment
	Gateways                               map[string]netip.AddrPort
}

// SyntheticAddress reserves network and broadcast addresses. Each service gets
// its own IPv4 address; all ports on that address remain unavailable for reuse.
func SyntheticAddress(prefix netip.Prefix, offset uint64) (netip.Addr, error) {
	if !validSyntheticRange(prefix) {
		return netip.Addr{}, fmt.Errorf("canonical private IPv4 /8 through /30 service range required")
	}
	capacity := uint64(1) << (32 - prefix.Bits())
	if offset == 0 || offset >= capacity-1 {
		return netip.Addr{}, ErrTupleExhausted
	}
	raw := prefix.Addr().As4()
	binary.BigEndian.PutUint32(raw[:], binary.BigEndian.Uint32(raw[:])+uint32(offset))
	return netip.AddrFrom4(raw), nil
}

// AllocationBindings validates the full compiler contract using caller-reserved
// addresses. Stable binding IDs make retries independent of map/catalog order.
func AllocationBindings(p AllocationRequest, addresses []netip.Addr) ([]RuntimeBinding, error) {
	if p.SessionID == "" || p.EnvironmentID == "" || p.GenerationID == "" || p.Epoch == 0 || p.EnvironmentFence < 0 || len(p.Policy.Endpoints) > 64 || len(addresses) != len(p.Policy.Endpoints) {
		return nil, fmt.Errorf("complete planned execution and at most sixty-four endpoints required")
	}
	services := map[string]Endpoint{}
	for _, s := range p.Catalog {
		services[s.ID] = s
	}
	accesses := append([]EndpointAccess(nil), p.Policy.Endpoints...)
	sort.Slice(accesses, func(i, j int) bool { return accesses[i].EndpointID < accesses[j].EndpointID })
	if len(p.Gateways) != len(accesses) {
		return nil, fmt.Errorf("exact service gateway selection required")
	}
	var bindings []RuntimeBinding
	for i, access := range accesses {
		service, ok := services[access.EndpointID]
		if !ok {
			return nil, fmt.Errorf("allocation references unknown service")
		}
		gateway, ok := p.Gateways[service.ID]
		if !ok {
			return nil, fmt.Errorf("service gateway selection missing")
		}
		identity, _ := json.Marshal([]string{p.Deployment.BackendID, p.GenerationID, service.ID})
		id := fmt.Sprintf("route_%x", sha256.Sum256(identity))
		bindings = append(bindings, RuntimeBinding{ID: id, BackendID: p.Deployment.BackendID, GenerationID: p.GenerationID, Epoch: p.Epoch, Revision: 1, EndpointID: service.ID, Destination: netip.AddrPortFrom(addresses[i], service.Port), Gateway: gateway})
	}
	if _, err := Compile(p.Policy, p.Catalog, bindings, p.Deployment); err != nil {
		return nil, err
	}
	return bindings, nil
}
