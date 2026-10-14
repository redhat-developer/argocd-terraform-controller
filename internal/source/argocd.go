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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ArgoCDSecretTypeLabel is the label Argo CD puts on credential Secrets.
	ArgoCDSecretTypeLabel = "argocd.argoproj.io/secret-type"
	argoCDTypeRepository  = "repository"
	argoCDTypeRepoCreds   = "repo-creds"
	// ArgoCDKnownHostsConfigMap holds Argo CD's SSH known hosts.
	ArgoCDKnownHostsConfigMap = "argocd-ssh-known-hosts-cm"
	argoCDKnownHostsKey       = "ssh_known_hosts"
)

// NormalizeGitURL makes URLs comparable the way Argo CD does: lower-cased
// scheme/host, no trailing slash or ".git", and scp-style ssh URLs rewritten
// to ssh://.
func NormalizeGitURL(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	// git@host:org/repo -> ssh://git@host/org/repo
	if !strings.Contains(u, "://") {
		if userHost, path, ok := strings.Cut(u, ":"); ok && strings.Contains(userHost, "@") {
			u = "ssh://" + userHost + "/" + strings.TrimPrefix(path, "/")
		}
	}
	if scheme, rest, ok := strings.Cut(u, "://"); ok {
		host, path, hasPath := strings.Cut(rest, "/")
		u = strings.ToLower(scheme) + "://" + strings.ToLower(host)
		if hasPath {
			u += "/" + path
		}
	}
	return u
}

// LookupArgoCDRepoCredentials finds the Argo CD credentials for url: an
// exact "repository" Secret match wins, otherwise the longest "repo-creds"
// URL prefix. It returns nil data when nothing matches. The SSH known hosts
// of the Argo CD installation are returned alongside.
func LookupArgoCDRepoCredentials(ctx context.Context, c client.Client, namespace, url string) (map[string][]byte, []byte, error) {
	want := NormalizeGitURL(url)

	list := &corev1.SecretList{}
	if err := c.List(ctx, list, client.InNamespace(namespace), client.HasLabels{ArgoCDSecretTypeLabel}); err != nil {
		return nil, nil, err
	}

	var best *corev1.Secret
	bestLen := -1
	for i := range list.Items {
		s := &list.Items[i]
		repoURL := NormalizeGitURL(string(s.Data["url"]))
		if repoURL == "" {
			continue
		}
		switch s.Labels[ArgoCDSecretTypeLabel] {
		case argoCDTypeRepository:
			if repoURL == want {
				best = s
				bestLen = len(repoURL) + 1 // exact match beats any prefix
			}
		case argoCDTypeRepoCreds:
			if strings.HasPrefix(want, repoURL) && len(repoURL) > bestLen {
				best, bestLen = s, len(repoURL)
			}
		}
	}
	if best == nil {
		return nil, nil, nil
	}

	var knownHosts []byte
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ArgoCDKnownHostsConfigMap}, cm); err == nil {
		knownHosts = []byte(cm.Data[argoCDKnownHostsKey])
	}
	return best.Data, knownHosts, nil
}
