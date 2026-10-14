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

package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-containerregistry/pkg/authn"
	k8sauthn "github.com/google/go-containerregistry/pkg/authn/kubernetes"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/tarball"
)

// OCIFetcher fetches sources packaged as OCI artifacts (e.g. by `oras push`).
// The first layer is expected to be a tar+gzip of
// the source tree; a plain tar is re-compressed.
type OCIFetcher struct {
	Client client.Client
}

func (o *OCIFetcher) options(ctx context.Context, tf *infrav1.Terraform) ([]remote.Option, []name.Option, error) {
	src := tf.Spec.Source.OCI
	opts := make([]remote.Option, 0, 2)
	opts = append(opts, remote.WithContext(ctx))
	var nameOpts []name.Option
	if src.Insecure {
		nameOpts = append(nameOpts, name.Insecure)
	}

	keychain := authn.Keychain(authn.DefaultKeychain)
	if src.SecretRef != nil {
		secret := &corev1.Secret{}
		if err := o.Client.Get(ctx, types.NamespacedName{Namespace: tf.Namespace, Name: src.SecretRef.Name}, secret); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return nil, nil, notFound("registry credentials Secret %s/%s not found", tf.Namespace, src.SecretRef.Name)
			}
			return nil, nil, err
		}
		if _, ok := secret.Data[corev1.DockerConfigJsonKey]; !ok {
			return nil, nil, fmt.Errorf("secret %s must be of type %s", src.SecretRef.Name, corev1.SecretTypeDockerConfigJson)
		}
		kc, err := k8sauthn.NewFromPullSecrets(ctx, []corev1.Secret{*secret})
		if err != nil {
			return nil, nil, fmt.Errorf("invalid registry credentials Secret %s: %w", src.SecretRef.Name, err)
		}
		keychain = authn.NewMultiKeychain(kc, authn.DefaultKeychain)
	}
	opts = append(opts, remote.WithAuthFromKeychain(keychain))
	return opts, nameOpts, nil
}

func (o *OCIFetcher) repository(tf *infrav1.Terraform, nameOpts []name.Option) (name.Repository, error) {
	url := strings.TrimPrefix(tf.Spec.Source.OCI.URL, "oci://")
	return name.NewRepository(url, nameOpts...)
}

// Resolve returns "<tag>@sha256:<digest>".
func (o *OCIFetcher) Resolve(ctx context.Context, tf *infrav1.Terraform) (string, error) {
	ref, digest, err := o.resolve(ctx, tf)
	if err != nil {
		return "", err
	}
	return ref + "@" + digest.String(), nil
}

func (o *OCIFetcher) resolve(ctx context.Context, tf *infrav1.Terraform) (string, v1.Hash, error) {
	opts, nameOpts, err := o.options(ctx, tf)
	if err != nil {
		return "", v1.Hash{}, err
	}
	repo, err := o.repository(tf, nameOpts)
	if err != nil {
		return "", v1.Hash{}, err
	}
	src := tf.Spec.Source.OCI.Ref
	if src == nil {
		src = &infrav1.OCIRef{}
	}

	var tag string
	switch {
	case src.Digest != "":
		h, err := v1.NewHash(src.Digest)
		if err != nil {
			return "", v1.Hash{}, fmt.Errorf("invalid digest %q: %w", src.Digest, err)
		}
		return h.Hex[:12], h, nil
	case src.SemVer != "":
		constraint, err := semver.NewConstraint(src.SemVer)
		if err != nil {
			return "", v1.Hash{}, fmt.Errorf("invalid semver range %q: %w", src.SemVer, err)
		}
		tags, err := remote.List(repo, opts...)
		if err != nil {
			return "", v1.Hash{}, fmt.Errorf("unable to list tags of %s: %w", repo, err)
		}
		var best *semver.Version
		for _, t := range tags {
			v, err := semver.NewVersion(t)
			if err != nil || !constraint.Check(v) {
				continue
			}
			if best == nil || v.GreaterThan(best) {
				best, tag = v, t
			}
		}
		if best == nil {
			return "", v1.Hash{}, notFound("no tag of %s matches semver range %q", repo, src.SemVer)
		}
	case src.Tag != "":
		tag = src.Tag
	default:
		tag = "latest"
	}

	desc, err := remote.Head(repo.Tag(tag), opts...)
	if err != nil {
		return "", v1.Hash{}, fmt.Errorf("unable to resolve %s:%s: %w", repo, tag, err)
	}
	return tag, desc.Digest, nil
}

// Fetch pulls the artifact and returns its first layer as a tar.gz.
func (o *OCIFetcher) Fetch(ctx context.Context, tf *infrav1.Terraform) (*Artifact, error) {
	log := ctrl.LoggerFrom(ctx)
	tag, digest, err := o.resolve(ctx, tf)
	if err != nil {
		return nil, err
	}
	opts, nameOpts, err := o.options(ctx, tf)
	if err != nil {
		return nil, err
	}
	repo, err := o.repository(tf, nameOpts)
	if err != nil {
		return nil, err
	}

	img, err := remote.Image(repo.Digest(digest.String()), opts...)
	if err != nil {
		return nil, fmt.Errorf("unable to pull %s@%s: %w", repo, digest, err)
	}
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return nil, fmt.Errorf("artifact %s@%s has no layers", repo, digest)
	}
	mt, _ := layers[0].MediaType()
	rc, err := layers[0].Compressed()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	// Accept uncompressed tar layers by extracting and re-archiving.
	if !strings.Contains(string(mt), "gzip") && !isGzip(data) {
		dir, err := os.MkdirTemp("", "tf-oci-*")
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		if err := tarball.ExtractTar(bytes.NewReader(data), dir, 0); err != nil {
			return nil, fmt.Errorf("artifact layer is not a tar archive: %w", err)
		}
		if data, err = tarball.Archive(dir, nil); err != nil {
			return nil, err
		}
	}

	revision := tag + "@" + digest.String()
	log.Info("fetched oci source", "repository", repo.String(), "revision", revision, "bytes", len(data))
	return &Artifact{Revision: revision, Data: data}, nil
}

func isGzip(b []byte) bool {
	return len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b
}
