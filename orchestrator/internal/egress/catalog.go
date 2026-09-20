package egress

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Catalog is the deployment's declared service inventory together with the
// per-driver entitlements and the network boundary every compilation is checked
// against. It is product authority: guest content never contributes to it.
//
// It exists as its own type so the daemon has one validated object to hold, and
// so validation happens once at load rather than per generation.
type Catalog struct {
	Revision   string
	Endpoints  []Endpoint
	Policies   map[string]Policy
	Deployment Deployment
}

// Endpoints reports which listener serves each entitled service for one driver.
// Compile requires an exact selection, so this is derived from the catalog rather
// than supplied per generation.
func (c Catalog) NextHops(driver string) (map[string]netip.AddrPort, error) {
	policy, ok := c.Policies[driver]
	if !ok {
		return nil, fmt.Errorf("driver %q has no declared service policy", driver)
	}
	services := map[string]Endpoint{}
	for _, service := range c.Endpoints {
		services[service.ID] = service
	}
	listeners := map[string]netip.AddrPort{}
	for _, gateway := range c.Deployment.Gateways {
		listeners[gateway.ID] = gateway.Endpoint
	}
	result := map[string]netip.AddrPort{}
	for _, access := range policy.Endpoints {
		service, ok := services[access.EndpointID]
		if !ok {
			return nil, fmt.Errorf("driver %q is entitled to undeclared service %q", driver, access.EndpointID)
		}
		endpoint, ok := listeners[service.NextHop]
		if !ok {
			return nil, fmt.Errorf("no %s gateway serves service %q", service.NextHop, service.ID)
		}
		result[service.ID] = endpoint
	}
	return result, nil
}

// Policy returns one driver's entitlements, or reports that it has none. A driver
// with no policy runs with no service access rather than an implicit default.
func (c Catalog) Policy(driver string) (Policy, bool) {
	policy, ok := c.Policies[driver]
	return policy, ok
}

// ValidateCatalog checks the whole declaration once, by compiling every driver's
// policy against synthetic placeholder destinations. A catalog that cannot
// compile must fail startup rather than fail the first launch that needs it.
func ValidateCatalog(c Catalog) error {
	if strings.TrimSpace(c.Revision) == "" {
		return fmt.Errorf("service catalog revision required")
	}
	if len(c.Endpoints) > 64 {
		return fmt.Errorf("at most sixty-four declared endpoints required")
	}
	ids := map[string]bool{}
	hostnames := map[string]bool{}
	for _, service := range c.Endpoints {
		normalized, err := normalizeEndpoint(service)
		if err != nil {
			return err
		}
		if ids[normalized.ID] {
			return fmt.Errorf("duplicate declared service %q", normalized.ID)
		}
		if hostnames[normalized.Hostname] {
			return fmt.Errorf("duplicate declared guest hostname %q", normalized.Hostname)
		}
		ids[normalized.ID], hostnames[normalized.Hostname] = true, true
	}
	if len(c.Policies) == 0 {
		return fmt.Errorf("at least one driver service policy required")
	}
	drivers := make([]string, 0, len(c.Policies))
	for driver := range c.Policies {
		drivers = append(drivers, driver)
	}
	sort.Strings(drivers)
	for _, driver := range drivers {
		policy := c.Policies[driver]
		if policy.Driver != driver {
			return fmt.Errorf("service policy for %q declares driver %q", driver, policy.Driver)
		}
		if policy.Revision != c.Revision {
			return fmt.Errorf("service policy for %q declares a different catalog revision", driver)
		}
		endpoints, err := c.NextHops(driver)
		if err != nil {
			return err
		}
		// Compile with placeholder destinations so an unsatisfiable entitlement,
		// a protocol mismatch or a protected-range overlap is caught at load.
		request := AllocationRequest{
			SessionID: "validation", EnvironmentID: "validation", GenerationID: "validation", Epoch: 1,
			Policy: policy, Catalog: c.Endpoints, Deployment: c.Deployment, Gateways: endpoints,
		}
		var placeholders []netip.Addr
		for i := range policy.Endpoints {
			address, err := SyntheticAddress(c.Deployment.SyntheticRange, uint64(i+1))
			if err != nil {
				return fmt.Errorf("declared policy for %q does not fit the synthetic range: %w", driver, err)
			}
			placeholders = append(placeholders, address)
		}
		bindings, err := AllocationBindings(request, placeholders)
		if err != nil {
			return fmt.Errorf("declared policy for %q is not compilable: %w", driver, err)
		}
		if _, err := Compile(policy, c.Endpoints, bindings, c.Deployment); err != nil {
			return fmt.Errorf("declared policy for %q is not compilable: %w", driver, err)
		}
	}
	return nil
}

// EndpointList is one driver's declared entitlements as the deployment document
// spells them: which services, and which operations on each. The policy's driver
// and catalog revision are supplied by the catalog, not repeated per entry, so a
// document cannot declare a policy for one driver under another's key.
type EndpointList struct {
	Endpoints []EndpointAccess `json:"endpoints"`
	Direct    []Tuple          `json:"direct,omitempty"`
	Resolvers []netip.Addr     `json:"resolvers,omitempty"`
}
