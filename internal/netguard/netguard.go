// Package netguard provides the outbound network boundary for administrator-
// configured destinations. DNS is resolved again at dial time so a hostname
// cannot be changed to an internal address after it is saved.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errBlockedAddress = errors.New("outbound address is not allowed")

// ValidateURL performs cheap validation suitable for an admin save endpoint.
// The resolver is intentionally not consulted here; Dialer performs the
// authoritative DNS check immediately before connecting.
func ValidateURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid outbound URL")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	if host == "" || (ip == nil && blockedHostname(host)) {
		return errBlockedAddress
	}
	if port := u.Port(); port != "" {
		if err := ValidatePort(port); err != nil {
			return err
		}
	}
	if ip != nil && blockedIP(ip) {
		return errBlockedAddress
	}
	return nil
}

func ValidateAddress(raw string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil || host == "" {
		return errors.New("invalid outbound address")
	}
	if err := ValidatePort(port); err != nil {
		return err
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	ip := net.ParseIP(host)
	if strings.Contains(host, ":") && ip == nil {
		return errors.New("invalid outbound host")
	}
	if (ip == nil && blockedHostname(host)) || (ip != nil && blockedIP(ip)) {
		return errBlockedAddress
	}
	return nil
}

func ValidatePort(raw string) error {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 65535 {
		return errors.New("invalid outbound port")
	}
	return nil
}

// NewHTTPClient returns a redirect-free client with guarded dialing. It is
// used for destinations configured by an administrator.
func NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&Dialer{Timeout: timeout}).DialContext
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

type Dialer struct {
	Timeout  time.Duration
	Resolver *net.Resolver
}

func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid outbound address: %w", err)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(host)
	if ip == nil && blockedHostname(host) {
		return nil, errBlockedAddress
	}
	resolver := d.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	var ips []net.IP
	if ip != nil {
		ips = []net.IP{ip}
	} else {
		addresses, lookupErr := resolver.LookupIPAddr(ctx, host)
		if lookupErr != nil {
			return nil, fmt.Errorf("resolve outbound address: %w", lookupErr)
		}
		for _, candidate := range addresses {
			ips = append(ips, candidate.IP)
		}
	}
	if len(ips) == 0 {
		return nil, errBlockedAddress
	}
	dialer := net.Dialer{Timeout: d.Timeout, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if blockedIP(ip) {
			return nil, errBlockedAddress
		}
		if network == "tcp4" && ip.To4() == nil {
			continue
		}
		if network == "tcp6" && ip.To4() != nil {
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errBlockedAddress
}

func blockedHostname(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || host == "metadata.google.internal" || host == "metadata" || !strings.Contains(host, ".") {
		return true
	}
	return strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local")
}

func blockedIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// Carrier-grade NAT is not globally routable and is commonly used for
	// internal services. Cloud metadata is covered by link-local above, but the
	// explicit ranges make the intent clear and protect unusual net.IP forms.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		if v4[0] == 169 && v4[1] == 254 && v4[2] == 169 && v4[3] == 254 {
			return true
		}
	}
	return false
}
