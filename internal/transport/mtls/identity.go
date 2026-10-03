package mtls

import (
	"crypto/x509"
	"errors"
	"slices"
	"strings"
)

// TrustDomain is the host of the identity URI of every certificate this
// system issues.
const TrustDomain = "data-path-assurance.local"

// Role is the part a client plays towards the controller.
type Role string

// The roles of the identity URI.
const (
	RoleNode   Role = "node"
	RoleFence  Role = "fence"
	RoleViewer Role = "viewer"
)

// Identity is what a client certificate says about its holder.
type Identity struct {
	// ClusterID is the cluster the certificate was issued for.
	ClusterID string
	// Role is the part the holder plays.
	Role Role
	// Subject names the holder within its role: for RoleNode the UID of the
	// Kubernetes Node. For the other roles the grammar of the subject belongs
	// to the service that accepts the role; ParseIdentity requires only a
	// non-empty path segment.
	Subject string
}

// URI renders the identity URI of id.
func (id Identity) URI() string {
	return "spiffe://" + TrustDomain + "/cluster/" + id.ClusterID + "/" + string(id.Role) + "/" + id.Subject
}

var (
	// ErrInvalidIdentity reports that a certificate carries no identity URI,
	// more than one, or one that violates the grammar.
	ErrInvalidIdentity = errors.New("mtls: the certificate carries no valid identity")
	// ErrClientAuthRequired reports that a certificate does not list the
	// client authentication extended key usage explicitly.
	ErrClientAuthRequired = errors.New("mtls: the certificate lacks the client authentication extended key usage")
)

// ParseIdentity reads the identity of a client certificate from its URI
// subject alternative names. Of those, the URIs with scheme spiffe and host
// exactly TrustDomain are identity URIs; URIs of any other scheme, trust
// domain or host (a port included) are ignored. There must be exactly one
// identity URI. It must have no user information, port, opaque part, query,
// fragment or percent-encoding, and its path must be exactly
// /cluster/<clusterID>/<role>/<subject> with four non-empty segments and no
// trailing slash. The cluster ID matches [A-Za-z0-9._-]{1,128}, the role is
// node, fence or viewer, and for the node role so does the subject. Any
// violation returns ErrInvalidIdentity.
func ParseIdentity(leaf *x509.Certificate) (Identity, error) {
	if leaf == nil {
		return Identity{}, ErrInvalidIdentity
	}
	var found int
	var path string
	for _, u := range leaf.URIs {
		if u == nil || u.Scheme != "spiffe" || u.Host != TrustDomain {
			continue
		}
		found++
		if found > 1 {
			return Identity{}, ErrInvalidIdentity
		}
		if u.User != nil || u.Port() != "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery ||
			u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" {
			return Identity{}, ErrInvalidIdentity
		}
		path = u.Path
	}
	if found != 1 {
		return Identity{}, ErrInvalidIdentity
	}
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "cluster" || parts[2] == "" || parts[3] == "" || parts[4] == "" {
		return Identity{}, ErrInvalidIdentity
	}
	id := Identity{ClusterID: parts[2], Role: Role(parts[3]), Subject: parts[4]}
	if !isNameChars(id.ClusterID, 128) {
		return Identity{}, ErrInvalidIdentity
	}
	switch id.Role {
	case RoleNode:
		if !isNameChars(id.Subject, 128) {
			return Identity{}, ErrInvalidIdentity
		}
	case RoleFence, RoleViewer:
	default:
		return Identity{}, ErrInvalidIdentity
	}
	return id, nil
}

// RequireClientAuth reports whether the certificate lists the client
// authentication extended key usage explicitly. A certificate without any
// extended key usage, or with only the any usage, does not qualify; the TLS
// stack lets both through its handshake, so servers call this after it.
func RequireClientAuth(leaf *x509.Certificate) error {
	if leaf == nil || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		return ErrClientAuthRequired
	}
	return nil
}

// ClientIdentity returns the identity of an authenticated client certificate:
// ParseIdentity, then RequireClientAuth. A caller maps either error to an
// unauthenticated status; whether the role is acceptable is the caller's
// decision.
func ClientIdentity(leaf *x509.Certificate) (Identity, error) {
	id, err := ParseIdentity(leaf)
	if err != nil {
		return Identity{}, err
	}
	if err := RequireClientAuth(leaf); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// isNameChars reports whether s matches [A-Za-z0-9._-]{1,max}.
func isNameChars(s string, max int) bool {
	if len(s) < 1 || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
