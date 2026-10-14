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

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/plan"
	"github.com/redhat-developer/argocd-terraform-controller/internal/tarball"
)

func TestNormalizeError(t *testing.T) {
	plain := errors.New("exit status 1")
	if got := normalizeError(plain); got != plain {
		t.Errorf("plain error must pass through, got %v", got)
	}

	locked := errors.New(`Error: Error acquiring the state lock

Error message: the state is already locked by another terraform client
Lock Info:
  ID:        f2ab685b-f84d-ac0b-a125-378a22877e8d
  Path:      tfstate-default-x
  Operation: OperationTypeApply
  Who:       runner@pod
  Version:   1.6.0
  Created:   2024-01-01 00:00:00 +0000 UTC
`)
	got := normalizeError(locked)
	var lockErr *StateLockError
	if !errors.As(got, &lockErr) {
		t.Fatalf("expected StateLockError, got %T", got)
	}
	if lockErr.ID != "f2ab685b-f84d-ac0b-a125-378a22877e8d" || lockErr.Who != "runner@pod" {
		t.Errorf("unexpected lock info: %+v", lockErr)
	}
	if !strings.Contains(lockErr.Error(), "ID:        f2ab685b") {
		t.Errorf("Error() = %q", lockErr.Error())
	}

	noInfo := normalizeError(errors.New("Error acquiring the state lock: timeout"))
	if !errors.As(noInfo, &lockErr) || lockErr.ID != "" {
		t.Errorf("expected lock error without ID, got %v", noInfo)
	}
}

func TestSanitizeLog(t *testing.T) {
	in := "Error: Invalid value\n\n  on generated.auto.tfvars.json line 1:\n   1: {\"password\":\"hunter2\",\"user\":\"bob\"}\n"
	out := sanitizeLog(in)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "bob") {
		t.Errorf("values leaked: %s", out)
	}
	if !strings.Contains(out, `"password":"***"`) {
		t.Errorf("expected masked json, got %s", out)
	}
}

func TestParseRenamePattern(t *testing.T) {
	for _, tt := range []struct {
		in, oldKey, newKey string
		wantErr            bool
	}{
		{"a", "a", "a", false},
		{"a:b", "a", "b", false},
		{":b", "", "", true},
		{"a:", "", "", true},
		{"a:b:c", "", "", true},
	} {
		o, n, err := parseRenamePattern(tt.in)
		if (err != nil) != tt.wantErr || o != tt.oldKey || n != tt.newKey {
			t.Errorf("parseRenamePattern(%q) = %q,%q,%v", tt.in, o, n, err)
		}
	}
}

func TestEnvMap(t *testing.T) {
	m := envMap([]string{"A=1", "B=x=y", "INVALID"})
	if m["A"] != "1" || m["B"] != "x=y" || len(m) != 2 {
		t.Errorf("envMap = %v", m)
	}
}

func TestBinaryName(t *testing.T) {
	if binaryName("/usr/local/bin/tofu") != "tofu" || binaryName("/bin/terraform.exe") != "terraform" {
		t.Error("binaryName failed")
	}
}

// newTestServer returns a Server backed by a fake client holding objs.
func newTestServer(t *testing.T, objs ...client.Object) (*Server, client.Client) {
	t.Helper()
	scheme, err := NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return NewServer(c, scheme, make(chan struct{})), c
}

