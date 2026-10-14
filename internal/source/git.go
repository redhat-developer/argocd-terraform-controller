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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/tarball"
)

// GitFetcher fetches sources from Git repositories.
type GitFetcher struct {
	Client client.Client
	// ArgoCDNamespace enables reuse of Argo CD repository credentials.
	ArgoCDNamespace string
}

const trueValue = "true"

// gitAuth holds the resolved transport settings.
type gitAuth struct {
	method                transport.AuthMethod
	caBundle              []byte
	insecureSkipTLS       bool
	insecureIgnoreHostKey bool
}

// credentials resolves credentials for the Git source, in order: secretRef,
// Argo CD repository Secrets (when enabled), anonymous.
func (g *GitFetcher) credentials(ctx context.Context, tf *infrav1.Terraform) (*gitAuth, error) {
	src := tf.Spec.Source.Git
	if src.SecretRef != nil {
		secret := &corev1.Secret{}
		if err := g.Client.Get(ctx, types.NamespacedName{Namespace: tf.Namespace, Name: src.SecretRef.Name}, secret); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return nil, notFound("git credentials Secret %s/%s not found", tf.Namespace, src.SecretRef.Name)
			}
			return nil, err
		}
		return authFromSecretData(src.URL, secret.Data)
	}
	if src.UseArgoCDRepoCredentials {
		if g.ArgoCDNamespace == "" {
			return nil, errors.New("useArgoCDRepoCredentials is set but the controller was started without --argocd-namespace")
		}
		data, knownHosts, err := LookupArgoCDRepoCredentials(ctx, g.Client, g.ArgoCDNamespace, src.URL)
		if err != nil {
			return nil, err
		}
		if data != nil {
			return authFromArgoCDData(src.URL, data, knownHosts)
		}
		ctrl.LoggerFrom(ctx).Info("no Argo CD repository credentials matched, continuing anonymously", "url", src.URL)
	}
	return &gitAuth{}, nil
}

func isSSH(url string) bool {
	return strings.HasPrefix(url, "ssh://") || strings.HasPrefix(url, "git@") || (strings.Contains(url, "@") && !strings.Contains(url, "://"))
}

// authFromSecretData builds auth from a credentials Secret.
func authFromSecretData(url string, data map[string][]byte) (*gitAuth, error) {
	a := &gitAuth{caBundle: data["ca.crt"]}
	if isSSH(url) {
		identity, ok := data["identity"]
		if !ok {
			return nil, errors.New("ssh source requires the 'identity' key in the credentials Secret")
		}
		return a, a.setSSH(identity, data["known_hosts"], string(data["insecureIgnoreHostKey"]) == trueValue)
	}
	switch {
	case len(data["bearerToken"]) > 0:
		a.method = &githttp.TokenAuth{Token: string(data["bearerToken"])}
	case len(data["username"]) > 0 || len(data["password"]) > 0:
		a.method = &githttp.BasicAuth{Username: string(data["username"]), Password: string(data["password"])}
	}
	return a, nil
}

// authFromArgoCDData builds auth from an Argo CD repository / repo-creds
// Secret; knownHosts comes from the argocd-ssh-known-hosts-cm ConfigMap.
func authFromArgoCDData(url string, data map[string][]byte, knownHosts []byte) (*gitAuth, error) {
	a := &gitAuth{insecureSkipTLS: string(data["insecure"]) == trueValue}
	if isSSH(url) {
		key, ok := data["sshPrivateKey"]
		if !ok {
			return nil, errors.New("ssh source requires 'sshPrivateKey' in the Argo CD repository Secret")
		}
		return a, a.setSSH(key, knownHosts, string(data["insecureIgnoreHostKey"]) == trueValue)
	}
	switch {
	case len(data["bearerToken"]) > 0:
		a.method = &githttp.TokenAuth{Token: string(data["bearerToken"])}
	case len(data["username"]) > 0 || len(data["password"]) > 0:
		a.method = &githttp.BasicAuth{Username: string(data["username"]), Password: string(data["password"])}
	}
	return a, nil
}

