/*
Copyright 2026 The Argo CD Terraform Controller Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package mtls issues the certificates used to secure the gRPC channel
// between the controller and runner Pods.
//
// The controller keeps a self-signed CA in memory and, on demand, writes an
// immutable TLS Secret per namespace that contains the CA and a certificate
// valid for runner Pods in that namespace. Both the runner (server) and the
// controller (client) load that Secret, so a runner only accepts connections
// from a holder of the current CA.
package mtls

import (
	"bytes"
	"context"
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
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
)

const (
	CertKey   = "tls.crt"
	KeyKey    = "tls.key"
	CACertKey = "ca.crt"
	CAKeyKey  = "ca.key"

	// RunnerTLSLabel marks TLS Secrets managed by the rotator.
	RunnerTLSLabel = "infra.argocd.io/runner-tls"
)

var log = logf.Log.WithName("cert-rotation")

var (
	_ manager.Runnable               = &CertRotator{}
	_ manager.LeaderElectionRunnable = &CertRotator{}
)

type keyPair struct {
	cert       *x509.Certificate
	key        *ecdsa.PrivateKey
	certPEM    []byte
	keyPEM     []byte
	validUntil time.Time
}

// CertRotator issues and rotates the CA and per-namespace runner certificates.
type CertRotator struct {
	Client client.Client

	// Ready is closed once the first CA has been generated.
	Ready chan struct{}

	CAName                 string
	CAOrganization         string
	CAValidityDuration     time.Duration
	RotationCheckFrequency time.Duration
	// LookaheadInterval renews the CA this long before it expires.
	LookaheadInterval time.Duration
	ClusterDomain     string
	// RuntimeNamespace is the controller namespace; stale Secrets there are
	// removed on startup.
	RuntimeNamespace string

	mu        sync.Mutex
	ca        *keyPair
	nsSecrets map[string]*corev1.Secret
	started   time.Time
}

// NeedLeaderElection makes only the leader rotate certificates.
func (cr *CertRotator) NeedLeaderElection() bool { return true }

// Start implements manager.Runnable.
func (cr *CertRotator) Start(ctx context.Context) error {
	cr.mu.Lock()
	cr.nsSecrets = map[string]*corev1.Secret{}
	cr.started = time.Now()
	if err := cr.rotateCALocked(); err != nil {
		cr.mu.Unlock()
		return fmt.Errorf("could not bootstrap CA: %w", err)
	}
	cr.mu.Unlock()

	if cr.RuntimeNamespace != "" {
		if err := cr.garbageCollect(ctx, cr.RuntimeNamespace); err != nil {
			log.Error(err, "startup gc failed", "namespace", cr.RuntimeNamespace)
		}
	}
	if cr.Ready != nil {
		close(cr.Ready)
	}
	log.Info("cert rotator started")
	defer log.Info("cert rotator stopped")

	ticker := time.NewTicker(cr.RotationCheckFrequency)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := cr.rotateIfNeeded(ctx); err != nil {
				log.Error(err, "rotation failed")
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// IsReady reports whether a CA exists.
func (cr *CertRotator) IsReady() bool {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return cr.ca != nil
}

func (cr *CertRotator) rotateCALocked() error {
	now := time.Now()
	ca, err := createCA(cr.CAName, cr.CAOrganization, now.Add(-time.Hour), now.Add(cr.CAValidityDuration))
	if err != nil {
		return err
	}
	cr.ca = ca
	// Namespace secrets were issued by the previous CA; drop them so they are
	// re-issued on next use.
	cr.nsSecrets = map[string]*corev1.Secret{}
	log.Info("generated new CA", "validUntil", ca.validUntil)
	return nil
}

func (cr *CertRotator) rotateIfNeeded(ctx context.Context) error {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if cr.ca != nil && cr.ca.validUntil.After(time.Now().Add(cr.LookaheadInterval)) {
		return nil
	}
	namespaces := make([]string, 0, len(cr.nsSecrets))
	for ns := range cr.nsSecrets {
		namespaces = append(namespaces, ns)
	}
	if err := cr.rotateCALocked(); err != nil {
		return err
	}
	for _, ns := range namespaces {
		if _, err := cr.ensureNamespaceTLSLocked(ctx, ns); err != nil {
			log.Error(err, "could not re-issue TLS secret", "namespace", ns)
		}
	}
	return nil
}

// SecretName returns the name of the TLS Secret issued by the current CA.
func (cr *CertRotator) SecretName() (string, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if cr.ca == nil {
		return "", errors.New("CA not ready")
	}
	return secretName(cr.ca), nil
}

func secretName(ca *keyPair) string {
	return fmt.Sprintf("%s-%d", infrav1.RunnerTLSSecretName, ca.validUntil.Unix())
}

// EnsureNamespaceTLS returns the TLS Secret for namespace, creating it if
// needed, and garbage-collects Secrets issued by older CAs.
func (cr *CertRotator) EnsureNamespaceTLS(ctx context.Context, namespace string) (*corev1.Secret, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	if cr.ca == nil {
		return nil, errors.New("CA not ready")
	}
	secret, err := cr.ensureNamespaceTLSLocked(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if err := cr.garbageCollect(ctx, namespace); err != nil {
		log.Error(err, "gc failed", "namespace", namespace)
	}
	return secret, nil
}

func (cr *CertRotator) ensureNamespaceTLSLocked(ctx context.Context, namespace string) (*corev1.Secret, error) {
	if s, ok := cr.nsSecrets[namespace]; ok {
		return s, nil
	}
	name := secretName(cr.ca)

	// Reuse a Secret issued by this CA if it survived a previous cache miss.
	existing := &corev1.Secret{}
	if err := cr.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err == nil {
		if _, _, err := artifactsFromSecret(existing); err == nil && bytes.Equal(existing.Data[CACertKey], cr.ca.certPEM) {
			cr.nsSecrets[namespace] = existing
			return existing, nil
		}
		if err := cr.Client.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
			return nil, err
		}
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}

	hostnames := []string{fmt.Sprintf("*.%s.pod.%s", namespace, cr.ClusterDomain)}
	certPEM, keyPEM, err := createLeaf(cr.ca, cr.CAName, hostnames, time.Now().Add(-time.Hour), cr.ca.validUntil)
	if err != nil {
		return nil, err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    map[string]string{RunnerTLSLabel: "true"},
		},
		Immutable: ptr.To(true),
		Type:      corev1.SecretTypeTLS,
		Data: map[string][]byte{
			CACertKey: cr.ca.certPEM,
			CAKeyKey:  cr.ca.keyPEM,
			CertKey:   certPEM,
			KeyKey:    keyPEM,
		},
	}
	if err := cr.Client.Create(ctx, secret); err != nil {
		return nil, fmt.Errorf("unable to create TLS secret %s/%s: %w", namespace, name, err)
	}
	cr.nsSecrets[namespace] = secret
	log.Info("issued runner TLS secret", "namespace", namespace, "name", name)
	return secret, nil
}

// garbageCollect deletes rotator Secrets in namespace that were not issued by
// the current CA.
func (cr *CertRotator) garbageCollect(ctx context.Context, namespace string) error {
	list := &corev1.SecretList{}
	if err := cr.Client.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels{RunnerTLSLabel: "true"}); err != nil {
		return err
	}
	current := ""
	if cr.ca != nil {
		current = secretName(cr.ca)
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Name == current {
			continue
		}
		if err := cr.Client.Delete(ctx, s); client.IgnoreNotFound(err) != nil {
			log.Error(err, "could not delete stale TLS secret", "namespace", namespace, "name", s.Name)
			continue
		}
		log.Info("deleted stale TLS secret", "namespace", namespace, "name", s.Name)
	}
	return nil
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func createCA(name, org string, begin, end time.Time) (*keyPair, error) {
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name, Organization: []string{org}},
		DNSNames:              []string{name},
		NotBefore:             begin,
		NotAfter:              end,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}
	certPEM, keyPEM, err := pemEncode(der, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &keyPair{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM, validUntil: end}, nil
}

func createLeaf(ca *keyPair, cn string, hostnames []string, begin, end time.Time) ([]byte, []byte, error) {
	if os.Getenv("INSECURE_LOCAL_RUNNER") == "1" {
		hostnames = append(hostnames, "localhost")
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		DNSNames:              hostnames,
		NotBefore:             begin,
		NotAfter:              end,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key: %w", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, key.Public(), ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating certificate: %w", err)
	}
	return pemEncode(der, key)
}

func pemEncode(der []byte, key *ecdsa.PrivateKey) ([]byte, []byte, error) {
	certBuf := &bytes.Buffer{}
	if err := pem.Encode(certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyBuf := &bytes.Buffer{}
	if err := pem.Encode(keyBuf, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		return nil, nil, err
	}
	return certBuf.Bytes(), keyBuf.Bytes(), nil
}

func artifactsFromSecret(secret *corev1.Secret) (caPEM []byte, cert tls.Certificate, err error) {
	caPEM, ok := secret.Data[CACertKey]
	if !ok || len(caPEM) == 0 {
		return nil, cert, fmt.Errorf("TLS secret %s is missing %s", secret.Name, CACertKey)
	}
	cert, err = tls.X509KeyPair(secret.Data[CertKey], secret.Data[KeyKey])
	if err != nil {
		return nil, cert, fmt.Errorf("TLS secret %s has an invalid key pair: %w", secret.Name, err)
	}
	return caPEM, cert, nil
}

func certPool(caPEM []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("failed to parse CA certificate")
	}
	return pool, nil
}

// ClientCredentials returns gRPC credentials for the controller side.
func ClientCredentials(secret *corev1.Secret) (credentials.TransportCredentials, error) {
	caPEM, cert, err := artifactsFromSecret(secret)
	if err != nil {
		return nil, err
	}
	pool, err := certPool(caPEM)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS12,
	}), nil
}

// ServerCredentials returns gRPC credentials for the runner side, requiring
// and verifying client certificates.
func ServerCredentials(secret *corev1.Secret) (credentials.TransportCredentials, error) {
	caPEM, cert, err := artifactsFromSecret(secret)
	if err != nil {
		return nil, err
	}
	pool, err := certPool(caPEM)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}), nil
}
