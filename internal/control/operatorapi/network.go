package operatorapi

import (
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/selfdrivingjazz/cuelight/internal/control/auth"
)

const (
	// TrustedNetworkActorID is the built-in actor every token-less request from
	// a trusted network authenticates as. It is stable so audit and created_by
	// references survive restarts and configuration changes.
	TrustedNetworkActorID   = "00000000-0000-4000-8000-7275737465ff"
	TrustedNetworkActorName = "Trusted network"

	AuthMethodToken   = "token"
	AuthMethodNetwork = "network"
)

// AllScopes is every operator scope; a trusted-network actor holds all of them.
var AllScopes = []string{
	ScopeSecurityAdmin, ScopeNodeEnroll, ScopeNodeRead, ScopeNodeWrite,
	ScopeAssignmentRead, ScopeAssignmentWrite, ScopeMediaRead, ScopeMediaWrite,
	ScopeLocalMediaRead, ScopeReleaseRead, ScopeReleaseWrite,
}

// NetworkTrust admits operator requests that carry no Authorization header
// when the TCP peer is inside one of Networks. The zero value trusts nothing.
type NetworkTrust struct {
	networks []netip.Prefix
	// origin is the only browser Origin accepted on network-trusted requests;
	// empty means the request's own scheme and Host.
	origin string
}

// NewNetworkTrust builds the trust policy. publicURL is control's public HTTPS
// URL (may be empty); when set, its origin is the only accepted browser Origin.
func NewNetworkTrust(networks []netip.Prefix, publicURL string) (NetworkTrust, error) {
	trust := NetworkTrust{networks: make([]netip.Prefix, len(networks))}
	for index, network := range networks {
		trust.networks[index] = network.Masked()
	}
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return NetworkTrust{}, fmt.Errorf("public URL %q has no origin", publicURL)
		}
		trust.origin = origin(parsed.Scheme, parsed.Host)
	}
	return trust, nil
}

// Enabled reports whether any network is trusted.
func (t NetworkTrust) Enabled() bool { return len(t.networks) != 0 }

// peer returns the request's TCP peer address if it is trusted. RemoteAddr is
// the connection's address; forwarding headers are deliberately ignored.
func (t NetworkTrust) peer(r *http.Request) (netip.Addr, bool) {
	if len(t.networks) == 0 {
		return netip.Addr{}, false
	}
	address, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	peer := address.Addr().Unmap().WithZone("")
	for _, network := range t.networks {
		if network.Contains(peer) {
			return peer, true
		}
	}
	return netip.Addr{}, false
}

// crossSite rejects browser requests a third-party page could have caused.
// Requests without fetch metadata or Origin (curl, the CLI) pass.
func (t NetworkTrust) crossSite(r *http.Request) error {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return fmt.Errorf("%w (Sec-Fetch-Site: %s)", ErrCrossSite, site)
	}
	value, present := r.Header["Origin"]
	if !present {
		return nil
	}
	want := t.origin
	if want == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		want = origin(scheme, r.Host)
	}
	if len(value) != 1 || !strings.EqualFold(value[0], want) {
		return fmt.Errorf("%w (Origin: %s; expected %s)", ErrCrossSite, strings.Join(value, ", "), want)
	}
	return nil
}

// origin serializes scheme://host the way browsers do in Origin, omitting
// the scheme's default port.
func origin(scheme, host string) string {
	scheme = strings.ToLower(scheme)
	host = strings.ToLower(host)
	if (scheme == "https" && strings.HasSuffix(host, ":443")) || (scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndexByte(host, ':')]
	}
	return scheme + "://" + host
}

func (t NetworkTrust) authenticate(r *http.Request) (auth.Actor, error) {
	if _, ok := t.peer(r); !ok {
		return auth.Actor{}, ErrUnauthorized
	}
	if err := t.crossSite(r); err != nil {
		return auth.Actor{}, err
	}
	return auth.Actor{ID: TrustedNetworkActorID, Scopes: AllScopes}, nil
}