func (a *gitAuth) setSSH(identity, knownHosts []byte, ignoreHostKey bool) error {
	keys, err := gitssh.NewPublicKeys("git", identity, "")
	if err != nil {
		return fmt.Errorf("invalid ssh identity: %w", err)
	}
	switch {
	case len(knownHosts) > 0:
		f, err := os.CreateTemp("", "known_hosts-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(f.Name()) }()
		if _, err := f.Write(knownHosts); err != nil {
			return err
		}
		_ = f.Close()
		cb, err := knownhosts.New(f.Name())
		if err != nil {
			return fmt.Errorf("invalid known_hosts: %w", err)
		}
		keys.HostKeyCallback = cb
	case ignoreHostKey:
		keys.HostKeyCallback = cryptossh.InsecureIgnoreHostKey() //nolint:gosec // opt-in by the user
	default:
		return errors.New("ssh source requires 'known_hosts' in the credentials Secret (or insecureIgnoreHostKey: \"true\")")
	}
	a.method = keys
	a.insecureIgnoreHostKey = ignoreHostKey
	return nil
}

// Resolve lists the remote and returns "<ref>@sha1:<hash>".
func (g *GitFetcher) Resolve(ctx context.Context, tf *infrav1.Terraform) (string, error) {
	src := tf.Spec.Source.Git
	ref := src.Ref
	if ref == nil {
		ref = &infrav1.GitRef{}
	}
	if ref.Commit != "" {
		name := ref.Branch
		if name == "" {
			name = "HEAD"
		}
		return fmt.Sprintf("%s@sha1:%s", name, ref.Commit), nil
	}

	auth, err := g.credentials(ctx, tf)
	if err != nil {
		return "", err
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{src.URL}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{
		Auth:            auth.method,
		CABundle:        auth.caBundle,
		InsecureSkipTLS: auth.insecureSkipTLS,
		PeelingOption:   git.AppendPeeled,
	})
	if err != nil {
		return "", fmt.Errorf("unable to list %s: %w", src.URL, err)
	}

	name, hash, err := selectRef(refs, ref)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s@sha1:%s", name, hash), nil
}

// selectRef picks the reference matching ref from a remote listing.
func selectRef(refs []*plumbing.Reference, ref *infrav1.GitRef) (string, string, error) {
	byName := make(map[plumbing.ReferenceName]*plumbing.Reference, len(refs))
	for _, r := range refs {
		byName[r.Name()] = r
	}
	// peeled returns the commit hash of a (possibly annotated) tag ref.
	peeled := func(n plumbing.ReferenceName) (plumbing.Hash, bool) {
		if p, ok := byName[plumbing.ReferenceName(n.String()+"^{}")]; ok {
			return p.Hash(), true
		}
		if r, ok := byName[n]; ok {
			return r.Hash(), true
		}
		return plumbing.ZeroHash, false
	}

	switch {
	case ref.Name != "":
		h, ok := peeled(plumbing.ReferenceName(ref.Name))
		if !ok {
			return "", "", notFound("reference %q not found", ref.Name)
		}
		return strings.TrimPrefix(strings.TrimPrefix(ref.Name, "refs/heads/"), "refs/tags/"), h.String(), nil
	case ref.SemVer != "":
		constraint, err := semver.NewConstraint(ref.SemVer)
		if err != nil {
			return "", "", fmt.Errorf("invalid semver range %q: %w", ref.SemVer, err)
		}
		var best *semver.Version
		var bestTag string
		for _, r := range refs {
			if !r.Name().IsTag() || strings.HasSuffix(r.Name().String(), "^{}") {
				continue
			}
			tag := r.Name().Short()
			v, err := semver.NewVersion(tag)
			if err != nil || !constraint.Check(v) {
				continue
			}
			if best == nil || v.GreaterThan(best) {
				best, bestTag = v, tag
			}
		}
		if best == nil {
			return "", "", notFound("no tag matches semver range %q", ref.SemVer)
		}
		h, _ := peeled(plumbing.NewTagReferenceName(bestTag))
		return bestTag, h.String(), nil
	case ref.Tag != "":
		h, ok := peeled(plumbing.NewTagReferenceName(ref.Tag))
		if !ok {
			return "", "", notFound("tag %q not found", ref.Tag)
		}
		return ref.Tag, h.String(), nil
	case ref.Branch != "":
		r, ok := byName[plumbing.NewBranchReferenceName(ref.Branch)]
		if !ok {
			return "", "", notFound("branch %q not found", ref.Branch)
		}
		return ref.Branch, r.Hash().String(), nil
	default:
		head, ok := byName[plumbing.HEAD]
		if !ok {
			return "", "", notFound("remote has no HEAD")
		}
		if head.Type() == plumbing.SymbolicReference {
			target, ok := byName[head.Target()]
			if !ok {
				return "", "", notFound("HEAD target %q not found", head.Target())
			}
			return head.Target().Short(), target.Hash().String(), nil
		}
		return "HEAD", head.Hash().String(), nil
	}
}

// Fetch clones the repository at the resolved revision and archives it.
func (g *GitFetcher) Fetch(ctx context.Context, tf *infrav1.Terraform) (*Artifact, error) {
	log := ctrl.LoggerFrom(ctx)
	src := tf.Spec.Source.Git
	revision, err := g.Resolve(ctx, tf)
	if err != nil {
		return nil, err
	}
	_, hashStr, _ := strings.Cut(revision, "@sha1:")
	hash := plumbing.NewHash(hashStr)

	auth, err := g.credentials(ctx, tf)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", "tf-source-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	cloneOpts := &git.CloneOptions{
		URL:             src.URL,
		Auth:            auth.method,
		CABundle:        auth.caBundle,
		InsecureSkipTLS: auth.insecureSkipTLS,
		NoCheckout:      true,
		Depth:           1,
		Tags:            git.NoTags,
	}
	// A pinned commit may not be the tip of its branch, so clone with full
	// history; servers generally refuse to serve arbitrary SHAs otherwise.
	if src.Ref != nil && src.Ref.Commit != "" {
		cloneOpts.Depth = 0
	}
	if src.RecurseSubmodules {
		cloneOpts.RecurseSubmodules = git.DefaultSubmoduleRecursionDepth
	}
	if ref := src.Ref; ref != nil {
		switch {
		case ref.Name != "":
			cloneOpts.ReferenceName = plumbing.ReferenceName(ref.Name)
			cloneOpts.SingleBranch = true
		case ref.Branch != "":
			cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(ref.Branch)
			cloneOpts.SingleBranch = true
		}
	}
	if ref := src.Ref; ref != nil && (ref.Tag != "" || ref.SemVer != "") {
		tag, _, _ := strings.Cut(revision, "@sha1:")
		cloneOpts.ReferenceName = plumbing.NewTagReferenceName(tag)
		cloneOpts.SingleBranch = true
	}

	repo, err := git.PlainCloneContext(ctx, dir, false, cloneOpts)
	if err != nil {
		return nil, fmt.Errorf("unable to clone %s: %w", src.URL, err)
	}

	// The listing and the clone may race with a push; make sure the resolved
	// commit is present, trying an exact-SHA fetch where the server allows it.
	if _, err := repo.CommitObject(hash); err != nil {
		log.Info("resolved commit not in clone, fetching", "commit", hash.String())
		err = repo.FetchContext(ctx, &git.FetchOptions{
			Auth:            auth.method,
			CABundle:        auth.caBundle,
			InsecureSkipTLS: auth.insecureSkipTLS,
			RefSpecs:        []config.RefSpec{config.RefSpec(fmt.Sprintf("+%s:refs/tf/pinned", hash.String()))},
			Tags:            git.NoTags,
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil, fmt.Errorf("unable to fetch commit %s: %w", hash.String(), err)
		}
	}

	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	if err := wt.Checkout(&git.CheckoutOptions{Hash: hash, Force: true}); err != nil {
		return nil, fmt.Errorf("unable to checkout %s: %w", hash.String(), err)
	}
	if src.RecurseSubmodules {
		subs, err := wt.Submodules()
		if err == nil {
			_ = subs.UpdateContext(ctx, &git.SubmoduleUpdateOptions{Init: true, Auth: auth.method, Depth: 1})
		}
	}

	data, err := tarball.Archive(dir, skipFunc(src.Ignore))
	if err != nil {
		return nil, fmt.Errorf("unable to archive source: %w", err)
	}
	log.Info("fetched git source", "url", src.URL, "revision", revision, "bytes", len(data))
	return &Artifact{Revision: revision, Data: data}, nil
}

// skipFunc returns the archive filter: always drops .git and applies the
// optional .gitignore-style patterns.
func skipFunc(ignore *string) func(rel string, info fs.FileInfo) bool {
	var patterns []gitignore.Pattern
	if ignore != nil {
		for line := range strings.SplitSeq(*ignore, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			patterns = append(patterns, gitignore.ParsePattern(line, nil))
		}
	}
	matcher := gitignore.NewMatcher(patterns)
	return func(rel string, info fs.FileInfo) bool {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if parts[0] == ".git" {
			return true
		}
		return matcher.Match(parts, info.IsDir())
	}
}
