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

// Package source fetches Terraform configuration from Git repositories and
// OCI artifacts and packages it as a gzip tarball for the runner.
package source

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
)

// Artifact is a fetched source.
type Artifact struct {
	// Revision uniquely identifies the content, e.g. "main@sha1:<sha>" for
	// Git or "v1.2.3@sha256:<digest>" for OCI.
	Revision string
	// Data is the gzip-compressed tar archive of the source tree.
	Data []byte
}

// Fetcher resolves and fetches a source.
type Fetcher interface {
	// Resolve returns the current revision without downloading content.
	Resolve(ctx context.Context, tf *infrav1.Terraform) (string, error)
	// Fetch downloads the source at its current revision.
	Fetch(ctx context.Context, tf *infrav1.Terraform) (*Artifact, error)
}

// Options configures the Manager.
type Options struct {
	// ArgoCDNamespace is where Argo CD repository credential Secrets live.
	// Empty disables spec.source.git.useArgoCDRepoCredentials.
	ArgoCDNamespace string
}

// Manager dispatches to the git or oci fetcher and caches artifacts by
// revision so unchanged sources are not re-downloaded on every reconcile.
type Manager struct {
	git Fetcher
	oci Fetcher

	mu    sync.Mutex
	cache map[string]*Artifact // keyed by namespace/name
}

// NewManager returns a Manager using c to read credential Secrets.
func NewManager(c client.Client, opts Options) *Manager {
	return &Manager{
		git:   &GitFetcher{Client: c, ArgoCDNamespace: opts.ArgoCDNamespace},
		oci:   &OCIFetcher{Client: c},
		cache: map[string]*Artifact{},
	}
}

func (m *Manager) fetcherFor(tf *infrav1.Terraform) (Fetcher, error) {
	switch {
	case tf.Spec.Source.Git != nil:
		return m.git, nil
	case tf.Spec.Source.OCI != nil:
		return m.oci, nil
	default:
		return nil, errors.New("spec.source must set git or oci")
	}
}

// Resolve returns the current revision of the source.
func (m *Manager) Resolve(ctx context.Context, tf *infrav1.Terraform) (string, error) {
	f, err := m.fetcherFor(tf)
	if err != nil {
		return "", err
	}
	return f.Resolve(ctx, tf)
}

// Fetch returns the artifact for the current revision, from cache when the
// revision has not changed.
func (m *Manager) Fetch(ctx context.Context, tf *infrav1.Terraform) (*Artifact, error) {
	f, err := m.fetcherFor(tf)
	if err != nil {
		return nil, err
	}
	key := client.ObjectKeyFromObject(tf).String()

	revision, err := f.Resolve(ctx, tf)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	cached, ok := m.cache[key]
	m.mu.Unlock()
	if ok && cached.Revision == revision {
		return cached, nil
	}

	art, err := f.Fetch(ctx, tf)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.cache[key] = art
	m.mu.Unlock()
	return art, nil
}

// Forget drops the cached artifact of tf.
func (m *Manager) Forget(tf *infrav1.Terraform) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, client.ObjectKeyFromObject(tf).String())
}

// NotFoundError marks a missing credential Secret or reference.
type NotFoundError struct{ Msg string }

func (e *NotFoundError) Error() string { return e.Msg }

func notFound(format string, args ...any) error {
	return &NotFoundError{Msg: fmt.Sprintf(format, args...)}
}
