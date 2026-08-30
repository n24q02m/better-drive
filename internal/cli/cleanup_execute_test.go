package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/n24q02m/better-drive/internal/cleanup"
	"github.com/n24q02m/better-drive/internal/paths"
	"github.com/n24q02m/better-drive/internal/protectedfs"
)

func setTestConfigHome(t *testing.T, root string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", root)
		return
	}
	t.Setenv("XDG_CONFIG_HOME", root)
}

func TestReadProtectedFileAcceptsOnlyBoundedRegularFilesInsideSecurityRoot(t *testing.T) {
	configHome := t.TempDir()
	setTestConfigHome(t, configHome)
	securityRoot := paths.CleanupSecurityDir()
	if err := protectedfs.EnsurePrivateDir(securityRoot); err != nil {
		t.Fatal(err)
	}
	writeProtected := func(path string, data []byte) {
		t.Helper()
		file, err := protectedfs.CreatePrivateFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	protectedPath := filepath.Join(securityRoot, "root.json")
	writeProtected(protectedPath, []byte(`{"active":true}`))
	data, err := readProtectedFile(protectedPath, 1024)
	if err != nil {
		t.Fatalf("readProtectedFile() error = %v", err)
	}
	if string(data) != `{"active":true}` {
		t.Fatalf("protected data = %q", data)
	}

	outsidePath := filepath.Join(configHome, "outside.json")
	if err := os.WriteFile(outsidePath, []byte(`{"active":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProtectedFile(outsidePath, 1024); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected outside-path rejection, got %v", err)
	}

	oversizedPath := filepath.Join(securityRoot, "oversized.json")
	writeProtected(oversizedPath, []byte(strings.Repeat("x", 1025)))
	if _, err := readProtectedFile(oversizedPath, 1024); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("expected size rejection, got %v", err)
	}
}

func TestReadSecretFDUsesInheritedDescriptorAndTrimsOneLineEnding(t *testing.T) {
	setInheritedFileDescriptor(t, "BETTER_DRIVE_TEST_SECRET_FD", []byte("secret-value\r\n"))
	data, err := readSecretFD("BETTER_DRIVE_TEST_SECRET_FD", 1024)
	if err != nil {
		t.Fatalf("readSecretFD() error = %v", err)
	}
	if string(data) != "secret-value" {
		t.Fatalf("secret data = %q", data)
	}
}

func TestCleanupBrokerTLSConfigRequiresTLS13VerifiedClientCertificates(t *testing.T) {
	pki := brokerTestCertificates(t)
	config, err := newCleanupBrokerTLSConfig(pki.serverCertificatePEM, pki.serverKeyPEM, pki.serverCAPEM, pki.clientCAPEM, "https://127.0.0.1:9443/")
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS13 || config.ClientAuth != tls.RequireAndVerifyClientCert ||
		config.ClientCAs == nil || len(config.Certificates) != 1 {
		t.Fatalf("unexpected broker TLS config: %+v", config)
	}
}

func TestCleanupBrokerTLSConfigRejectsEndpointOutsidePinnedCertificate(t *testing.T) {
	pki := brokerTestCertificates(t)
	_, err := newCleanupBrokerTLSConfig(
		pki.serverCertificatePEM,
		pki.serverKeyPEM,
		pki.serverCAPEM,
		pki.clientCAPEM,
		"https://broker.invalid:9443/",
	)
	if err == nil || !strings.Contains(err.Error(), "protected endpoint and CA") {
		t.Fatalf("endpoint mismatch error = %v", err)
	}
}

func TestCleanupBrokerAuthorityRejectsPrivateKeyMismatch(t *testing.T) {
	now := time.Unix(150, 0).UTC()
	approvalPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, wrongAuthorityPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approvalRoot, err := cleanup.NewTrustRoot(
		"approval-root",
		"cleanup-signer",
		cleanup.CleanupTrustPurpose,
		approvalPublicKey,
		now.Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	authorityRoot, err := cleanup.NewTrustRoot(
		"authority-root",
		"cleanup-broker",
		cleanup.OwnerRiskAuthorityPurpose,
		authorityPublicKey,
		now.Add(-time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "authority.git")
	if err := protectedfs.EnsurePrivateDir(storePath); err != nil {
		t.Fatal(err)
	}
	store, err := cleanup.NewApprovalStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	config := cleanupBrokerConfig{
		SchemaVersion: cleanupBrokerConfigSchema,
		Endpoint:      "https://127.0.0.1:9443/",
		Repository:    "n24q02m/private-control",
		Authority:     "cleanup-broker",
		Owner:         "executor-home",
	}
	if _, err := newCleanupBrokerAuthority(store, approvalRoot, authorityRoot, wrongAuthorityPrivateKey, config, func() time.Time { return now }); err == nil || !strings.Contains(err.Error(), "match") {
		t.Fatalf("private key mismatch error = %v", err)
	}
}

func TestCleanupBrokerServeCommandIsRegistered(t *testing.T) {
	command, _, err := cleanupCmd().Find([]string{"broker", "serve"})
	if err != nil {
		t.Fatal(err)
	}
	if command == nil || command.Name() != "serve" {
		t.Fatalf("cleanup broker serve command not found: %v", command)
	}
}

type brokerTestPKI struct {
	serverCAPEM          []byte
	serverCertificatePEM []byte
	serverKeyPEM         []byte
	clientCAPEM          []byte
	clientCertificatePEM []byte
	clientKeyPEM         []byte
}

func brokerTestCertificates(t *testing.T) brokerTestPKI {
	t.Helper()
	now := time.Now().UTC()
	serverCAPublicKey, serverCAPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverCATemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cleanup-test-server-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	serverCADER, err := x509.CreateCertificate(rand.Reader, serverCATemplate, serverCATemplate, serverCAPublicKey, serverCAPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	clientCAPublicKey, clientCAPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientCATemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "cleanup-test-client-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	clientCADER, err := x509.CreateCertificate(rand.Reader, clientCATemplate, clientCATemplate, clientCAPublicKey, clientCAPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverPublicKey, serverPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, serverCATemplate, serverPublicKey, serverCAPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	clientPublicKey, clientPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(4),
		Subject:      pkix.Name{CommonName: "cleanup-test-client"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, clientCATemplate, clientPublicKey, clientCAPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	clientKeyDER, err := x509.MarshalPKCS8PrivateKey(clientPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return brokerTestPKI{
		serverCAPEM:          pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCADER}),
		serverCertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		serverKeyPEM:         pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}),
		clientCAPEM:          pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCADER}),
		clientCertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}),
		clientKeyPEM:         pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: clientKeyDER}),
	}
}

func TestServeCleanupBrokerEnforcesMutualTLSAndStops(t *testing.T) {
	pki := brokerTestCertificates(t)
	serverTLS, err := newCleanupBrokerTLSConfig(pki.serverCertificatePEM, pki.serverKeyPEM, pki.serverCAPEM, pki.clientCAPEM, "https://127.0.0.1:9443/")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	result := make(chan error, 1)
	go func() {
		result <- serveCleanupBroker(ctx, &output, &cleanupBrokerRuntime{
			handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			}),
			tlsConfig:     serverTLS,
			listenAddress: address,
			endpoint:      "https://" + address + "/",
			repository:    "n24q02m/private-control",
			authorityName: "cleanup-broker",
			listen: func(_ context.Context, network, requestedAddress string) (net.Listener, error) {
				if network != "tcp" || requestedAddress != address {
					return nil, fmt.Errorf("unexpected listen request: %s %s", network, requestedAddress)
				}
				return listener, nil
			},
		})
	}()

	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(pki.serverCAPEM) {
		t.Fatal("append test CA")
	}
	withoutCertificateTransport := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    rootCAs,
	}}
	withoutCertificate := &http.Client{Transport: withoutCertificateTransport, Timeout: 2 * time.Second}
	response, err := withoutCertificate.Get("https://" + address + "/")
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("broker accepted a client without a certificate")
	}
	withoutCertificateTransport.CloseIdleConnections()

	clientCertificate, err := tls.X509KeyPair(pki.clientCertificatePEM, pki.clientKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	withCertificateTransport := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      rootCAs,
		Certificates: []tls.Certificate{clientCertificate},
	}}
	defer withCertificateTransport.CloseIdleConnections()
	withCertificate := &http.Client{Transport: withCertificateTransport, Timeout: 2 * time.Second}
	response, err = withCertificate.Get("https://" + address + "/")
	if err != nil {
		t.Fatalf("verified client request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("verified client status = %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serveCleanupBroker() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup broker did not stop after context cancellation")
	}
	if !strings.Contains(output.String(), "authority=cleanup-broker") {
		t.Fatalf("startup output = %q", output.String())
	}
}

func TestCleanupBrokerHTTPClientPinsServerCAAndPresentsClientCertificate(t *testing.T) {
	pki := brokerTestCertificates(t)
	serverTLS, err := newCleanupBrokerTLSConfig(pki.serverCertificatePEM, pki.serverKeyPEM, pki.serverCAPEM, pki.clientCAPEM, "https://127.0.0.1:9443/")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveCleanupBroker(ctx, io.Discard, &cleanupBrokerRuntime{
			handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.TLS == nil || len(request.TLS.VerifiedChains) == 0 {
					http.Error(writer, "unverified peer", http.StatusUnauthorized)
					return
				}
				writer.WriteHeader(http.StatusNoContent)
			}),
			tlsConfig:     serverTLS,
			listenAddress: address,
			endpoint:      "https://" + address + "/",
			repository:    "n24q02m/private-control",
			authorityName: "cleanup-broker",
			listen: func(_ context.Context, _, _ string) (net.Listener, error) {
				return listener, nil
			},
		})
	}()

	setInheritedFileDescriptor(t, cleanupMTLSCertFDEnv, pki.clientCertificatePEM)
	setInheritedFileDescriptor(t, cleanupMTLSKeyFDEnv, pki.clientKeyPEM)
	client, err := newCleanupBrokerHTTPClient(string(pki.serverCAPEM))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatalf("production broker client request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("production broker client status = %d", response.StatusCode)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatal("production broker client did not install the pinned server CA")
	}
	transport.CloseIdleConnections()

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serveCleanupBroker() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup broker did not stop after production client test")
	}
}

func setInheritedFileDescriptor(t *testing.T, environmentName string, data []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), environmentName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	descriptor := duplicateInheritedTestDescriptor(t, file)
	t.Setenv(environmentName, strconv.FormatUint(uint64(descriptor), 10))
}

func TestReadCleanupBrokerConfigRequiresExactHTTPSOrigin(t *testing.T) {
	configHome := t.TempDir()
	setTestConfigHome(t, configHome)
	if err := protectedfs.EnsurePrivateDir(paths.CleanupSecurityDir()); err != nil {
		t.Fatal(err)
	}
	configPath := paths.CleanupTrustBundleFile()
	bundle := testCleanupTrustBundle(t, time.Now().UTC().Add(-time.Hour), "endpoint")
	writeConfig := func(endpoint string) {
		t.Helper()
		bundle.Broker.Endpoint = endpoint
		data, err := json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(configPath)
		file, err := protectedfs.CreatePrivateFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("https://127.0.0.1:9443/")
	if _, err := readCleanupBrokerConfig(); err != nil {
		t.Fatalf("valid broker config: %v", err)
	}
	for _, endpoint := range []string{
		"http://127.0.0.1:9443/",
		"https://127.0.0.1/",
		"https://127.0.0.1:9443/broker/",
		"https://user@127.0.0.1:9443/",
	} {
		writeConfig(endpoint)
		if _, err := readCleanupBrokerConfig(); err == nil {
			t.Fatalf("invalid endpoint %q was accepted", endpoint)
		}
	}
}
