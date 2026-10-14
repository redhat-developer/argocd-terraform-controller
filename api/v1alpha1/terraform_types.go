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
	"bytes"
	"fmt"
	"math"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

const (
	// TerraformKind is the kind name of the Terraform resource.
	TerraformKind = "Terraform"
	// TerraformFinalizer is the finalizer added to Terraform objects.
	TerraformFinalizer = "finalizers.infra.argocd.io"
	// DependencyFinalizerPrefix is the prefix of finalizers added to a Terraform
	// object by other Terraform objects that depend on it.
	DependencyFinalizerPrefix = "dependency.infra.argocd.io/"

	// CACertSecretName is the Secret in the controller namespace holding the CA
	// used to issue runner certificates.
	CACertSecretName = "argocd-terraform-controller.tls"
	// RunnerTLSSecretName is the Secret written to every namespace in which a
	// runner Pod is created, holding the runner server certificate.
	RunnerTLSSecretName = "terraform-runner.tls"

	// RunnerLabel is set on runner Pods and carries the Terraform namespace.
	RunnerLabel = "infra.argocd.io/terraform"
	// RunnerTLSSecretLabel is set on runner Pods and carries the TLS Secret name.
	RunnerTLSSecretLabel = "infra.argocd.io/tls-secret-name"
	// HasTerraformLabel may be set on an Argo CD Application to force the UI
	// extension to show the Terraform view.
	HasTerraformLabel = "infra.argocd.io/has-terraform"

	// MaxConditionMessageLength caps condition messages.
	MaxConditionMessageLength = 20000

	ApprovePlanAutoValue    = "auto"
	ApprovePlanDisableValue = "disable"
	DefaultWorkspaceName    = "default"
	DefaultRunnerSA         = "tf-runner"
)

// BinaryEnum selects the IaC binary run by the runner.
// +kubebuilder:validation:Enum=auto;terraform;tofu
type BinaryEnum string

const (
	// BinaryAuto picks tofu if present in the runner image, otherwise terraform.
	BinaryAuto      BinaryEnum = "auto"
	BinaryTerraform BinaryEnum = "terraform"
	BinaryTofu      BinaryEnum = "tofu"
)

// ForceUnlockEnum controls force-unlocking of the state.
// +kubebuilder:validation:Enum=yes;no;auto
type ForceUnlockEnum string

const (
	ForceUnlockEnumAuto ForceUnlockEnum = "auto"
	ForceUnlockEnumYes  ForceUnlockEnum = "yes"
	ForceUnlockEnumNo   ForceUnlockEnum = "no"
)

// RetryStrategyEnum selects the requeue strategy after failures.
// +kubebuilder:validation:Enum=StaticInterval;ExponentialBackoff
type RetryStrategyEnum string

const (
	StaticInterval     RetryStrategyEnum = "StaticInterval"
	ExponentialBackoff RetryStrategyEnum = "ExponentialBackoff"
)

// StoreReadablePlanEnum selects how a human/machine readable copy of the plan is stored.
// +kubebuilder:validation:Enum=none;json;human
type StoreReadablePlanEnum string

const (
	StoreReadablePlanNone  StoreReadablePlanEnum = "none"
	StoreReadablePlanJSON  StoreReadablePlanEnum = "json"
	StoreReadablePlanHuman StoreReadablePlanEnum = "human"
)

// WriteOutputsToSecretSpec defines where to store outputs, and which outputs to be stored.
type WriteOutputsToSecretSpec struct {
	// Name is the name of the Secret to be written.
	// +required
	Name string `json:"name"`

	// Labels to add to the outputted Secret.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations to add to the outputted Secret.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Outputs contain the selected names of outputs to be written to the
	// Secret. An empty list writes all outputs. An entry may be renamed with
	// the form "output_name:secret_key".
	// +optional
	Outputs []string `json:"outputs,omitempty"`
}

