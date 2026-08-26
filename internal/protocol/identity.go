package protocol

import (
	"errors"
	"net/http"
	"strings"
)

const NodeSANPrefix = "cable-node-"

type RevocationChecker interface {
	Revoked(nodeID, serial string) bool
}

func NodeIdentity(request *http.Request, checker RevocationChecker) (string, error) {
	if request.TLS == nil || len(request.TLS.VerifiedChains) == 0 || len(request.TLS.PeerCertificates) == 0 {
		return "", errors.New("verified client certificate is required")
	}
	certificate := request.TLS.PeerCertificates[0]
	var nodeID string
	for _, name := range certificate.DNSNames {
		if strings.HasPrefix(name, NodeSANPrefix) && len(name) > len(NodeSANPrefix) {
			if nodeID != "" {
				return "", errors.New("client certificate has multiple node identities")
			}
			nodeID = strings.TrimPrefix(name, NodeSANPrefix)
		}
	}
	if nodeID == "" {
		return "", errors.New("client certificate has no Cable node SAN")
	}
	if checker != nil && checker.Revoked(nodeID, certificate.SerialNumber.String()) {
		return "", errors.New("node credential is revoked")
	}
	return nodeID, nil
}
