package egress

import (
	"errors"
	"net/netip"
	"time"
)

var ErrDenied = errors.New("egress request is not authorized")
var ErrRequestConflict = errors.New("egress request identity reused with different intent")

type AuthorizationRequest struct {
	// Destination is supplied by trusted ingress from the accepted socket's
	// original destination, never from a guest header or source IP.
	BackendID                          string
	Destination                        netip.AddrPort
	ServiceID, Operation, RequestID    string
	Model, Database, Bucket, ObjectKey string
	Now                                time.Time
}
type Authorization struct {
	RequestContext
	PolicyDigest string
	ExpiresAt    time.Time
	Service      Service
}
type RequestFinish struct {
	RequestID, BackendID, Outcome string
	Bytes                         int64
	Now                           time.Time
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
