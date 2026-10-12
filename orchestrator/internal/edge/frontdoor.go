package edge

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// FrontDoor is explicit deployment trust, never inferred from forwarded headers.
// The browser listener must be private when this policy is configured. Only the
// configured peer may supply an original client address; origin comes from config.
type FrontDoor struct {
	PublicOrigin string `json:"public_origin"`
	PeerIP       string `json:"peer_ip"`
}

func (f *FrontDoor) Validate() error {
	if f == nil {
		return nil
	}
	u, err := url.Parse(f.PublicOrigin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("front_door.public_origin must be an HTTPS origin without path, credentials, query or fragment")
	}
	ip, err := netip.ParseAddr(f.PeerIP)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Zone() != "" {
		return fmt.Errorf("front_door.peer_ip must be a specific IP address")
	}
	return nil
}

func (f *FrontDoor) accepts(r *http.Request) bool {
	if f == nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Unmap() != netip.MustParseAddr(f.PeerIP).Unmap() {
		return false
	}
	u, _ := url.Parse(f.PublicOrigin)
	if !strings.EqualFold(r.Host, u.Host) {
		return false
	}
	client, err := netip.ParseAddr(r.Header.Get("X-Real-IP"))
	return err == nil && !client.IsUnspecified() && !client.IsMulticast() && client.Zone() == ""
}
