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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SourceSpec defines where the Terraform configuration is fetched from.
// Exactly one of git or oci must be set.
// +kubebuilder:validation:XValidation:rule="has(self.git) != has(self.oci)",message="exactly one of spec.source.git or spec.source.oci must be set"
type SourceSpec struct {
	// Git fetches the configuration from a Git repository.
	// +optional
	Git *GitSource `json:"git,omitempty"`

	// OCI fetches the configuration from an OCI artifact.
	// +optional
	OCI *OCISource `json:"oci,omitempty"`

	// Interval at which the source is checked for new revisions. Defaults to
	// spec.interval.
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`
}

// GitSource is a Git repository source.
type GitSource struct {
	// URL of the Git repository (https://, ssh:// or git@ form).
	// +kubebuilder:validation:MinLength=1
	// +required
	URL string `json:"url"`

	// Ref selects the Git reference to check out. Defaults to the remote HEAD.
	// +optional
	Ref *GitRef `json:"ref,omitempty"`

	// SecretRef references a Secret in the same namespace with Git
	// credentials. Supported keys: username/password, bearerToken,
	// identity/identity.pub/known_hosts (SSH), ca.crt.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// UseArgoCDRepoCredentials looks up credentials for the URL in the Argo CD
	// repository and repo-creds Secrets (label
	// argocd.argoproj.io/secret-type) when no secretRef is given. Requires
	// the controller to be granted read access to Secrets in the Argo CD
	// namespace.
	// +optional
	UseArgoCDRepoCredentials bool `json:"useArgoCDRepoCredentials,omitempty"`

	// Ignore is a .gitignore style list of paths excluded from the artifact.
	// +optional
	Ignore *string `json:"ignore,omitempty"`

	// RecurseSubmodules checks out submodules.
	// +optional
	RecurseSubmodules bool `json:"recurseSubmodules,omitempty"`
}

// GitRef selects a Git reference. At most one of the fields should be set;
// precedence is commit, name, semver, tag, branch.
type GitRef struct {
	// +optional
	Branch string `json:"branch,omitempty"`

	// +optional
	Tag string `json:"tag,omitempty"`

	// SemVer selects the highest tag matching the semver range.
	// +optional
	SemVer string `json:"semver,omitempty"`

	// Name is a full reference name, e.g. refs/heads/main or refs/pull/1/head.
	// +optional
	Name string `json:"name,omitempty"`

	// Commit is a Git commit SHA. When Branch is also set, the branch is used
	// to narrow the fetch.
	// +optional
	Commit string `json:"commit,omitempty"`
}

// OCISource is an OCI artifact source.
type OCISource struct {
	// URL of the OCI artifact in the form oci://registry/repository.
	// +kubebuilder:validation:Pattern=`^oci://.*$`
	// +required
	URL string `json:"url"`

	// Ref selects the artifact. Defaults to the "latest" tag.
	// +optional
	Ref *OCIRef `json:"ref,omitempty"`

	// SecretRef references a Secret of type kubernetes.io/dockerconfigjson
	// with registry credentials.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// Provider enables cloud-native registry authentication.
	// +kubebuilder:validation:Enum=generic;aws;azure;gcp
	// +kubebuilder:default:=generic
	// +optional
	Provider string `json:"provider,omitempty"`

	// Insecure allows plain HTTP registries.
	// +optional
	Insecure bool `json:"insecure,omitempty"`
}

// OCIRef selects an OCI artifact. Precedence is digest, semver, tag.
type OCIRef struct {
	// +optional
	Tag string `json:"tag,omitempty"`

	// +optional
	SemVer string `json:"semver,omitempty"`

	// +optional
	Digest string `json:"digest,omitempty"`
}
