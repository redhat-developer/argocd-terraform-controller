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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NamespacedObjectReference references a Terraform object, optionally in
// another namespace.
type NamespacedObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// Namespace of the referent. Defaults to the namespace of the referring object.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// SecretKeyReference references a key of a Secret in the same namespace.
type SecretKeyReference struct {
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// +kubebuilder:validation:MinLength=1
	// +required
	Key string `json:"key"`
}

// FileMapping describes a file written into the runner from a Secret.
type FileMapping struct {
	// SecretRef references the Secret key holding the file content.
	// +required
	SecretRef SecretKeyReference `json:"secretRef"`

	// Location is either the runner home directory or the Terraform workspace.
	// +kubebuilder:validation:Enum=home;workspace
	// +required
	Location string `json:"location"`

	// Path of the file relative to the location.
	// +kubebuilder:validation:Pattern=`^(\.?[/_a-zA-Z0-9.-]{1,})*$`
	// +required
	Path string `json:"path"`
}

// BackendConfigsReference references a Secret or ConfigMap whose data are
// passed as -backend-config values.
type BackendConfigsReference struct {
	// +kubebuilder:validation:Enum=Secret;ConfigMap
	// +required
	Kind string `json:"kind"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// Keys selects specific data keys. Defaults to all keys.
	// +optional
	Keys []string `json:"keys,omitempty"`

	// Optional ignores a missing referent.
	// +optional
	Optional bool `json:"optional,omitempty"`
}

// VarsReference references a Secret or ConfigMap whose data become variables.
type VarsReference struct {
	// +kubebuilder:validation:Enum=Secret;ConfigMap
	// +required
	Kind string `json:"kind"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// VarsKeys selects specific data keys. An entry may rename the variable
	// with the form "key:var_name". Defaults to all keys.
	// +optional
	VarsKeys []string `json:"varsKeys,omitempty"`

	// Optional ignores a missing referent.
	// +optional
	Optional bool `json:"optional,omitempty"`
}

const (
	HealthCheckTypeTCP     = "tcp"
	HealthCheckTypeHTTPGet = "http"
)

// HealthCheck is performed after a successful apply.
type HealthCheck struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// Type is "tcp" (requires address) or "http" (requires url).
	// +kubebuilder:validation:Enum=tcp;http
	// +required
	Type string `json:"type"`

	// URL for http checks. Go templates may reference outputs, e.g. {{.url}}.
	// +optional
	URL string `json:"url,omitempty"`

	// Address for tcp checks. Go templates may reference outputs, e.g. {{.host}}:{{.port}}.
	// +optional
	Address string `json:"address,omitempty"`

	// Timeout of the check. Defaults to 20s.
	// +kubebuilder:default:="20s"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

// GetTimeout returns the configured or default timeout.
func (in HealthCheck) GetTimeout() time.Duration {
	if in.Timeout != nil {
		return in.Timeout.Duration
	}
	return 20 * time.Second
}

// RunnerSpec configures the runner Pod.
type RunnerSpec struct {
	// Binary selects the IaC binary: "terraform", "tofu" or "auto" (tofu if
	// available in the runner image, otherwise terraform).
	// +kubebuilder:default:=auto
	// +optional
	Binary BinaryEnum `json:"binary,omitempty"`

	// Image overrides the runner Pod image.
	// +optional
	Image string `json:"image,omitempty"`

	// ServiceAccountName used by the runner Pod. Defaults to tf-runner.
	// +kubebuilder:default:=tf-runner
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// AlwaysCleanup deletes the runner Pod after each reconciliation.
	// +kubebuilder:default:=true
	// +optional
	AlwaysCleanup *bool `json:"alwaysCleanup,omitempty"`

	// TerminationGracePeriodSeconds of the runner Pod.
	// +kubebuilder:default:=30
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// PodTemplate customises the runner Pod.
	// +optional
	PodTemplate RunnerPodTemplate `json:"podTemplate,omitempty"`
}

// GetAlwaysCleanup returns whether the runner Pod is removed after each run.
func (in *RunnerSpec) GetAlwaysCleanup() bool {
	return in.AlwaysCleanup == nil || *in.AlwaysCleanup
}

// GetServiceAccountName returns the runner service account.
func (in *RunnerSpec) GetServiceAccountName() string {
	if in.ServiceAccountName == "" {
		return DefaultRunnerSA
	}
	return in.ServiceAccountName
}

// GetBinary returns the configured binary selection.
func (in *RunnerSpec) GetBinary() BinaryEnum {
	if in.Binary == "" {
		return BinaryAuto
	}
	return in.Binary
}

// RunnerPodTemplate customises the runner Pod.
type RunnerPodTemplate struct {
	// +optional
	Metadata RunnerPodMetadata `json:"metadata,omitempty"`

	// +optional
	Spec RunnerPodSpec `json:"spec,omitempty"`
}

// RunnerPodMetadata holds extra labels and annotations for the runner Pod.
type RunnerPodMetadata struct {
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// RunnerPodSpec holds the customisable parts of the runner Pod spec.
type RunnerPodSpec struct {
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	Env []corev1.EnvVar `json:"env,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// +optional
	InitContainers []corev1.Container `json:"initContainers,omitempty"`

	// +optional
	HostAliases []corev1.HostAlias `json:"hostAliases,omitempty"`

	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`

	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// ResourceInventory lists Terraform resources applied by this object.
type ResourceInventory struct {
	// +required
	Entries []ResourceRef `json:"entries"`
}

// ResourceRef locates a Terraform-managed resource.
type ResourceRef struct {
	// Name is the Terraform resource address.
	Name string `json:"n"`

	// Type is the Terraform resource type.
	Type string `json:"t"`

	// Identifier is the provider-specific ID (e.g. an ARN).
	Identifier string `json:"id"`
}
