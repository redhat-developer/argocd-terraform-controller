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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newTF(name, workspace string) *Terraform {
	tf := &Terraform{}
	tf.Name = name
	tf.Namespace = "default"
	tf.Spec.Workspace = workspace
	return tf
}

func TestNaming(t *testing.T) {
	tf := newTF("my-stack", "")
	if got := tf.WorkspaceName(); got != "default" {
		t.Errorf("WorkspaceName() = %q, want default", got)
	}
	if got := tf.StateSecretName(); got != "tfstate-default-my-stack" {
		t.Errorf("StateSecretName() = %q", got)
	}
	if got := tf.StateLockLeaseName(); got != "lock-tfstate-default-my-stack" {
		t.Errorf("StateLockLeaseName() = %q", got)
	}
	if got := tf.PlanSecretName(); got != "tfplan-default-my-stack" {
		t.Errorf("PlanSecretName() = %q", got)
	}
	if got := tf.RunnerPodName(); got != "my-stack-tf-runner" {
		t.Errorf("RunnerPodName() = %q", got)
	}

	tf = newTF("my-stack", "dev")
	tf.Spec.BackendConfig = &BackendConfigSpec{SecretSuffix: "custom"}
	if got := tf.StateSecretName(); got != "tfstate-dev-custom" {
		t.Errorf("StateSecretName() with suffix = %q", got)
	}
	if got := tf.PlanSecretName(); got != "tfplan-dev-my-stack" {
		t.Errorf("PlanSecretName() with workspace = %q", got)
	}
}

func TestUsesKubernetesBackend(t *testing.T) {
	tests := []struct {
		name string
		spec TerraformSpec
		want bool
	}{
		{"default", TerraformSpec{}, true},
		{"explicit k8s", TerraformSpec{BackendConfig: &BackendConfigSpec{SecretSuffix: "x"}}, true},
		{"disabled", TerraformSpec{BackendConfig: &BackendConfigSpec{Disable: true}}, false},
		{"custom", TerraformSpec{BackendConfig: &BackendConfigSpec{CustomConfiguration: `backend "s3" {}`}}, false},
		{"cloud", TerraformSpec{Cloud: &CloudSpec{Organization: "o"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tf := &Terraform{Spec: tt.spec}
			if got := tf.UsesKubernetesBackend(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetRetryInterval(t *testing.T) {
	tf := &Terraform{}
	if got := tf.GetRetryInterval(); got != 15*time.Second {
		t.Errorf("default = %v", got)
	}

	tf.Spec.RetryInterval = &metav1.Duration{Duration: time.Minute}
	if got := tf.GetRetryInterval(); got != time.Minute {
		t.Errorf("static = %v", got)
	}

	tf.Spec.RetryStrategy = ExponentialBackoff
	tf.Status.ReconciliationFailures = 3
	if got := tf.GetRetryInterval(); got != 8*time.Minute {
		t.Errorf("backoff = %v, want 8m", got)
	}

	tf.Spec.MaxRetryInterval = &metav1.Duration{Duration: 5 * time.Minute}
	if got := tf.GetRetryInterval(); got != 5*time.Minute {
		t.Errorf("capped = %v, want 5m", got)
	}
}

func TestShouldRetry(t *testing.T) {
	tf := &Terraform{}
	if !tf.ShouldRetry() {
		t.Error("nil remediation should retry")
	}
	tf.Spec.Remediation = &Remediation{Retries: 2}
	tf.Status.ReconciliationFailures = 2
	if tf.ShouldRetry() {
		t.Error("should stop at limit")
	}
	tf.Spec.Remediation.Retries = -1
	if !tf.ShouldRetry() {
		t.Error("negative is unlimited")
	}
}

func TestCloudSpecToHCL(t *testing.T) {
	c := &CloudSpec{
		Organization: "acme",
		Workspaces:   &CloudWorkspacesSpec{Tags: []string{"a", "b"}},
		Hostname:     "app.terraform.io",
	}
	if !c.IsValid() {
		t.Fatal("expected valid")
	}
	hcl := c.ToHCL()
	for _, want := range []string{`organization = "acme"`, `tags = ["a", "b"]`, `hostname = "app.terraform.io"`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("missing %q in:\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, "token") {
		t.Errorf("empty token must be omitted:\n%s", hcl)
	}

	if (&CloudSpec{Organization: "acme", Workspaces: &CloudWorkspacesSpec{}}).IsValid() {
		t.Error("workspace without name or tags should be invalid")
	}
}

func TestRunnerSpecDefaults(t *testing.T) {
	r := &RunnerSpec{}
	if r.GetBinary() != BinaryAuto || r.GetServiceAccountName() != DefaultRunnerSA || !r.GetAlwaysCleanup() {
		t.Errorf("unexpected defaults: %+v", r)
	}
	f := false
	r.AlwaysCleanup = &f
	if r.GetAlwaysCleanup() {
		t.Error("AlwaysCleanup=false not honoured")
	}
}

func TestPlanLifecycle(t *testing.T) {
	tf := newTF("stack", "")
	tf.Generation = 3
	rev := "main@sha1:0123456789abcdef"

	TerraformPlannedWithChanges(tf, rev, false, "Plan generated")
	if tf.Status.Plan.Pending != "plan-main-0123456789" {
		t.Fatalf("pending = %q", tf.Status.Plan.Pending)
	}
	if tf.Status.LastPlannedGeneration != 3 || tf.Status.LastPlannedRevision != rev {
		t.Errorf("plan bookkeeping: %+v", tf.Status)
	}
	ready := tf.GetCondition(ReadyCondition)
	if ready == nil || ready.Status != metav1.ConditionUnknown || !strings.Contains(ready.Message, "approvePlan") {
		t.Errorf("manual mode must ask for approval: %+v", ready)
	}

	TerraformApplied(tf, rev, "Applied", false, nil)
	if tf.Status.Plan.Pending != "" || tf.Status.Plan.LastApplied != "plan-main-0123456789" {
		t.Errorf("apply bookkeeping: %+v", tf.Status.Plan)
	}
	if tf.Status.LastAppliedRevision != rev {
		t.Errorf("lastAppliedRevision = %q", tf.Status.LastAppliedRevision)
	}

	TerraformOutputsWritten(tf, rev, "Outputs written")
	if !tf.IsReady() {
		t.Error("expected Ready=True after outputs written")
	}

	TerraformStateLocked(tf, "lock-1", "locked")
	if tf.Status.Lock.Pending != "lock-1" || tf.IsReady() {
		t.Errorf("lock bookkeeping: %+v ready=%v", tf.Status.Lock, tf.IsReady())
	}
	TerraformForceUnlock(tf, "unlocking")
	if tf.Status.Lock.Pending != "" || tf.Status.Lock.LastApplied != "lock-1" {
		t.Errorf("unlock bookkeeping: %+v", tf.Status.Lock)
	}
}

func TestTrimString(t *testing.T) {
	if got := trimString("héllo", 3); got != "hél..." {
		t.Errorf("got %q", got)
	}
	if got := trimString("abc", 10); got != "abc" {
		t.Errorf("got %q", got)
	}
}