func TestGenerateVarsForTF(t *testing.T) {
	tf := &infrav1.Terraform{
		ObjectMeta: metav1.ObjectMeta{Name: "stack", Namespace: "ns"},
		Spec: infrav1.TerraformSpec{
			Values: &apiextensionsv1.JSON{Raw: []byte(`{"region":"us-east-1"}`)},
			Vars: []infrav1.Variable{
				{Name: "count", Value: &apiextensionsv1.JSON{Raw: []byte(`3`)}},
				{Name: "overridden", Value: &apiextensionsv1.JSON{Raw: []byte(`"from-vars"`)}},
				{Name: "from_ref", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}, Key: "token"}}},
			},
			VarsFrom: []infrav1.VarsReference{
				{Kind: "Secret", Name: "creds", VarsKeys: []string{"overridden", "token:renamed"}},
				{Kind: "ConfigMap", Name: "missing", Optional: true},
				{Kind: "ConfigMap", Name: "cm"},
			},
		},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "ns"},
		Data: map[string][]byte{"overridden": []byte("from-secret"), "token": []byte("t0k")}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"},
		Data: map[string]string{"env": "dev"}}

	s, _ := newTestServer(t, secret, cm)
	s.terraform = tf
	dir := t.TempDir()
	if _, err := s.GenerateVarsForTF(context.Background(), &GenerateVarsForTFRequest{WorkingDir: dir}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, generatedVarsFile))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"values":     map[string]any{"region": "us-east-1"},
		"count":      float64(3),
		"overridden": "from-secret",
		"from_ref":   "t0k",
		"renamed":    "t0k",
		"env":        "dev",
	}
	for k, v := range want {
		if gj, _ := json.Marshal(got[k]); string(gj) != mustJSON(v) {
			t.Errorf("%s = %s, want %s", k, gj, mustJSON(v))
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected keys: %v", got)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCreateFileMappingsRejectsUnknownLocation(t *testing.T) {
	s, _ := newTestServer(t)
	_, err := s.CreateFileMappings(context.Background(), &CreateFileMappingsRequest{
		WorkingDir:   t.TempDir(),
		FileMappings: []*FileMapping{{Location: "etc", Path: "x", Content: []byte("y")}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestUploadAndExtractRequiresPath(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.tf"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := tarball.Archive(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer(t)
	reply, err := s.UploadAndExtract(context.Background(), &UploadAndExtractRequest{Namespace: "ns", Name: "x", TarGz: data, Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = s.CleanupDir(context.Background(), &CleanupDirRequest{TmpDir: reply.TmpDir}) }()
	if _, err := os.Stat(filepath.Join(reply.WorkingDir, "main.tf")); err != nil {
		t.Error(err)
	}
	if _, err := s.UploadAndExtract(context.Background(), &UploadAndExtractRequest{Namespace: "ns", Name: "x", TarGz: data, Path: "does-not-exist"}); err == nil {
		t.Error("expected error for missing path")
	}
}

// TestPlanApplyLifecycle drives the full plan -> save -> load -> apply ->
// output flow against a real binary with the built-in terraform_data
// resource and a local backend, so no providers are downloaded.
//
//nolint:gocyclo // one long scenario is clearer than many interdependent tests
func TestPlanApplyLifecycle(t *testing.T) {
	execPath := findBinary(t)
	t.Setenv("DISABLE_TF_LOGS", "1")

	workDir := t.TempDir()
	const config = `
variable "greeting" { type = string }
resource "terraform_data" "x" { input = var.greeting }
output "echo" { value = terraform_data.x.output }
output "secret" {
  value     = "s3cr3t"
  sensitive = true
}
`
	if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	tf := &infrav1.Terraform{
		TypeMeta:   metav1.TypeMeta{APIVersion: infrav1.GroupVersion.String(), Kind: infrav1.TerraformKind},
		ObjectMeta: metav1.ObjectMeta{Name: "stack", Namespace: "ns", UID: "uid-1"},
		Spec: infrav1.TerraformSpec{
			Vars:              []infrav1.Variable{{Name: "greeting", Value: &apiextensionsv1.JSON{Raw: []byte(`"hello"`)}}},
			StoreReadablePlan: infrav1.StoreReadablePlanHuman,
			BackendConfig:     &infrav1.BackendConfigSpec{CustomConfiguration: `backend "local" {}`},
		},
	}
	s, c := newTestServer(t)
	ctx := context.Background()

	tfBytes, err := tf.ToBytes(s.Scheme)
	if err != nil {
		t.Fatal(err)
	}
	newReply, err := s.NewTerraform(ctx, &NewTerraformRequest{WorkingDir: workDir, ExecPath: execPath, Terraform: tfBytes, InstanceID: "i-1"})
	if err != nil {
		t.Fatal(err)
	}
	if newReply.Version == "" {
		t.Error("expected a binary version")
	}
	t.Logf("binary: %s", newReply.Version)

	if _, err := s.SetEnv(ctx, &SetEnvRequest{TfInstance: "i-1", Envs: map[string]string{"MY_PROVIDER_TOKEN": "x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteBackendConfig(ctx, &WriteBackendConfigRequest{DirPath: workDir, BackendConfig: []byte("terraform {\n  backend \"local\" {}\n}\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GenerateVarsForTF(ctx, &GenerateVarsForTFRequest{WorkingDir: workDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Init(ctx, &InitRequest{TfInstance: "i-1", Upgrade: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectWorkspace(ctx, &WorkspaceRequest{TfInstance: "i-1"}); err != nil {
		t.Fatal(err)
	}

	planReply, err := s.Plan(ctx, &PlanRequest{TfInstance: "i-1", Out: TFPlanName, Refresh: true, SourceRefRootDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if !planReply.Drifted || !planReply.PlanCreated {
		t.Fatalf("expected a plan with changes: %+v", planReply)
	}

	const revision = "main@sha1:0123456789abcdef"
	saveReply, err := s.SaveTFPlan(ctx, &SaveTFPlanRequest{TfInstance: "i-1", Name: "stack", Namespace: "ns", Uuid: "uid-1", Revision: revision})
	if err != nil {
		t.Fatal(err)
	}
	if saveReply.ReadablePlanName != "tfplan-default-stack-readable" {
		t.Errorf("readable plan name = %q", saveReply.ReadablePlanName)
	}
	secrets := &corev1.SecretList{}
	if err := c.List(ctx, secrets, client.InNamespace("ns"), client.MatchingLabels(plan.Labels("stack", "default", ""))); err != nil || len(secrets.Items) != 1 {
		t.Fatalf("expected one plan secret, got %d (%v)", len(secrets.Items), err)
	}
	if secrets.Items[0].Annotations[plan.SavedAnnotation] != "plan-main-0123456789" {
		t.Errorf("plan id annotation = %q", secrets.Items[0].Annotations[plan.SavedAnnotation])
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "ns", Name: saveReply.ReadablePlanName}, cm); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cm.Data[plan.DataKey], "terraform_data.x") {
		t.Errorf("readable plan does not mention the resource:\n%s", cm.Data[plan.DataKey])
	}

	// Simulate a fresh runner: remove the plan file and reload it from the Secret.
	if err := os.Remove(filepath.Join(workDir, TFPlanName)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadTFPlan(ctx, &LoadTFPlanRequest{TfInstance: "i-1", Name: "stack", Namespace: "ns", PendingPlan: "plan-main-0123456789"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadTFPlan(ctx, &LoadTFPlanRequest{TfInstance: "i-1", Name: "stack", Namespace: "ns", PendingPlan: "plan-other"}); err == nil {
		t.Error("expected mismatching pending plan to be rejected")
	}

	if _, err := s.Apply(ctx, &ApplyRequest{TfInstance: "i-1", DirOrPlan: TFPlanName}); err != nil {
		t.Fatal(err)
	}

	out, err := s.Output(ctx, &OutputRequest{TfInstance: "i-1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Outputs["echo"].Value) != `"hello"` || !out.Outputs["secret"].Sensitive {
		t.Errorf("unexpected outputs: %v", out.Outputs)
	}

	inv, err := s.GetInventory(ctx, &GetInventoryRequest{TfInstance: "i-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Inventories) != 1 || inv.Inventories[0].Type != "terraform_data" {
		t.Errorf("inventory = %v", inv.Inventories)
	}

	// A second plan must report no changes.
	planReply, err = s.Plan(ctx, &PlanRequest{TfInstance: "i-1", Out: TFPlanName, Refresh: true, SourceRefRootDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if planReply.Drifted {
		t.Error("expected no drift after apply")
	}

	wr, err := s.WriteOutputs(ctx, &WriteOutputsRequest{Namespace: "ns", Name: "stack", SecretName: "stack-outputs", Uuid: "uid-1",
		Data: map[string][]byte{"echo": []byte("hello")}})
	if err != nil || !wr.Changed {
		t.Fatalf("WriteOutputs: %v %v", wr, err)
	}
	wr, _ = s.WriteOutputs(ctx, &WriteOutputsRequest{Namespace: "ns", Name: "stack", SecretName: "stack-outputs", Uuid: "uid-1",
		Data: map[string][]byte{"echo": []byte("hello")}})
	if wr.Changed {
		t.Error("identical outputs must not report a change")
	}

	fin, err := s.FinalizeSecrets(ctx, &FinalizeSecretsRequest{Namespace: "ns", Name: "stack", Workspace: "default", HasSpecifiedOutputSecret: true, OutputSecretName: "stack-outputs"})
	if err != nil || fin.NotFound {
		t.Fatalf("FinalizeSecrets: %v %v", fin, err)
	}
	if err := c.List(ctx, secrets, client.InNamespace("ns")); err != nil || len(secrets.Items) != 0 {
		t.Errorf("expected all secrets deleted, got %d", len(secrets.Items))
	}

	if _, err := s.Destroy(ctx, &DestroyRequest{TfInstance: "i-1"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Plan(ctx, &PlanRequest{TfInstance: "other", Out: TFPlanName}); err == nil {
		t.Error("expected session mismatch error")
	}
}

func findBinary(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"tofu", "terraform"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("neither tofu nor terraform found in PATH")
	return ""
}
