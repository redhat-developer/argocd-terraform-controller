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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/tarball"
)

func TestNormalizeGitURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://GitHub.com/Org/Repo.git/": "https://github.com/Org/Repo",
		"git@github.com:org/repo.git":      "ssh://git@github.com/org/repo",
		"ssh://git@github.com/org/repo":    "ssh://git@github.com/org/repo",
		"https://example.com":              "https://example.com",
	} {
		if got := NormalizeGitURL(in); got != want {
			t.Errorf("NormalizeGitURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupArgoCDRepoCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	secret := func(name, typ, url string, extra map[string]string) *corev1.Secret {
		data := map[string][]byte{"url": []byte(url)}
		for k, v := range extra {
			data[k] = []byte(v)
		}
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd", Labels: map[string]string{ArgoCDSecretTypeLabel: typ}},
			Data:       data,
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		secret("exact", "repository", "https://github.com/org/repo.git", map[string]string{"password": "exact"}),
		secret("org-creds", "repo-creds", "https://github.com/org", map[string]string{"password": "org"}),
		secret("gh-creds", "repo-creds", "https://github.com", map[string]string{"password": "gh"}),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ArgoCDKnownHostsConfigMap, Namespace: "argocd"},
			Data: map[string]string{"ssh_known_hosts": "github.com ssh-ed25519 AAAA"}},
	).Build()

	for url, want := range map[string]string{
		"https://github.com/org/repo":     "exact",
		"https://github.com/org/other":    "org",
		"https://github.com/someone/else": "gh",
		"https://gitlab.com/org/repo":     "",
	} {
		data, kh, err := LookupArgoCDRepoCredentials(context.Background(), c, "argocd", url)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if data != nil {
			got = string(data["password"])
			if len(kh) == 0 {
				t.Errorf("%s: expected known hosts", url)
			}
		}
		if got != want {
			t.Errorf("%s: matched %q, want %q", url, got, want)
		}
	}
}

func TestSelectRef(t *testing.T) {
	h := func(s string) plumbing.Hash { return plumbing.NewHash(strings.Repeat(s, 40)) }
	refs := []*plumbing.Reference{
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")),
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), h("a")),
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("dev"), h("b")),
		plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.0.0"), h("c")),
		plumbing.NewHashReference(plumbing.NewTagReferenceName("v1.2.0"), h("d")), // annotated
		plumbing.NewHashReference(plumbing.ReferenceName("refs/tags/v1.2.0^{}"), h("e")),
		plumbing.NewHashReference(plumbing.NewTagReferenceName("v2.0.0"), h("f")),
		plumbing.NewHashReference(plumbing.NewTagReferenceName("not-semver"), h("0")),
	}
	tests := []struct {
		name     string
		ref      infrav1.GitRef
		wantName string
		wantHash string
		wantErr  bool
	}{
		{"default HEAD", infrav1.GitRef{}, "main", h("a").String(), false},
		{"branch", infrav1.GitRef{Branch: "dev"}, "dev", h("b").String(), false},
		{"tag lightweight", infrav1.GitRef{Tag: "v1.0.0"}, "v1.0.0", h("c").String(), false},
		{"tag annotated is peeled", infrav1.GitRef{Tag: "v1.2.0"}, "v1.2.0", h("e").String(), false},
		{"semver range", infrav1.GitRef{SemVer: "^1.0.0"}, "v1.2.0", h("e").String(), false},
		{"semver any", infrav1.GitRef{SemVer: ">=1.0.0"}, "v2.0.0", h("f").String(), false},
		{"name", infrav1.GitRef{Name: "refs/heads/dev"}, "dev", h("b").String(), false},
		{"missing branch", infrav1.GitRef{Branch: "nope"}, "", "", true},
		{"no semver match", infrav1.GitRef{SemVer: "^9.0.0"}, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, hash, err := selectRef(refs, &tt.ref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if name != tt.wantName || hash != tt.wantHash {
				t.Errorf("got %s %s, want %s %s", name, hash, tt.wantName, tt.wantHash)
			}
		})
	}
}

// initRepo creates a local repository with two commits on main and a tag.
func initRepo(t *testing.T) (string, plumbing.Hash, plumbing.Hash) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: plumbing.Main}})
	if err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	commit := func(file, content, msg string) plumbing.Hash {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add("."); err != nil {
			t.Fatal(err)
		}
		h, err := wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	first := commit("infra/main.tf", `resource "terraform_data" "a" {}`, "first")
	if _, err := repo.CreateTag("v0.1.0", first, nil); err != nil {
		t.Fatal(err)
	}
	second := commit("infra/vars.tf", `variable "x" {}`, "second")
	_ = commit("secrets.tfvars", "x = 1", "third")
	return dir, first, second
}

