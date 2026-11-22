package tlsconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"time"
)

const (
	certKey       = "tls_cert"
	keyKey        = "tls_key"
	validity      = 10 * 365 * 24 * time.Hour
	renewalWindow = 30 * 24 * time.Hour
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Config() (*tls.Config, error) {
	cert, err := s.certificate()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}, nil
}

func (s *Store) certificate() (tls.Certificate, error) {
	certPEM, keyPEM, err := s.load()
	if err != nil {
		return tls.Certificate{}, err
	}
	if certPEM != "" && keyPEM != "" {
		cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err == nil && stillValid(cert) {
			return cert, nil
		}
	}
	certPEM, keyPEM, err = generate()
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := s.save(certPEM, keyPEM); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
}

func stillValid(cert tls.Certificate) bool {
	if len(cert.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return false
	}
	return time.Now().Add(renewalWindow).Before(leaf.NotAfter)
}

func (s *Store) load() (string, string, error) {
	values := map[string]string{}
	for _, key := range []string{certKey, keyKey} {
		var value string
		err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		values[key] = value
	}
	return values[certKey], values[keyKey], nil
}

func (s *Store) save(certPEM, keyPEM string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{certKey: certPEM, keyKey: keyPEM} {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func generate() (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", err
	}

	dnsNames := []string{"localhost"}
	if host, err := os.Hostname(); err == nil && host != "" && host != "localhost" {
		dnsNames = append(dnsNames, host)
	}
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				ips = append(ips, ipNet.IP)
			}
		}
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Aura", Organization: []string{"Aura"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return string(certPEM), string(keyPEM), nil
}
