package harness

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// ClientPKI is a freshly generated certificate chain for A2A client
// authentication: a self-signed CA and a client certificate it issued. The CA
// PEM is uploaded to Safeguard as a trusted certificate, and the client
// certificate's thumbprint identifies the certificate user.
type ClientPKI struct {
	// CACertPEM is the issuing CA certificate, PEM-encoded. Upload this to
	// Safeguard's Trusted CA Certificates.
	CACertPEM []byte

	// CACertDER is the issuing CA certificate in raw DER form, for callers that
	// need base64(DER) (Safeguard's TrustedCertificates Base64CertificateData).
	CACertDER []byte

	// ClientCertPEM is the client (leaf) certificate, PEM-encoded. This is the
	// clientCertificate supplied to the provider / A2A context.
	ClientCertPEM []byte

	// ClientKeyPEM is the client private key in PKCS#8 PEM. This is the
	// clientKey supplied to the provider / A2A context.
	ClientKeyPEM []byte

	// Thumbprint is the client certificate's SHA-1 thumbprint in Safeguard's
	// format: uppercase hex with no separators. Map the certificate user to it.
	Thumbprint string
}

// GenerateClientPKI builds a CA and a client certificate signed by it. The
// client certificate carries the Extended Key Usage for client authentication,
// which Safeguard requires for certificate-based users.
func GenerateClientPKI(commonName string) (*ClientPKI, error) {
	notBefore := time.Now().Add(-1 * time.Hour)
	notAfter := notBefore.Add(24 * time.Hour)

	// --- Issuing CA ---
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}
	caSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: commonName + " CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	// --- Client (leaf) ---
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating client key: %w", err)
	}
	clientSerial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: clientSerial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("creating client certificate: %w", err)
	}

	clientKeyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		return nil, fmt.Errorf("marshaling client key: %w", err)
	}

	sum := sha1.Sum(clientDER)

	return &ClientPKI{
		CACertPEM:     pemEncode("CERTIFICATE", caDER),
		CACertDER:     caDER,
		ClientCertPEM: pemEncode("CERTIFICATE", clientDER),
		ClientKeyPEM:  pemEncode("PRIVATE KEY", clientKeyDER),
		Thumbprint:    strings.ToUpper(fmt.Sprintf("%x", sum[:])),
	}, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}
	return serial, nil
}

func pemEncode(blockType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
}