func TestGitFetcherLocalRepo(t *testing.T) {
	dir, first, _ := initRepo(t)
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	g := &GitFetcher{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	ctx := context.Background()

	ignore := "*.tfvars"
	tf := &infrav1.Terraform{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: infrav1.TerraformSpec{Source: infrav1.SourceSpec{Git: &infrav1.GitSource{
			URL: dir, Ref: &infrav1.GitRef{Branch: "main"}, Ignore: &ignore,
		}}},
	}

	rev, err := g.Resolve(ctx, tf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rev, "main@sha1:") {
		t.Errorf("revision = %q", rev)
	}

	art, err := g.Fetch(ctx, tf)
	if err != nil {
		t.Fatal(err)
	}
	if art.Revision != rev {
		t.Errorf("artifact revision %q != resolved %q", art.Revision, rev)
	}
	out := t.TempDir()
	if err := tarball.Extract(bytes.NewReader(art.Data), out, 0); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"infra/main.tf", "infra/vars.tf"} {
		if _, err := os.Stat(filepath.Join(out, want)); err != nil {
			t.Errorf("missing %s", want)
		}
	}
	for _, unwanted := range []string{"secrets.tfvars", ".git"} {
		if _, err := os.Stat(filepath.Join(out, unwanted)); err == nil {
			t.Errorf("%s should be excluded", unwanted)
		}
	}

	// Pin to the first commit: vars.tf must not exist.
	tf.Spec.Source.Git.Ref = &infrav1.GitRef{Commit: first.String()}
	art, err = g.Fetch(ctx, tf)
	if err != nil {
		t.Fatal(err)
	}
	if art.Revision != "HEAD@sha1:"+first.String() {
		t.Errorf("pinned revision = %q", art.Revision)
	}
	out = t.TempDir()
	if err := tarball.Extract(bytes.NewReader(art.Data), out, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "infra/vars.tf")); err == nil {
		t.Error("vars.tf should not exist at the first commit")
	}

	// Tag and semver.
	tf.Spec.Source.Git.Ref = &infrav1.GitRef{Tag: "v0.1.0"}
	if rev, err = g.Resolve(ctx, tf); err != nil || rev != "v0.1.0@sha1:"+first.String() {
		t.Errorf("tag revision = %q err=%v", rev, err)
	}
	tf.Spec.Source.Git.Ref = &infrav1.GitRef{SemVer: ">=0.0.1"}
	if rev, err = g.Resolve(ctx, tf); err != nil || rev != "v0.1.0@sha1:"+first.String() {
		t.Errorf("semver revision = %q err=%v", rev, err)
	}

	// Manager caches by revision.
	m := NewManager(g.Client, Options{})
	tf.Spec.Source.Git.Ref = &infrav1.GitRef{Tag: "v0.1.0"}
	a1, err := m.Fetch(ctx, tf)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := m.Fetch(ctx, tf)
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Error("expected cached artifact on unchanged revision")
	}
}

func TestGitCredentialsMissingSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	g := &GitFetcher{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	tf := &infrav1.Terraform{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: infrav1.TerraformSpec{Source: infrav1.SourceSpec{Git: &infrav1.GitSource{
			URL: "https://example.com/repo.git", SecretRef: &corev1.LocalObjectReference{Name: "missing"},
		}}},
	}
	_, err := g.Resolve(context.Background(), tf)
	var nf *NotFoundError
	if err == nil || !errorsAs(err, &nf) {
		t.Fatalf("expected NotFoundError, got %v", err)
	}
}

func TestAuthFromSecretData(t *testing.T) {
	a, err := authFromSecretData("https://example.com/x", map[string][]byte{"username": []byte("u"), "password": []byte("p"), "ca.crt": []byte("CA")})
	if err != nil || a.method == nil || string(a.caBundle) != "CA" {
		t.Fatalf("basic auth: %+v %v", a, err)
	}
	a, err = authFromSecretData("https://example.com/x", map[string][]byte{"bearerToken": []byte("t")})
	if err != nil || a.method.Name() != "http-token-auth" {
		t.Fatalf("token auth: %+v %v", a, err)
	}
	if _, err := authFromSecretData("git@example.com:x/y", map[string][]byte{}); err == nil {
		t.Error("ssh without identity must fail")
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
