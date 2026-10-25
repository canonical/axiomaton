// Copyright (c) 2026 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

// Package proxy is the caching forward proxy that agent containers are
// pointed at. It caches package downloads (apt, the Go module proxy, PyPI,
// toolchain tarballs and so on) across containers and runs, and
// intercepts HTTPS with its own certificate authority so those can be
// cached too. The CA certificate is handed to each container.
package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA signs the certificates the proxy presents for intercepted hosts.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// CertPath and KeyPath are where the CA is stored.
	CertPath, KeyPath string
	// CertPEM is the certificate in PEM form, for installing in containers.
	CertPEM []byte

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

const (
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 30 * 24 * time.Hour
	// renewBefore is how close to expiry a certificate is replaced.
	renewBefore = 7 * 24 * time.Hour
)

// LoadOrCreateCA reads the CA from dir (ca.crt and ca.key), creating a new
// one when either is missing, unreadable or close to expiry. The key is
// written readable by the owner only.
func LoadOrCreateCA(dir string) (*CA, error) {
	ca := &CA{CertPath: filepath.Join(dir, "ca.crt"), KeyPath: filepath.Join(dir, "ca.key"), leaves: map[string]*tls.Certificate{}}
	if err := ca.load(); err == nil {
		return ca, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Several ax processes may start at once on a fresh machine; only one
	// creates the CA and the rest load it.
	unlock, err := lockFile(filepath.Join(dir, "ca.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := ca.load(); err == nil {
		return ca, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "axiomaton cache proxy CA", Organization: []string{"axiomaton"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := writeAtomic(ca.KeyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeAtomic(ca.CertPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	return ca, ca.load()
}

func (ca *CA) load() error {
	certPEM, err := os.ReadFile(ca.CertPath)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(ca.KeyPath)
	if err != nil {
		return err
	}
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return errors.New("proxy CA files are not PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return err
	}
	if !cert.IsCA || time.Until(cert.NotAfter) < renewBefore {
		return errors.New("proxy CA is not usable")
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return errors.New("proxy CA key does not match its certificate")
	}
	ca.cert, ca.key, ca.CertPEM = cert, key, certPEM
	return nil
}

// Certificate returns the CA certificate.
func (ca *CA) Certificate() *x509.Certificate { return ca.cert }

// Leaf returns a certificate for host (a DNS name or IP address) signed by
// the CA, issuing and caching one as needed.
func (ca *CA) Leaf(host string) (*tls.Certificate, error) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if c, ok := ca.leaves[host]; ok && time.Until(c.Leaf.NotAfter) > renewBefore {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key, Leaf: leaf}
	ca.leaves[host] = c
	return c, nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		panic(fmt.Sprintf("serial: %v", err))
	}
	return n
}
