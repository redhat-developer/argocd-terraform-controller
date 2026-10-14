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

package controller

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/runner"
)

// session is an initialised Terraform session in the runner.
type session struct {
	instance   string
	workingDir string
	tmpDir     string
}

// backendCompletelyDisabled reports whether no backend block is written.
func backendCompletelyDisabled(tf *infrav1.Terraform) bool {
	if tf.Spec.Cloud != nil {
		return true
	}
	return tf.Spec.BackendConfig != nil && tf.Spec.BackendConfig.Disable
}

// backendHCL renders the backend block for tf.
func backendHCL(tf *infrav1.Terraform) string {
	bc := tf.Spec.BackendConfig
	switch {
	case bc != nil && bc.CustomConfiguration != "":
		return fmt.Sprintf("\nterraform {\n  %s\n}\n", strings.TrimSpace(bc.CustomConfiguration))
	case bc == nil && os.Getenv("DISABLE_TF_K8S_BACKEND") == "1":
		return "\nterraform {\n  backend \"local\" { }\n}\n"
	}

	inCluster := true
	configPath := ""
	var labels map[string]string
	if bc != nil {
		inCluster = bc.InClusterConfig || bc.ConfigPath == ""
		configPath = bc.ConfigPath
		labels = bc.Labels
	}
	var b strings.Builder
	b.WriteString("\nterraform {\n  backend \"kubernetes\" {\n")
	fmt.Fprintf(&b, "    secret_suffix     = %q\n", tf.StateSecretSuffix())
	fmt.Fprintf(&b, "    in_cluster_config = %v\n", inCluster)
	if configPath != "" {
		fmt.Fprintf(&b, "    config_path       = %q\n", configPath)
	}
	fmt.Fprintf(&b, "    namespace         = %q\n", tf.Namespace)
	b.WriteString("    labels            = {\n")
	b.WriteString(labelsAsHCL(labels, 6))
	b.WriteString("    }\n  }\n}\n")
	return b.String()
}

func labelsAsHCL(labels map[string]string, indent int) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s%q = %q\n", strings.Repeat(" ", indent), k, labels[k])
	}
	return b.String()
}

// binaryCandidates returns the binaries to look for, in preference order.
func binaryCandidates(b infrav1.BinaryEnum) []string {
	switch b {
	case infrav1.BinaryTerraform:
		return []string{"terraform"}
	case infrav1.BinaryTofu:
		return []string{"tofu"}
	default:
		return []string{"tofu", "terraform"}
	}
}

// lockDetail extracts the state lock identifier from a runner error.
func lockDetail(err error) (string, bool) {
	st, ok := status.FromError(err)
	if !ok {
		return "", false
	}
	for _, d := range st.Details() {
		switch reply := d.(type) {
		case *runner.InitReply:
			return reply.StateLockIdentifier, true
		case *runner.PlanReply:
			return reply.StateLockIdentifier, true
		case *runner.ApplyReply:
			return reply.StateLockIdentifier, true
		case *runner.DestroyReply:
			return reply.StateLockIdentifier, true
		}
	}
	return "", false
}

