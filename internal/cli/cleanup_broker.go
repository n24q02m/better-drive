package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/spf13/cobra"
)

const (
	cleanupAuthorityKeyFDEnv = "BETTER_DRIVE_CLEANUP_AUTHORITY_KEY_FD"
	cleanupServerCertFDEnv   = "BETTER_DRIVE_CLEANUP_SERVER_CERT_FD"
	cleanupServerKeyFDEnv    = "BETTER_DRIVE_CLEANUP_SERVER_KEY_FD"
	cleanupClientCAFDEnv     = "BETTER_DRIVE_CLEANUP_CLIENT_CA_FD"
)

type cleanupBrokerRuntime struct {
	authority     *cleanup.GitOwnerRiskAuthority
	handler       http.Handler
	tlsConfig     *tls.Config
	listen        func(context.Context, string, string) (net.Listener, error)
	listenAddress string
	endpoint      string
	repository    string
	authorityName string
}

func cleanupBrokerCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "broker",
		Short: "Run the protected owner-risk cleanup authority",
		Long:  "Serve the private-Git cleanup authority with TLS 1.3 and verified client certificates. Trust roots and broker configuration are read only from the protected cleanup directory; private keys are accepted only through inherited file descriptors.",
	}
	command.AddCommand(cleanupBrokerServeCmd())
	return command
}

func cleanupBrokerServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve the protected mTLS cleanup broker",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			runtime, err := loadCleanupBrokerRuntime()
			if err != nil {
				return exitcode.WithRemediation(
					exitcode.ConfigError(err),
					"install protected cleanup roots/config and pass authority, server, and client-CA material through inherited file descriptors",
				)
			}
			return serveCleanupBroker(command.Context(), command.OutOrStdout(), runtime)
		},
	}
}

func loadCleanupBrokerRuntime() (*cleanupBrokerRuntime, error) {
	bundle, err := readProtectedTrustBundle()
	if err != nil {
		return nil, err
	}
	config := bundle.Broker
	listenAddress, err := cleanupBrokerListenAddress(config)
	if err != nil {
		return nil, err
	}
	privateKeyPEM, err := readSecretFD(cleanupAuthorityKeyFDEnv, maxCleanupPEMBytes)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(privateKeyPEM)
	privateKey, err := parseCleanupAuthorityPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(privateKey)

	store, err := cleanup.NewApprovalStore(paths.CleanupAuthorityStoreDir())
	if err != nil {
		return nil, fmt.Errorf("open protected cleanup authority store: %w", err)
	}
	authority, err := newCleanupBrokerAuthority(store, bundle.ApprovalRoot, bundle.AuthorityRoot, privateKey, config, time.Now)
	if err != nil {
		return nil, err
	}
	handler, err := cleanup.NewOwnerRiskHTTPHandler(authority, cleanup.RequireMTLSOwnerRiskPeer)
	if err != nil {
		return nil, err
	}

	serverCertificatePEM, err := readSecretFD(cleanupServerCertFDEnv, maxCleanupPEMBytes)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(serverCertificatePEM)
	serverKeyPEM, err := readSecretFD(cleanupServerKeyFDEnv, maxCleanupPEMBytes)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(serverKeyPEM)
	clientCAPEM, err := readSecretFD(cleanupClientCAFDEnv, maxCleanupPEMBytes)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(clientCAPEM)
	tlsConfig, err := newCleanupBrokerTLSConfig(
		serverCertificatePEM,
		serverKeyPEM,
		[]byte(bundle.BrokerServerCAPEM),
		clientCAPEM,
		config.Endpoint,
	)
	if err != nil {
		return nil, err
	}
	return &cleanupBrokerRuntime{
		authority:     authority,
		handler:       handler,
		tlsConfig:     tlsConfig,
		listenAddress: listenAddress,
		endpoint:      config.Endpoint,
		repository:    config.Repository,
		authorityName: config.Authority,
	}, nil
}

func newCleanupBrokerAuthority(
	store *cleanup.ApprovalStore,
	approvalRoot cleanup.TrustRoot,
	authorityRoot cleanup.TrustRoot,
	privateKey ed25519.PrivateKey,
	config cleanupBrokerConfig,
	now func() time.Time,
) (*cleanup.GitOwnerRiskAuthority, error) {
	if now == nil {
		return nil, errors.New("cleanup broker clock is required")
	}
	publicKey, err := authorityRoot.PublicKeyForPurpose(cleanup.OwnerRiskAuthorityPurpose, config.Authority, now().UTC())
	if err != nil {
		return nil, fmt.Errorf("validate cleanup authority trust root: %w", err)
	}
	privatePublicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(publicKey, privatePublicKey) {
		return nil, errors.New("cleanup authority private key does not match the protected authority trust root")
	}
	return cleanup.NewGitOwnerRiskAuthority(
		store,
		approvalRoot,
		privateKey,
		config.Authority,
		config.Repository,
		now,
	)
}

func parseCleanupAuthorityPrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, trailing := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(trailing)) != 0 {
		return nil, errors.New("cleanup authority key must be one PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("cleanup authority private key is invalid")
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("cleanup authority private key must be Ed25519")
	}
	return append(ed25519.PrivateKey(nil), privateKey...), nil
}

func newCleanupBrokerTLSConfig(
	serverCertificatePEM []byte,
	serverKeyPEM []byte,
	serverCAPEM []byte,
	clientCAPEM []byte,
	endpoint string,
) (*tls.Config, error) {
	certificate, err := tls.X509KeyPair(serverCertificatePEM, serverKeyPEM)
	if err != nil {
		return nil, errors.New("cleanup broker server certificate or key is invalid")
	}
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("cleanup broker server certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, errors.New("cleanup broker server leaf certificate is invalid")
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Scheme != "https" || strings.TrimSpace(origin.Hostname()) == "" {
		return nil, errors.New("cleanup broker endpoint is invalid for server certificate verification")
	}
	now := time.Now().UTC()
	serverCAs, err := newCleanupCAPool(serverCAPEM, "server", now)
	if err != nil {
		return nil, err
	}
	intermediates := x509.NewCertPool()
	for _, encoded := range certificate.Certificate[1:] {
		intermediate, err := x509.ParseCertificate(encoded)
		if err != nil {
			return nil, errors.New("cleanup broker server certificate chain is invalid")
		}
		intermediates.AddCert(intermediate)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       origin.Hostname(),
		Intermediates: intermediates,
		Roots:         serverCAs,
		CurrentTime:   now,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, errors.New("cleanup broker server certificate does not match the protected endpoint and CA")
	}
	certificate.Leaf = leaf
	clientCAs, err := newCleanupCAPool(clientCAPEM, "client", now)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}, nil
}

func newCleanupCAPool(data []byte, purpose string, now time.Time) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	remaining := data
	caCount := 0
	for len(bytes.TrimSpace(remaining)) != 0 {
		block, rest := pem.Decode(remaining)
		if block == nil {
			return nil, fmt.Errorf("cleanup broker %s CA bundle is invalid PEM", purpose)
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("cleanup broker %s CA bundle contains a non-certificate block", purpose)
		}
		ca, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || now.Before(ca.NotBefore) || now.After(ca.NotAfter) {
			return nil, fmt.Errorf("cleanup broker %s CA certificate is invalid", purpose)
		}
		pool.AddCert(ca)
		caCount++
	}
	if caCount == 0 {
		return nil, fmt.Errorf("cleanup broker %s CA bundle is empty", purpose)
	}
	return pool, nil
}

func cleanupBrokerListenAddress(config cleanupBrokerConfig) (string, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" ||
		endpoint.Fragment != "" || endpoint.RawPath != "" || endpoint.Path != "/" {
		return "", errors.New("protected cleanup broker endpoint must be an exact HTTPS origin ending in /")
	}
	host, port, err := net.SplitHostPort(endpoint.Host)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("protected cleanup broker endpoint must include an explicit host and port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber <= 0 || portNumber > 65535 {
		return "", errors.New("protected cleanup broker endpoint port is invalid")
	}
	return endpoint.Host, nil
}

func serveCleanupBroker(ctx context.Context, output io.Writer, runtime *cleanupBrokerRuntime) error {
	if ctx == nil || output == nil || runtime == nil || runtime.handler == nil || runtime.tlsConfig == nil {
		return errors.New("cleanup broker runtime is not configured")
	}
	listen := runtime.listen
	if listen == nil {
		listenConfig := &net.ListenConfig{}
		listen = listenConfig.Listen
	}
	listener, err := listen(ctx, "tcp", runtime.listenAddress)
	if err != nil {
		return fmt.Errorf("listen for cleanup broker: %w", err)
	}
	tlsListener := tls.NewListener(listener, runtime.tlsConfig)
	server := &http.Server{
		Handler:           runtime.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	if _, err := fmt.Fprintf(
		output,
		"cleanup broker listening: endpoint=%s authority=%s repository=%s\n",
		runtime.endpoint,
		runtime.authorityName,
		runtime.repository,
	); err != nil {
		_ = listener.Close()
		return err
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(tlsListener)
	}()
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		serveErr := <-serveResult
		if shutdownErr != nil {
			return shutdownErr
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}
