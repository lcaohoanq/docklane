package installartifacts

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"time"

	"docklane.local/docklane/internal/domain"
)

type PKIBundle struct {
	RootCertificate []byte
	RootPrivateKey  []byte
	LeafCertificate []byte
	LeafPrivateKey  []byte
}

func GeneratePKI(
	specification domain.InstallationSpecification,
	now time.Time,
	random io.Reader,
) (PKIBundle, error) {
	if err := specification.Validate(); err != nil {
		return PKIBundle{}, err
	}
	if random == nil {
		random = rand.Reader
	}
	rootKey, err := rsa.GenerateKey(random, specification.PKI.RSAKeyBits)
	if err != nil {
		return PKIBundle{}, fmt.Errorf("generate root CA key: %w", err)
	}
	leafKey, err := rsa.GenerateKey(random, specification.PKI.RSAKeyBits)
	if err != nil {
		return PKIBundle{}, fmt.Errorf("generate leaf key: %w", err)
	}
	now = now.UTC()
	notBefore := now.Add(-5 * time.Minute)
	rootSerial, err := randomSerial(random)
	if err != nil {
		return PKIBundle{}, fmt.Errorf("generate root CA serial: %w", err)
	}
	leafSerial, err := randomSerial(random)
	if err != nil {
		return PKIBundle{}, fmt.Errorf("generate leaf serial: %w", err)
	}
	rootTemplate := x509.Certificate{
		SerialNumber:          rootSerial,
		Subject:               pkix.Name{CommonName: specification.PKI.RootCommonName},
		NotBefore:             notBefore,
		NotAfter:              now.AddDate(0, 0, specification.PKI.RootValidityDays),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign |
			x509.KeyUsageCRLSign |
			x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(
		random,
		&rootTemplate,
		&rootTemplate,
		&rootKey.PublicKey,
		rootKey,
	)
	if err != nil {
		return PKIBundle{}, fmt.Errorf("create root CA certificate: %w", err)
	}
	rootCertificate, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return PKIBundle{}, err
	}
	leafTemplate := x509.Certificate{
		SerialNumber: leafSerial,
		Subject: pkix.Name{
			CommonName: "*." + specification.BaseDomain,
		},
		NotBefore:   notBefore,
		NotAfter:    now.AddDate(0, 0, specification.PKI.LeafValidityDays),
		DNSNames:    append([]string(nil), specification.PKI.DNSNames...),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(
		random,
		&leafTemplate,
		rootCertificate,
		&leafKey.PublicKey,
		rootKey,
	)
	if err != nil {
		return PKIBundle{}, fmt.Errorf("create leaf certificate: %w", err)
	}
	if err := verifyLeaf(specification, rootCertificate, leafDER, now); err != nil {
		return PKIBundle{}, err
	}
	return PKIBundle{
		RootCertificate: pem.EncodeToMemory(
			&pem.Block{Type: "CERTIFICATE", Bytes: rootDER},
		),
		RootPrivateKey: pem.EncodeToMemory(
			&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rootKey)},
		),
		LeafCertificate: pem.EncodeToMemory(
			&pem.Block{Type: "CERTIFICATE", Bytes: leafDER},
		),
		LeafPrivateKey: pem.EncodeToMemory(
			&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)},
		),
	}, nil
}

func randomSerial(random io.Reader) (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(random, limit)
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return serial, nil
}

func verifyLeaf(
	specification domain.InstallationSpecification,
	root *x509.Certificate,
	leafDER []byte,
	now time.Time,
) error {
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	for _, hostname := range []string{
		specification.BaseDomain,
		"docklane-preflight." + specification.BaseDomain,
	} {
		if _, err := leaf.Verify(x509.VerifyOptions{
			DNSName:     hostname,
			Roots:       roots,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return fmt.Errorf("verify generated leaf for %s: %w", hostname, err)
		}
	}
	return nil
}