// setupTerraform uploads the source and prepares the runner session up to
// and including init and workspace selection.
//
//nolint:gocyclo // the setup sequence is intentionally linear and explicit
func (r *TerraformReconciler) setupTerraform(ctx context.Context, p *patcher, rc runner.RunnerClient, tf *infrav1.Terraform, revision, loopID string) (*session, error) {
	log := ctrl.LoggerFrom(ctx)
	if r.RunnerRPCTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.RunnerRPCTimeout)
		defer cancel()
	}
	s := &session{instance: "0"}
	fail := func(reason string, err error) (*session, error) {
		infrav1.TerraformNotReady(tf, revision, reason, err.Error())
		return s, err
	}

	infrav1.TerraformProgressing(tf, "Initializing")
	if err := p.Patch(ctx, tf); err != nil {
		return s, err
	}

	artifact, err := r.Source.Fetch(ctx, tf)
	if err != nil {
		return fail(infrav1.SourceFetchFailedReason, fmt.Errorf("unable to fetch source: %w", err))
	}
	if artifact.Revision != revision {
		log.Info("source moved during reconciliation", "resolved", revision, "fetched", artifact.Revision)
		revision = artifact.Revision
	}

	extract, err := rc.UploadAndExtract(ctx, &runner.UploadAndExtractRequest{
		Namespace: tf.Namespace, Name: tf.Name, TarGz: artifact.Data, Path: tf.Spec.Path,
	})
	if err != nil {
		return fail(infrav1.SourceFetchFailedReason, fmt.Errorf("unable to upload source: %w", err))
	}
	s.workingDir, s.tmpDir = extract.WorkingDir, extract.TmpDir

	// Backend.
	var backend string
	switch {
	case tf.Spec.Cloud != nil:
		if !tf.Spec.Cloud.IsValid() {
			return fail(infrav1.BackendConfigurationFailedReason, fmt.Errorf("spec.cloud requires organization and a workspace name or tags"))
		}
		backend = tf.Spec.Cloud.ToHCL()
	case backendCompletelyDisabled(tf):
		log.Info("backend configuration is disabled")
	default:
		backend = backendHCL(tf)
	}
	if backend != "" {
		if _, err := rc.WriteBackendConfig(ctx, &runner.WriteBackendConfigRequest{DirPath: s.workingDir, BackendConfig: []byte(backend)}); err != nil {
			return fail(infrav1.BackendConfigurationFailedReason, fmt.Errorf("unable to write backend config: %w", err))
		}
	}
	if tf.UsesKubernetesBackend() {
		tf.Status.StateSecretName = tf.StateSecretName()
	} else {
		tf.Status.StateSecretName = ""
	}

	// CLI config.
	tfrcPath := ""
	if ref := tf.Spec.CliConfigSecretRef; ref != nil {
		ns := ref.Namespace
		if ns == "" {
			ns = tf.Namespace
		}
		if r.NoCrossNamespaceRefs && ns != tf.Namespace {
			return fail(infrav1.AccessDeniedReason, &accessDeniedError{msg: fmt.Sprintf("cannot access secret %s/%s, cross-namespace references have been disabled", ns, ref.Name)})
		}
		reply, err := rc.ProcessCliConfig(ctx, &runner.ProcessCliConfigRequest{DirPath: s.workingDir, Namespace: ns, Name: ref.Name})
		if err != nil {
			return fail(infrav1.TFExecNewFailedReason, fmt.Errorf("cannot process cli config: %w", err))
		}
		tfrcPath = reply.FilePath
	}

	// Binary.
	candidates := binaryCandidates(tf.Spec.Runner.GetBinary())
	lookPath, err := rc.LookPath(ctx, &runner.LookPathRequest{Files: candidates})
	if err != nil {
		return fail(infrav1.TFExecNewFailedReason, fmt.Errorf("cannot find any of the required binaries %v in the runner image: %w", candidates, err))
	}

	tfBytes, err := tf.ToBytes(r.Scheme)
	if err != nil {
		return s, err
	}
	newReply, err := rc.NewTerraform(ctx, &runner.NewTerraformRequest{
		WorkingDir: s.workingDir, ExecPath: lookPath.ExecPath, InstanceID: loopID, Terraform: tfBytes,
	})
	if err != nil {
		return fail(infrav1.TFExecNewFailedReason, fmt.Errorf("error creating terraform session: %w", err))
	}
	s.instance = newReply.Id
	if newReply.Version != "" {
		tf.Status.Binary = newReply.Version
	}

	// Environment.
	envs := map[string]string{}
	for _, env := range tf.Spec.Runner.PodTemplate.Spec.Env {
		if env.ValueFrom == nil {
			envs[env.Name] = env.Value
			continue
		}
		val, err := r.resolveEnvFrom(ctx, tf.Namespace, env.ValueFrom)
		if err != nil {
			return fail(infrav1.TFExecInitFailedReason, fmt.Errorf("environment variable %s: %w", env.Name, err))
		}
		envs[env.Name] = val
	}
	if os.Getenv("DISABLE_TF_LOGS") != "1" {
		envs["DISABLE_TF_LOGS"] = "1"
	}
	if tfrcPath != "" {
		envs["TF_CLI_CONFIG_FILE"] = tfrcPath
	}
	if _, err := rc.SetEnv(ctx, &runner.SetEnvRequest{TfInstance: s.instance, Envs: envs}); err != nil {
		return fail(infrav1.TFExecInitFailedReason, fmt.Errorf("error setting environment: %w", err))
	}

	// File mappings.
	if len(tf.Spec.FileMappings) > 0 {
		mappings, err := r.fileMappings(ctx, tf)
		if err != nil {
			return fail(infrav1.TFExecInitFailedReason, fmt.Errorf("error reading file mappings: %w", err))
		}
		if _, err := rc.CreateFileMappings(ctx, &runner.CreateFileMappingsRequest{WorkingDir: s.workingDir, FileMappings: mappings}); err != nil {
			return fail(infrav1.TFExecInitFailedReason, fmt.Errorf("error creating file mappings: %w", err))
		}
	}

	if _, err := rc.GenerateVarsForTF(ctx, &runner.GenerateVarsForTFRequest{WorkingDir: s.workingDir}); err != nil {
		return fail(infrav1.VarsGenerationFailedReason, err)
	}
	if _, err := rc.GenerateTemplate(ctx, &runner.GenerateTemplateRequest{WorkingDir: s.workingDir}); err != nil {
		return fail(infrav1.TemplateGenerationFailedReason, err)
	}

	// Init.
	initReq := &runner.InitRequest{TfInstance: s.instance, Upgrade: tf.Spec.GetUpgradeOnInit(), ForceCopy: !backendCompletelyDisabled(tf)}
	if _, err := rc.Init(ctx, initReq); err != nil {
		if id, ok := lockDetail(err); ok {
			infrav1.TerraformStateLocked(tf, id, fmt.Sprintf("Terraform Locked with Lock Identifier: %s", id))
		}
		return fail(infrav1.TFExecInitFailedReason, fmt.Errorf("error running init: %w", err))
	}
	if _, err := rc.SelectWorkspace(ctx, &runner.WorkspaceRequest{TfInstance: s.instance}); err != nil {
		return fail(infrav1.WorkspaceSelectFailedReason, err)
	}

	// Force unlock.
	if st := tf.Spec.TFState; st != nil && tf.Status.Lock.Pending != "" {
		lockID := ""
		switch {
		case st.ForceUnlock == infrav1.ForceUnlockEnumYes && st.LockIdentifier == tf.Status.Lock.Pending:
			lockID = tf.Status.Lock.Pending
		case st.ForceUnlock == infrav1.ForceUnlockEnumAuto:
			lockID = tf.Status.Lock.Pending
		}
		if lockID != "" {
			if _, err := rc.ForceUnlock(ctx, &runner.ForceUnlockRequest{TfInstance: s.instance, LockIdentifier: lockID}); err != nil {
				return s, fmt.Errorf("error force unlocking state: %w", err)
			}
			msg := fmt.Sprintf("Terraform Force Unlock with Lock Identifier: %s", lockID)
			r.Eventf(tf, corev1.EventTypeWarning, infrav1.TFExecForceUnlockReason, "%s", msg)
			infrav1.TerraformForceUnlock(tf, msg)
			if err := p.Patch(ctx, tf); err != nil {
				return s, err
			}
		}
	}
	return s, nil
}

