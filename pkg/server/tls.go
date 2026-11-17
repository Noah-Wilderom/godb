package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"uuid"
)

type peerTLS struct {
	certificate tls.Certificate
	roots       *x509.CertPool
	config      Config
}

func PeerIdentityURI(group uuid.UUID, node uint64) string {
	return fmt.Sprintf("spiffe://godb/%s/node/%d", group.String(), node)
}

func loadTLS(config Config) (*peerTLS, error) {
	certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, err
	}

	ca, err := os.ReadFile(config.CAFile) // #nosec G304 -- Read the CA bundle selected by the local server operator.
	if err != nil {
		return nil, err
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("%w: no CA certificates", ErrInvalidConfig)
	}

	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, err
	}

	if !matchesIdentity(leaf, config.Identity.GroupID, config.Identity.NodeID) {
		return nil, fmt.Errorf("%w: local certificate identity", ErrInvalidConfig)
	}

	intermediates := x509.NewCertPool()

	for _, encoded := range certificate.Certificate[1:] {
		cert, err := x509.ParseCertificate(encoded)
		if err != nil {
			return nil, err
		}

		intermediates.AddCert(cert)
	}

	for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
			return nil, err
		}
	}

	host, _, err := net.SplitHostPort(config.Peers[config.Identity.NodeID])
	if err != nil {
		return nil, err
	}

	if err := leaf.VerifyHostname(host); err != nil {
		return nil, err
	}

	return &peerTLS{certificate: certificate, roots: roots, config: config}, nil
}

func matchesIdentity(cert *x509.Certificate, group uuid.UUID, node uint64) bool {
	return len(cert.URIs) == 1 && cert.URIs[0].String() == PeerIdentityURI(group, node)
}

func (p *peerTLS) server() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{p.certificate},
		ClientCAs: p.roots, ClientAuth: tls.RequireAndVerifyClientCert,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 0 {
				for id := range p.config.Peers {
					if id != p.config.Identity.NodeID && matchesIdentity(state.PeerCertificates[0], p.config.Identity.GroupID, id) {
						return nil
					}
				}
			}

			return fmt.Errorf("peer certificate is not a configured member")
		},
	}
}

func (p *peerTLS) client(id uint64) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{p.certificate}, RootCAs: p.roots,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || !matchesIdentity(state.PeerCertificates[0], p.config.Identity.GroupID, id) {
				return fmt.Errorf("unexpected peer certificate identity")
			}

			return nil
		},
	}
}
