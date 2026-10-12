package servicegateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"harness-platform/orchestrator/internal/product"
)

type rejectingPlatform struct{ err error }

func (p rejectingPlatform) PlatformSchedules(context.Context, product.ServiceRequest, product.PlatformScheduleRequest) (product.PlatformScheduleResult, error) {
	return product.PlatformScheduleResult{}, p.err
}
func TestPlatformQuotaAndAuthorityErrorsSurviveOwnerTransport(t *testing.T) {
	quota := &product.QuotaError{Dimension: product.ResourceSchedules, Used: 3, RequestedDelta: 1, Limit: 3}
	for _, cause := range []error{quota, product.ErrForbidden, product.ErrUnauthorized, product.ErrCapacity} {
		t.Run(cause.Error(), func(t *testing.T) {
			server := httptest.NewServer(PlatformHandler(rejectingPlatform{err: cause}, map[string]bool{"model": true}))
			defer server.Close()
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
			}}
			defer transport.CloseIdleConnections()
			remote := &RemoteAuthority{audience: "model", instance: "gateway", transport: transport}
			_, err := remote.PlatformSchedules(context.Background(), product.ServiceRequest{Audience: "model", Instance: "gateway", Slot: 0, Sequence: 1}, product.PlatformScheduleRequest{Action: "context"})
			if cause == quota {
				var result *product.QuotaError
				if !errors.As(err, &result) || *result != *quota {
					t.Fatal("quota details lost", err)
				}
			} else if !errors.Is(err, cause) {
				t.Fatal("authority error remapped", cause, err)
			}
		})
	}
}