// Variable is an input variable for the Terraform program.
type Variable struct {
	// Name is the name of the variable.
	// +required
	Name string `json:"name"`

	// Value is the literal value of the variable (any JSON type).
	// +optional
	Value *apiextensionsv1.JSON `json:"value,omitempty"`

	// ValueFrom sources the value from a Secret or ConfigMap key.
	// +optional
	ValueFrom *corev1.EnvVarSource `json:"valueFrom,omitempty"`
}

// TerraformSpec defines the desired state of Terraform.
type TerraformSpec struct {
	// Source defines where the Terraform configuration is fetched from.
	// +required
	Source SourceSpec `json:"source"`

	// Path to the directory containing the Terraform (.tf) files, relative
	// to the root of the source. Defaults to the root of the source.
	// +optional
	Path string `json:"path,omitempty"`

	// The interval at which to reconcile the Terraform.
	// +required
	Interval metav1.Duration `json:"interval"`

	// The interval at which to retry a previously failed reconciliation.
	// Defaults to 15s when not specified.
	// +optional
	RetryInterval *metav1.Duration `json:"retryInterval,omitempty"`

	// The strategy to use when retrying a previously failed reconciliation.
	// ExponentialBackoff uses 2^reconciliationFailures * RetryInterval capped
	// at MaxRetryInterval.
	// +kubebuilder:default:=StaticInterval
	// +optional
	RetryStrategy RetryStrategyEnum `json:"retryStrategy,omitempty"`

	// The maximum requeue duration after a failed reconciliation. Only used
	// with the ExponentialBackoff strategy. Defaults to 24h.
	// +optional
	MaxRetryInterval *metav1.Duration `json:"maxRetryInterval,omitempty"`

	// ApprovePlan specifies the plan ID to approve. "auto" approves every
	// plan, "disable" never plans or applies, and the empty string requires a
	// manual approval of each plan.
	// +optional
	ApprovePlan string `json:"approvePlan,omitempty"`

	// PlanOnly stops the reconciliation at the plan phase.
	// +optional
	PlanOnly bool `json:"planOnly,omitempty"`

	// Destroy produces a destroy plan. Applying the plan destroys all resources.
	// +optional
	Destroy bool `json:"destroy,omitempty"`

	// DestroyResourcesOnDeletion creates and applies a destroy plan when this
	// object is deleted. Defaults to false.
	// +kubebuilder:default:=false
	// +optional
	DestroyResourcesOnDeletion bool `json:"destroyResourcesOnDeletion,omitempty"`

	// Suspend tells the controller to suspend subsequent executions. It does
	// not interrupt an execution that already started.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Force instructs the controller to unconditionally re-plan and re-apply.
	// +kubebuilder:default:=false
	// +optional
	Force bool `json:"force,omitempty"`

	// Workspace is the Terraform workspace to use.
	// +kubebuilder:default:=default
	// +optional
	Workspace string `json:"workspace,omitempty"`

	// Vars is a list of input variables for the Terraform program.
	// +optional
	Vars []Variable `json:"vars,omitempty"`

	// VarsFrom references Secrets or ConfigMaps whose data become variables.
	// Later entries override earlier ones for the same key.
	// +optional
	VarsFrom []VarsReference `json:"varsFrom,omitempty"`

	// Values maps to the Terraform variable "values", an object of arbitrary
	// values. The Terraform configuration must declare `variable "values"`.
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// TfVarsFiles loads the given .tfvars files (like -var-file).
	// +optional
	TfVarsFiles []string `json:"tfVarsFiles,omitempty"`

	// FileMappings is a list of files to create in the runner before init.
	// +optional
	FileMappings []FileMapping `json:"fileMappings,omitempty"`

	// BackendConfig configures the state backend. When omitted the Kubernetes
	// backend is used with a Secret in the namespace of this object.
	// +optional
	BackendConfig *BackendConfigSpec `json:"backendConfig,omitempty"`

	// BackendConfigsFrom references Secrets or ConfigMaps whose data are
	// passed as -backend-config key/value pairs.
	// +optional
	BackendConfigsFrom []BackendConfigsReference `json:"backendConfigsFrom,omitempty"`

	// Cloud configures the Terraform Cloud / Enterprise `cloud {}` block.
	// Setting it disables the Kubernetes backend.
	// +optional
	Cloud *CloudSpec `json:"cloud,omitempty"`

	// TFState configures state lock handling and state lifecycle.
	// +optional
	TFState *TFStateSpec `json:"tfstate,omitempty"`

	// CliConfigSecretRef references a Secret with a Terraform CLI config
	// (e.g. private registry credentials) mounted as .terraformrc.
	// +optional
	CliConfigSecretRef *corev1.SecretReference `json:"cliConfigSecretRef,omitempty"`

	// WriteOutputsToSecret writes Terraform outputs to a Secret after apply.
	// +optional
	WriteOutputsToSecret *WriteOutputsToSecretSpec `json:"writeOutputsToSecret,omitempty"`

	// HealthChecks is a list of checks performed after a successful apply.
	// +optional
	HealthChecks []HealthCheck `json:"healthChecks,omitempty"`

	// DisableDriftDetection disables the periodic refresh-only plan used to
	// detect drift. Defaults to false.
	// +kubebuilder:default:=false
	// +optional
	DisableDriftDetection bool `json:"disableDriftDetection,omitempty"`

	// DependsOn lists Terraform objects that must be Ready before this one is
	// reconciled, and that cannot be deleted while this one exists.
	// +optional
	DependsOn []NamespacedObjectReference `json:"dependsOn,omitempty"`

	// Runner configures the runner Pod that executes terraform / tofu.
	// +kubebuilder:default:={}
	// +optional
	Runner RunnerSpec `json:"runner,omitempty"`

	// UpgradeOnInit passes -upgrade to init. Defaults to true.
	// +kubebuilder:default:=true
	// +optional
	UpgradeOnInit *bool `json:"upgradeOnInit,omitempty"`

	// RefreshBeforeApply forces a state refresh before apply.
	// +kubebuilder:default:=false
	// +optional
	RefreshBeforeApply bool `json:"refreshBeforeApply,omitempty"`

	// EnableInventory stores the applied resources in status.inventory.
	// +optional
	EnableInventory bool `json:"enableInventory,omitempty"`

	// Targets limits plan/apply to the given resource addresses.
	// +optional
	Targets []string `json:"targets,omitempty"`

	// Parallelism limits the number of concurrent operations. 0 uses the
	// Terraform default.
	// +kubebuilder:default:=0
	// +optional
	Parallelism int32 `json:"parallelism,omitempty"`

	// StoreReadablePlan stores a readable copy of the plan: "human" writes a
	// ConfigMap (readable through the Argo CD UI), "json" writes a Secret.
	// +kubebuilder:default:=human
	// +optional
	StoreReadablePlan StoreReadablePlanEnum `json:"storeReadablePlan,omitempty"`

	// Remediation configures how many failures are tolerated before the
	// controller stops retrying.
	// +optional
	Remediation *Remediation `json:"remediation,omitempty"`
}

