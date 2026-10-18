package compose

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readCert reads a PEM certificate written by RunnerCertificate.
func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("%s holds no PEM certificate", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// an inbound Runner's certificate : ECDSA P-256, its address as its only name, ten years, the
// key readable by its owner only, and the fingerprint of the certificate written
func TestRunnerCertificateForAnIP(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "certificates", "cert.pem"), filepath.Join(dir, "certificates", "key.pem")
	fp, err := RunnerCertificate(certFile, keyFile, "10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	cert := readCert(t, certFile)
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("the key is not ECDSA P-256: %T", cert.PublicKey)
	}
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("10.0.0.5")) || len(cert.DNSNames) != 0 {
		t.Fatalf("names = %v %v, want the IP address alone", cert.IPAddresses, cert.DNSNames)
	}
	if err := cert.VerifyHostname("10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	if cert.NotAfter.Before(time.Now().AddDate(9, 11, 0)) {
		t.Fatalf("valid until %s, want about ten years", cert.NotAfter)
	}
	if got := CertFingerprint(cert.Raw); got != fp {
		t.Fatalf("fingerprint %s, want the written certificate's %s", fp, got)
	}
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o, want 600", info.Mode().Perm())
	}
	keyData, _ := os.ReadFile(keyFile)
	block, _ := pem.Decode(keyData)
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !key.PublicKey.Equal(pub) {
		t.Fatal("the key does not belong to the certificate")
	}
}

// a host name goes into the DNS names ; an IPv6 address bracketed as in a URL is an IP address
func TestRunnerCertificateNames(t *testing.T) {
	dir := t.TempDir()
	if _, err := RunnerCertificate(filepath.Join(dir, "a.pem"), filepath.Join(dir, "a.key"), "runner.example.com"); err != nil {
		t.Fatal(err)
	}
	if c := readCert(t, filepath.Join(dir, "a.pem")); len(c.DNSNames) != 1 || c.DNSNames[0] != "runner.example.com" || len(c.IPAddresses) != 0 {
		t.Fatalf("names = %v %v, want runner.example.com alone", c.DNSNames, c.IPAddresses)
	}
	if _, err := RunnerCertificate(filepath.Join(dir, "b.pem"), filepath.Join(dir, "b.key"), "[2001:db8::5]"); err != nil {
		t.Fatal(err)
	}
	if c := readCert(t, filepath.Join(dir, "b.pem")); len(c.IPAddresses) != 1 || !c.IPAddresses[0].Equal(net.ParseIP("2001:db8::5")) {
		t.Fatalf("names = %v, want 2001:db8::5", c.IPAddresses)
	}
	if _, err := RunnerCertificate(filepath.Join(dir, "c.pem"), filepath.Join(dir, "c.key"), " "); err == nil {
		t.Fatal("an empty address was accepted")
	}
}

// two certificates never share a fingerprint, which has the token's style
func TestCertFingerprintStyle(t *testing.T) {
	dir := t.TempDir()
	a, _ := RunnerCertificate(filepath.Join(dir, "a.pem"), filepath.Join(dir, "a.key"), "10.0.0.5")
	b, _ := RunnerCertificate(filepath.Join(dir, "b.pem"), filepath.Join(dir, "b.key"), "10.0.0.5")
	if a == b {
		t.Fatal("two certificates have the same fingerprint")
	}
	if len(a) != 9 || a[4] != '-' {
		t.Fatalf("fingerprint %q, want xxxx-xxxx", a)
	}
	if got := CertFingerprint([]byte("abc")); got != "ba78-16bf" {
		t.Fatalf("CertFingerprint(abc) = %s, want ba78-16bf", got)
	}
}

// the app reaches an inbound Runner over HTTPS, IPv6 addresses bracketed
func TestInboundAddress(t *testing.T) {
	cases := map[string]string{
		"10.0.0.5":           "https://10.0.0.5:8000",
		"runner.example.com": "https://runner.example.com:8000",
		"2001:db8::5":        "https://[2001:db8::5]:8000",
		"[2001:db8::5]":      "https://[2001:db8::5]:8000",
	}
	for host, want := range cases {
		if got := InboundAddress(host, 8000); got != want {
			t.Errorf("InboundAddress(%q) = %s, want %s", host, got, want)
		}
	}
}
