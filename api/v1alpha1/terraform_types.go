/*
Copyright 2022.

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
	"github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// TerraformSpec defines the desired state of Terraform
type TerraformSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Source               TerraformSource      `json:"source"`
	Project              string               `json:"project"`
	SyncPolicy           *v1alpha1.SyncPolicy `json:"syncPolicy,omitempty"`
	Info                 []v1alpha1.Info      `json:"info,omitempty"`
	RevisionHistoryLimit *int64               `json:"revisionHistoryLimit,omitempty"`
}

type TerraformSource struct {
	RepoURL         string           `json:"repoURL"`
	Path            string           `json:"path,omitempty"`
	TargetRevision  string           `json:"targetRevision,omitempty"`
	Destroy         bool             `json:"destroy,omitempty"`
	RefreshInterval *metav1.Duration `json:"refreshInterval,omitempty"`
}

// TerraformStatus defines the observed state of Terraform
type TerraformStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	State        TerraformState             `json:"state,omitempty"`
	Stage        TerraformStage             `json:"status,omitempty"`
	SyncStatus   v1alpha1.SyncStatus        `json:"sync,omitempty"`
	History      v1alpha1.RevisionHistories `json:"history,omitempty"`
	ReconciledAt *metav1.Time               `json:"reconciledAt,omitempty"`
}

// path of file containing terraform state
type TerraformState string

/* This could be:
Initializing
Planning
Applying
Destroying
Failed
*/
type TerraformStage string

// type returned from terraform-generate plugin
type TerraformWrapper struct {
	metav1.TypeMeta `json:",inline"`
	List            []TerraformFile `json:"list,omitempty"`
}

type TerraformFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Terraform is the Schema for the terraforms API
type Terraform struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TerraformSpec   `json:"spec,omitempty"`
	Status TerraformStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TerraformList contains a list of Terraform
type TerraformList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Terraform `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Terraform{}, &TerraformList{})
}
