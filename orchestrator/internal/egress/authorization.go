package egress

import (
	"errors"
	"net/netip"
	"time"
)

var ErrDenied = errors.New("egress request is not authorized")
var ErrRequestConflict = errors.New("egress request identity reused with different intent")

type AuthorizationRequest struct {
	// Destination comes from trusted ingress, never a guest header or source IP.
	BackendID   string         `json:"backend_id"`
	Destination netip.AddrPort `json:"destination"`
	ServiceID   string         `json:"service_id"`
	Operation   string         `json:"operation"`
	RequestID   string         `json:"request_id"`
	Model       string         `json:"model,omitempty"`
	Database    string         `json:"database,omitempty"`
	Bucket      string         `json:"bucket,omitempty"`
	ObjectKey   string         `json:"object_key,omitempty"`
	Now         time.Time      `json:"-"`
}
type Authorization struct {
	RequestContext
	PolicyDigest string    `json:"policy_digest"`
	ExpiresAt    time.Time `json:"expires_at"`
	Service      Service   `json:"service"`
}
type RequestFinish struct {
	RequestID string    `json:"request_id"`
	BackendID string    `json:"backend_id"`
	Outcome   string    `json:"outcome"`
	Bytes     int64     `json:"bytes"`
	Now       time.Time `json:"-"`
}

func AllowsResource(service Service, request AuthorizationRequest) bool {
	if !operationAllowed(service, request.Operation) {
		return false
	}
	switch service.Protocol {
	case ModelHTTP:
		return service.Model != nil && contains(service.Model.Models, request.Model)
	case DorisMySQL:
		return service.Doris != nil && request.Database != "" && (request.Database == "information_schema" || contains(service.Doris.Databases, request.Database))
	case OBSHTTP:
		if service.OBS == nil || !contains(service.OBS.Buckets, request.Bucket) {
			return false
		}
		if request.Operation == "list" && request.ObjectKey == "" {
			return len(service.OBS.Prefixes) == 0
		}
		if len(service.OBS.Prefixes) == 0 {
			return true
		}
		for _, prefix := range service.OBS.Prefixes {
			if len(request.ObjectKey) >= len(prefix) && request.ObjectKey[:len(prefix)] == prefix {
				return true
			}
		}
		return false
	case WebHTTP:
		return true
	}
	return false
}
func contains(values []string, want string) bool {
	if want == "" {
		return false
	}
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