// Remediation configures retry limits.
type Remediation struct {
	// Retries is the number of retries attempted on failures before giving up.
	// 0 (default) means no retries; a negative value means unlimited retries.
	// +optional
	Retries int64 `json:"retries,omitempty"`
}

// CloudSpec configures Terraform Cloud / Enterprise.
type CloudSpec struct {
	// +required
	Organization string `json:"organization"`

	// +required
	Workspaces *CloudWorkspacesSpec `json:"workspaces"`

	// +optional
	Hostname string `json:"hostname,omitempty"`

	// +optional
	Token string `json:"token,omitempty"`
}

// CloudWorkspacesSpec selects Terraform Cloud workspaces.
type CloudWorkspacesSpec struct {
	// +optional
	Name string `json:"name,omitempty"`

	// +optional
	Tags []string `json:"tags,omitempty"`
}

// BackendConfigSpec configures the state backend.
type BackendConfigSpec struct {
	// Disable completely disables backend configuration generation.
	// +optional
	Disable bool `json:"disable,omitempty"`

	// SecretSuffix overrides the Kubernetes backend secret_suffix. Defaults to
	// the name of the Terraform object.
	// +optional
	SecretSuffix string `json:"secretSuffix,omitempty"`

	// InClusterConfig sets in_cluster_config for the Kubernetes backend.
	// Defaults to true when no explicit backendConfig is given.
	// +optional
	InClusterConfig bool `json:"inClusterConfig,omitempty"`

	// CustomConfiguration is inserted verbatim inside the `terraform {}` block
	// and replaces the generated Kubernetes backend, e.g. `backend "s3" {...}`.
	// +optional
	CustomConfiguration string `json:"customConfiguration,omitempty"`

	// ConfigPath sets config_path for the Kubernetes backend.
	// +optional
	ConfigPath string `json:"configPath,omitempty"`

	// Labels are added to the state Secret by the Kubernetes backend.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// TFStateSpec configures state lock handling and state lifecycle.
type TFStateSpec struct {
	// ForceUnlock a Terraform state that has become locked. Defaults to `no`.
	// `yes` unlocks only when LockIdentifier matches the pending lock; `auto`
	// unlocks whatever lock is pending.
	//
	// WARNING: Only use `auto` when you are certain no other process is using
	// this state.
	// +kubebuilder:default:=no
	// +optional
	ForceUnlock ForceUnlockEnum `json:"forceUnlock,omitempty"`

	// LockIdentifier is the lock ID required by Terraform to force-unlock the
	// state, e.g. `f2ab685b-f84d-ac0b-a125-378a22877e8d`.
	// +optional
	LockIdentifier string `json:"lockIdentifier,omitempty"`

	// LockTimeout instructs Terraform to retry acquiring the state lock for
	// this duration before failing. Defaults to 0s (no retry).
	// +kubebuilder:default:="0s"
	// +optional
	LockTimeout metav1.Duration `json:"lockTimeout,omitempty"`

	// DeleteOnDestroy deletes the Kubernetes backend state Secret and its lock
	// Lease after a successful destroy triggered by deletion of this object
	// (requires destroyResourcesOnDeletion). Only applies to the default
	// Kubernetes backend. Defaults to false.
	// +kubebuilder:default:=false
	// +optional
	DeleteOnDestroy bool `json:"deleteOnDestroy,omitempty"`
}

// PlanStatus holds the plan bookkeeping.
type PlanStatus struct {
	// LastApplied is the ID of the last applied plan.
	// +optional
	LastApplied string `json:"lastApplied,omitempty"`

	// Pending is the ID of the plan awaiting approval or apply.
	// +optional
	Pending string `json:"pending,omitempty"`

	// +optional
	IsDestroyPlan bool `json:"isDestroyPlan,omitempty"`

	// +optional
	IsDriftDetectionPlan bool `json:"isDriftDetectionPlan,omitempty"`
}

// LockStatus defines the observed state of a Terraform state lock.
type LockStatus struct {
	// +optional
	LastApplied string `json:"lastApplied,omitempty"`

	// Pending holds the identifier of the lock holder, to be used with force unlock.
	// +optional
	Pending string `json:"pending,omitempty"`
}

// TerraformStatus defines the observed state of Terraform.
type TerraformStatus struct {
	// ObservedGeneration is the last reconciled generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastHandledReconcileAt holds the value of the most recent reconcile
	// request annotation, used to detect manual reconcile requests.
	// +optional
	LastHandledReconcileAt string `json:"lastHandledReconcileAt,omitempty"`

	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastAppliedRevision is the last successfully applied source revision,
	// in the form <ref>@sha1:<checksum>.
	// +optional
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`

	// LastAttemptedRevision is the revision of the last reconciliation attempt.
	// +optional
	LastAttemptedRevision string `json:"lastAttemptedRevision,omitempty"`

	// LastPlannedRevision is the revision used by the last plan.
	// +optional
	LastPlannedRevision string `json:"lastPlannedRevision,omitempty"`

	// LastPlannedGeneration is the generation at the time of the last plan.
	// +optional
	LastPlannedGeneration int64 `json:"lastPlannedGeneration,omitempty"`

	// +optional
	LastPlanAt *metav1.Time `json:"lastPlanAt,omitempty"`

	// +optional
	LastSuccessfulReconcileAt *metav1.Time `json:"lastSuccessfulReconcileAt,omitempty"`

	// +optional
	LastDriftDetectedAt *metav1.Time `json:"lastDriftDetectedAt,omitempty"`

	// +optional
	LastAppliedByDriftDetectionAt *metav1.Time `json:"lastAppliedByDriftDetectionAt,omitempty"`

	// AvailableOutputs lists the names of the outputs of the last apply.
	// +optional
	AvailableOutputs []string `json:"availableOutputs,omitempty"`

	// +optional
	Plan PlanStatus `json:"plan,omitempty"`

	// Inventory lists the Terraform resources applied, when enableInventory is set.
	// +optional
	Inventory *ResourceInventory `json:"inventory,omitempty"`

	// +optional
	Lock LockStatus `json:"lock,omitempty"`

	// ReconciliationFailures counts failures since the last success.
	// +optional
	ReconciliationFailures int64 `json:"reconciliationFailures,omitempty"`

	// Binary is the IaC binary used by the last run, e.g. "tofu 1.9.0".
	// +optional
	Binary string `json:"binary,omitempty"`

	// StateSecretName is the Kubernetes backend state Secret, when used.
	// +optional
	StateSecretName string `json:"stateSecretName,omitempty"`

	// PlanConfigMapName is the ConfigMap holding the human readable plan, when
	// storeReadablePlan is "human".
	// +optional
	PlanConfigMapName string `json:"planConfigMapName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=tf
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Plan",type="string",JSONPath=".status.plan.pending"
// +kubebuilder:printcolumn:name="Applied",type="string",JSONPath=".status.lastAppliedRevision",priority=1
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Terraform is the Schema for the terraforms API.
type Terraform struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec TerraformSpec `json:"spec"`
	// +kubebuilder:default={"observedGeneration":-1}
	// +optional
	Status TerraformStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TerraformList contains a list of Terraform.
type TerraformList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Terraform `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Terraform{}, &TerraformList{})
		metav1.AddToGroupVersion(s, SchemeGroupVersion)
		return nil
	})
}

// WorkspaceName returns the configured workspace or "default".
func (in *Terraform) WorkspaceName() string {
	if in.Spec.Workspace != "" {
		return in.Spec.Workspace
	}
	return DefaultWorkspaceName
}

// StateSecretSuffix returns the Kubernetes backend secret_suffix.
func (in *Terraform) StateSecretSuffix() string {
	if in.Spec.BackendConfig != nil && in.Spec.BackendConfig.SecretSuffix != "" {
		return in.Spec.BackendConfig.SecretSuffix
	}
	return in.Name
}

// StateSecretName returns the name of the Kubernetes backend state Secret
// (tfstate-<workspace>-<suffix>), following the naming of the Terraform
// kubernetes backend.
func (in *Terraform) StateSecretName() string {
	return fmt.Sprintf("tfstate-%s-%s", in.WorkspaceName(), in.StateSecretSuffix())
}

// StateLockLeaseName returns the Lease used by the Kubernetes backend for locking.
func (in *Terraform) StateLockLeaseName() string {
	return "lock-" + in.StateSecretName()
}

// PlanSecretName returns the Secret holding the binary plan.
func (in *Terraform) PlanSecretName() string {
	return fmt.Sprintf("tfplan-%s-%s", in.WorkspaceName(), in.Name)
}

// ReadablePlanName returns the ConfigMap/Secret holding the readable plan.
func (in *Terraform) ReadablePlanName() string {
	return in.PlanSecretName() + "-readable"
}

// RunnerPodName returns the name of the runner Pod for this object.
func (in *Terraform) RunnerPodName() string {
	return in.Name + "-tf-runner"
}

// UsesKubernetesBackend reports whether state is stored in the default
// Kubernetes backend Secret.
func (in *Terraform) UsesKubernetesBackend() bool {
	if in.Spec.Cloud != nil {
		return false
	}
	if in.Spec.BackendConfig == nil {
		return true
	}
	return !in.Spec.BackendConfig.Disable && in.Spec.BackendConfig.CustomConfiguration == ""
}

// GetRetryInterval returns the requeue interval after a failure.
func (in *Terraform) GetRetryInterval() time.Duration {
	retryInterval := 15 * time.Second
	if in.Spec.RetryInterval != nil {
		retryInterval = in.Spec.RetryInterval.Duration
	}

	if in.Spec.RetryStrategy == ExponentialBackoff {
		retryInterval *= time.Duration(math.Pow(2, float64(in.Status.ReconciliationFailures)))
		maxRetryInterval := 24 * time.Hour
		if in.Spec.MaxRetryInterval != nil {
			maxRetryInterval = in.Spec.MaxRetryInterval.Duration
		}
		if retryInterval > maxRetryInterval {
			return maxRetryInterval
		}
	}

	return retryInterval
}

// GetRetries returns the configured retry limit.
func (in *Terraform) GetRetries() int64 {
	if in.Spec.Remediation == nil {
		return 0
	}
	return in.Spec.Remediation.Retries
}

// ShouldRetry reports whether another attempt is allowed.
func (in *Terraform) ShouldRetry() bool {
	if in.Spec.Remediation == nil || in.Spec.Remediation.Retries < 0 {
		return true
	}
	return in.Status.ReconciliationFailures < in.Spec.Remediation.Retries
}

// IncrementReconciliationFailures bumps the failure counter.
func (in *Terraform) IncrementReconciliationFailures() {
	in.Status.ReconciliationFailures++
}

// GetUpgradeOnInit returns whether init runs with -upgrade.
func (in *TerraformSpec) GetUpgradeOnInit() bool {
	return in.UpgradeOnInit == nil || *in.UpgradeOnInit
}

// GetConditions returns the status conditions of the object.
func (in *Terraform) GetConditions() []metav1.Condition {
	return in.Status.Conditions
}

// SetConditions sets the status conditions on the object.
func (in *Terraform) SetConditions(conditions []metav1.Condition) {
	in.Status.Conditions = conditions
}

// ToBytes serialises the object for transfer to the runner.
func (in Terraform) ToBytes(scheme *runtime.Scheme) ([]byte, error) {
	return runtime.Encode(
		serializer.NewCodecFactory(scheme).LegacyCodec(corev1.SchemeGroupVersion, GroupVersion), &in)
}

// FromBytes deserialises an object transferred from the controller.
func (in *Terraform) FromBytes(b []byte, scheme *runtime.Scheme) error {
	return runtime.DecodeInto(
		serializer.NewCodecFactory(scheme).LegacyCodec(corev1.SchemeGroupVersion, GroupVersion), b, in)
}

// IsValid reports whether the cloud block has the required fields.
func (c *CloudSpec) IsValid() bool {
	if c.Organization == "" || c.Workspaces == nil {
		return false
	}
	return c.Workspaces.Name != "" || len(c.Workspaces.Tags) > 0
}

// ToHCL renders the `terraform { cloud {} }` block.
func (c *CloudSpec) ToHCL() string {
	var buf bytes.Buffer
	buf.WriteString("terraform {\n")
	buf.WriteString("  cloud {\n")
	fmt.Fprintf(&buf, "    organization = %q\n", c.Organization)
	buf.WriteString("    workspaces {\n")
	if c.Workspaces.Name != "" {
		fmt.Fprintf(&buf, "      name = %q\n", c.Workspaces.Name)
	}
	if len(c.Workspaces.Tags) > 0 {
		fmt.Fprintf(&buf, "      tags = [\"%s\"]\n", strings.Join(c.Workspaces.Tags, "\", \""))
	}
	buf.WriteString("    }\n")
	if c.Hostname != "" {
		fmt.Fprintf(&buf, "    hostname = %q\n", c.Hostname)
	}
	if c.Token != "" {
		fmt.Fprintf(&buf, "    token = %q\n", c.Token)
	}
	buf.WriteString("  }\n")
	buf.WriteString("}\n")
	return buf.String()
}
