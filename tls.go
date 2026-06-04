package http

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/go-kratos/kratos/v2/transport/http"
	"github.com/go-lynx/lynx/log"
)

// tlsLoad builds the server TLS option from the Lynx certificate provider.
// Certs are served via a GetCertificate callback so file-watch rotation takes effect without
// a restart. Client mTLS is enabled only when a root CA is present; otherwise ClientCAs is left
// unset and client-cert verification is effectively disabled regardless of the configured auth type.
func (h *ServiceHttp) tlsLoad() (http.ServerOption, error) {
	app := currentLynxApp()
	if app == nil {
		return nil, fmt.Errorf("lynx app not initialized")
	}
	certProvider := app.Certificate()
	if certProvider == nil {
		return nil, fmt.Errorf("certificate provider not configured")
	}

	// Validate certificate provider has required data at startup
	if len(certProvider.GetCertificate()) == 0 {
		return nil, fmt.Errorf("certificate data is empty")
	}
	if len(certProvider.GetPrivateKey()) == 0 {
		return nil, fmt.Errorf("private key data is empty")
	}

	// Create certificate pool and add root CA
	certPool := x509.NewCertPool()
	hasClientCAs := false
	rootCACert := certProvider.GetRootCACertificate()
	if len(rootCACert) > 0 {
		if !certPool.AppendCertsFromPEM(rootCACert) {
			log.Warnf("Failed to append root CA certificate to pool, continuing without client certificate verification")
		} else {
			hasClientCAs = true
			log.Infof("Root CA certificate successfully added to certificate pool")
		}
	} else {
		log.Warnf("No root CA certificate provided, client certificate verification will be disabled")
	}

	// Use GetCertificate callback for hot reload: file watch and auto rotation update certs without restart
	tlsConfig := &tls.Config{
		GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			certPEM := certProvider.GetCertificate()
			keyPEM := certProvider.GetPrivateKey()
			if len(certPEM) == 0 || len(keyPEM) == 0 {
				return nil, fmt.Errorf("server certificate or private key not provided")
			}
			cert, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				return nil, fmt.Errorf("failed to parse X509 key pair: %w", err)
			}
			return &cert, nil
		},
		ServerName: currentLynxName(),
		ClientAuth: tls.ClientAuthType(h.conf.GetTlsAuthType()),
	}

	// Only set ClientCAs if we have a valid certificate pool
	if hasClientCAs {
		tlsConfig.ClientCAs = certPool
	}

	log.Infof("TLS configuration created successfully with client auth type: %d", h.conf.GetTlsAuthType())
	return http.TLSConfig(tlsConfig), nil
}
