package tls

import (
	"bytes"
	gotls "crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/protocol/tls/cert"
)

func TestSBCertificateSnapshotPublication(t *testing.T) {
	first := &gotls.Certificate{Leaf: &x509.Certificate{DNSNames: []string{"example.test"}}, OCSPStaple: []byte("first")}
	second := &gotls.Certificate{Leaf: &x509.Certificate{DNSNames: []string{"example.test"}}, OCSPStaple: []byte("second")}
	snapshots := &certificateSnapshots{}
	index := snapshots.append(first)
	original := snapshots.load()
	selectCertificate := getNewGetCertificateFunc(snapshots, true)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 1000 {
				selected, err := selectCertificate(&gotls.ClientHelloInfo{ServerName: "example.test"})
				if err != nil || (selected != first && selected != second) {
					t.Errorf("invalid published certificate: %v", err)
					return
				}
				_ = bytes.Equal(selected.OCSPStaple, []byte("first"))
			}
		})
	}
	workers.Go(func() {
		for range 1000 {
			snapshots.store(index, second)
			snapshots.store(index, first)
		}
	})
	workers.Wait()
	if original[0] != first || string(original[0].OCSPStaple) != "first" {
		t.Fatal("publication mutated a previously returned certificate snapshot")
	}
}

func TestSBCertificateFileReloadKeepsConfiguredStateImmutable(t *testing.T) {
	oldCert, _ := cert.MustGenerate(nil, cert.CommonName("example.test"), cert.DNSNames("example.test"))
	newCert, _ := cert.MustGenerate(nil, cert.CommonName("example.test"), cert.DNSNames("example.test"))
	configured := ParseCertificate(oldCert)
	newPEM, newKey := newCert.ToPEM()
	configured.CertificatePath = filepath.Join(t.TempDir(), "certificate.pem")
	configured.KeyPath = filepath.Join(filepath.Dir(configured.CertificatePath), "key.pem")
	if err := os.WriteFile(configured.CertificatePath, newPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configured.KeyPath, newKey, 0600); err != nil {
		t.Fatal(err)
	}
	originalCert, originalKey := bytes.Clone(configured.Certificate), bytes.Clone(configured.Key)
	tlsConfig := (&Config{Certificate: []*Certificate{configured}}).GetTLSConfig()
	expected, err := gotls.X509KeyPair(newPEM, newKey)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 1000 {
				if _, err := tlsConfig.GetCertificate(&gotls.ClientHelloInfo{ServerName: "example.test"}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		selected, err := tlsConfig.GetCertificate(&gotls.ClientHelloInfo{ServerName: "example.test"})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(selected.Certificate[0], expected.Certificate[0]) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("certificate file reload was not published")
		}
		time.Sleep(time.Millisecond)
	}
	workers.Wait()
	if !bytes.Equal(configured.Certificate, originalCert) || !bytes.Equal(configured.Key, originalKey) {
		t.Fatal("ticker changed shared proto certificate state")
	}
}
