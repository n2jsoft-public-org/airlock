package cachemcp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func startRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	return miniredis.RunT(t)
}

func portOf(t *testing.T, m *miniredis.Miniredis) int {
	t.Helper()
	_, port, err := net.SplitHostPort(m.Addr())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// startTLSRedis serves Redis over TLS with a certificate for serverName, trusted by the clients
// of this test only.
func startTLSRedis(t *testing.T, serverName string) *miniredis.Miniredis {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: serverName}, DNSNames: []string{serverName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	previous := rootCAs
	rootCAs = pool
	t.Cleanup(func() { rootCAs = previous })

	m, err := miniredis.RunTLS(&tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