func (r *TerraformReconciler) resolveEnvFrom(ctx context.Context, namespace string, src *corev1.EnvVarSource) (string, error) {
	switch {
	case src.SecretKeyRef != nil:
		s := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: src.SecretKeyRef.Name}, s); err != nil {
			return "", err
		}
		return string(s.Data[src.SecretKeyRef.Key]), nil
	case src.ConfigMapKeyRef != nil:
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: src.ConfigMapKeyRef.Name}, cm); err != nil {
			return "", err
		}
		return cm.Data[src.ConfigMapKeyRef.Key], nil
	}
	return "", fmt.Errorf("only secretKeyRef and configMapKeyRef are supported")
}

func (r *TerraformReconciler) fileMappings(ctx context.Context, tf *infrav1.Terraform) ([]*runner.FileMapping, error) {
	out := make([]*runner.FileMapping, 0, len(tf.Spec.FileMappings))
	for _, fm := range tf.Spec.FileMappings {
		s := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: tf.Namespace, Name: fm.SecretRef.Name}, s); err != nil {
			return nil, err
		}
		content, ok := s.Data[fm.SecretRef.Key]
		if !ok {
			return nil, fmt.Errorf("key %q not found in Secret %s", fm.SecretRef.Key, fm.SecretRef.Name)
		}
		out = append(out, &runner.FileMapping{Content: content, Location: fm.Location, Path: fm.Path})
	}
	return out, nil
}
