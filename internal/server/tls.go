package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// EnsureCert returns paths to a TLS certificate and key, generating a
// self-signed pair in certDir if none exist yet.
func EnsureCert(certDir string) (certPath, keyPath string, err error) {
	certPath = filepath.Join(certDir, "ditto.crt")
	keyPath = filepath.Join(certDir, "ditto.key")

	if fileExists(certPath) && fileExists(keyPath) {
		return certPath, keyPath, nil
	}

	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating cert dir: %w", err)
	}

	if err := generateCert(certPath, keyPath); err != nil {
		return "", "", err
	}

	return certPath, keyPath, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func generateCert(certPath, keyPath string) error {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generating private key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generating serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Ditto"},
			CommonName:   "Ditto Local Proxy",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // 10 years
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
			net.ParseIP("10.0.2.2"), // Android emulator host
		},
	}

	// Add the machine's local IPs to the cert so physical devices on the
	// same Wi-Fi network can validate it.
	for _, ip := range localIPs() {
		template.IPAddresses = append(template.IPAddresses, ip)
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("creating certificate: %w", err)
	}

	certFile, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("opening cert file: %w", err)
	}
	defer certFile.Close()
	if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return fmt.Errorf("writing cert: %w", err)
	}

	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("opening key file: %w", err)
	}
	defer keyFile.Close()
	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshaling key: %w", err)
	}
	if err := pem.Encode(keyFile, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return fmt.Errorf("writing key: %w", err)
	}

	return nil
}

func localIPs() []net.IP {
	var ips []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ipv4 := ipNet.IP.To4(); ipv4 != nil {
			ips = append(ips, ipv4)
		}
	}
	return ips
}
