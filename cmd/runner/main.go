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

// Command runner is the gRPC server that executes terraform / tofu inside
// the runner Pod. It expects POD_NAMESPACE to be set from the downward API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/redhat-developer/argocd-terraform-controller/internal/runner"
)

var (
	// Version and Commit are set at build time.
	Version = "dev"
	Commit  = "unknown"
)

func main() {
	var (
		grpcPort      int
		tlsSecretName string
		maxMessageMiB int
	)
	flag.IntVar(&grpcPort, "grpc-port", 30000, "Port on which to expose the gRPC endpoint.")
	flag.StringVar(&tlsSecretName, "tls-secret-name", "", "Name of the TLS Secret issued by the controller. Empty disables TLS (development only).")
	flag.IntVar(&maxMessageMiB, "grpc-max-message-size", 4, "Maximum size of gRPC messages in MiB.")
	zapOpts := zap.Options{Development: false, TimeEncoder: zapcore.RFC3339TimeEncoder}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	if os.Getenv("DISABLE_TF_LOGS") != "1" {
		ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	}
	log := ctrl.Log.WithName("runner")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigs
		log.Info("shutdown signal received")
		close(done)
		cancel()
	}()

	log.Info("starting runner", "version", Version, "commit", Commit, "port", grpcPort, "tls", tlsSecretName != "")
	err := runner.Serve(ctx, runner.ServeOptions{
		Addr:          fmt.Sprintf(":%d", grpcPort),
		Namespace:     os.Getenv("POD_NAMESPACE"),
		TLSSecretName: tlsSecretName,
		MaxMessageMiB: maxMessageMiB,
		Done:          done,
	})
	if err != nil {
		log.Error(err, "runner exited with error")
		os.Exit(1)
	}
}
