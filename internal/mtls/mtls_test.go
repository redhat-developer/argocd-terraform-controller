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

package mtls

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newRotator(t *testing.T) (*CertRotator, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	cr := &CertRotator{
		Client:                 c,
		Ready:                  make(chan struct{}),
		CAName:                 "argocd-terraform-controller",
		CAOrganization:         "argocd",
		CAValidityDuration:     time.Hour,
		RotationCheckFrequency: time.Hour,
		LookaheadInterval:      time.Minute,
		ClusterDomain:          "cluster.local",
	}
	return cr, c
}

func TestEnsureNamespaceTLSAndHandshake(t *testing.T) {
	t.Setenv("INSECURE_LOCAL_RUNNER", "1")
	cr, c := newRotator(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = cr.Start(ctx) }()
	select {
	case <-cr.Ready:
	case <-time.After(10 * time.Second):
		t.Fatal("rotator not ready")
	}

	secret, err := cr.EnsureNamespaceTLS(ctx, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := cr.EnsureNamespaceTLS(ctx, "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != again.Name {
		t.Errorf("secret re-issued unexpectedly: %s vs %s", secret.Name, again.Name)
	}
	stored := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(secret), stored); err != nil {
		t.Fatalf("secret not persisted: %v", err)
	}
	if stored.Labels[RunnerTLSLabel] != "true" || stored.Immutable == nil || !*stored.Immutable {
		t.Errorf("unexpected secret metadata: %+v", stored.ObjectMeta)
	}

	serverCreds, err := ServerCredentials(stored)
	if err != nil {
		t.Fatal(err)
	}
	clientCreds, err := ClientCredentials(stored)
	if err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(serverCreds))
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	// Dial by the "localhost" SAN added in INSECURE_LOCAL_RUNNER mode.
	_, port, _ := net.SplitHostPort(lis.Addr().String())
	conn, err := grpc.NewClient("localhost:"+port, grpc.WithTransportCredentials(clientCreds))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()
	if _, err := healthpb.NewHealthClient(conn).Check(dialCtx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("mTLS health check failed: %v", err)
	}

}

func TestRotationReissuesAndGarbageCollects(t *testing.T) {
	cr, c := newRotator(t)
	ctx := context.Background()
	cr.nsSecrets = map[string]*corev1.Secret{}
	cr.started = time.Now()
	if err := cr.rotateCALocked(); err != nil {
		t.Fatal(err)
	}
	first, err := cr.EnsureNamespaceTLS(ctx, "ns")
	if err != nil {
		t.Fatal(err)
	}

	// Force expiry so the next check rotates; a longer validity guarantees a
	// distinct secret name even within the same second.
	cr.ca.validUntil = time.Now().Add(-time.Second)
	cr.CAValidityDuration = 2 * time.Hour
	if err := cr.rotateIfNeeded(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := cr.EnsureNamespaceTLS(ctx, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if first.Name == second.Name {
		t.Fatal("expected a new secret after rotation")
	}
	list := &corev1.SecretList{}
	if err := c.List(ctx, list, client.InNamespace("ns")); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != second.Name {
		names := make([]string, 0, len(list.Items))
		for _, s := range list.Items {
			names = append(names, s.Name)
		}
		t.Errorf("expected only the new secret, got %v", names)
	}
}
