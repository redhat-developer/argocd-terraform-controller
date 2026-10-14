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

package runner

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/mtls"
)

// ServeOptions configures Serve.
type ServeOptions struct {
	// Addr to listen on, e.g. ":30000".
	Addr string
	// Namespace and TLSSecretName locate the TLS Secret issued by the
	// controller. When TLSSecretName is empty the server runs without TLS
	// (only for local development).
	Namespace     string
	TLSSecretName string
	MaxMessageMiB int
	// Done is closed on SIGTERM to cancel in-flight commands.
	Done <-chan struct{}
}

// NewScheme returns the scheme used by the runner.
func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := infrav1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, nil
}

// Serve runs the runner gRPC server until it fails or ctx is cancelled.
func Serve(ctx context.Context, opts ServeOptions) error {
	scheme, err := NewScheme()
	if err != nil {
		return err
	}
	k8sClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		return err
	}

	creds := insecure.NewCredentials()
	if opts.TLSSecretName != "" {
		creds, err = loadServerCredentials(ctx, k8sClient, opts.Namespace, opts.TLSSecretName)
		if err != nil {
			return err
		}
	}

	server := NewServer(k8sClient, scheme, opts.Done)
	return ServeWith(ctx, server, opts.Addr, creds, opts.MaxMessageMiB)
}

func loadServerCredentials(ctx context.Context, c client.Client, namespace, name string) (credentials.TransportCredentials, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return nil, fmt.Errorf("unable to read TLS secret %s/%s: %w", namespace, name, err)
	}
	return mtls.ServerCredentials(secret)
}

// ServeWith serves server on addr with the given credentials.
func ServeWith(ctx context.Context, server RunnerServer, addr string, creds credentials.TransportCredentials, maxMessageMiB int) error {
	if maxMessageMiB <= 0 {
		maxMessageMiB = 4
	}
	maxMsgSize := maxMessageMiB * 1024 * 1024

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(maxMsgSize), grpc.MaxSendMsgSize(maxMsgSize))
	RegisterRunnerServer(grpcServer, server)

	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()
	return grpcServer.Serve(listener)
}
