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

// Package runner implements the gRPC server that executes terraform / tofu
// inside the runner Pod on behalf of the controller.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/go-logr/logr"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/plan"
	"github.com/redhat-developer/argocd-terraform-controller/internal/tarball"
)

const (
	// TFPlanName is the plan file name in the working directory.
	TFPlanName = "tfplan"
	// replyNotOK is the message of replies attached to error details.
	replyNotOK = "not ok"
	// HomePath is the runner's home directory, target of "home" file mappings.
	HomePath = "/home/runner"

	fileMappingLocationHome      = "home"
	fileMappingLocationWorkspace = "workspace"
	fileMappingDirPerm           = 0o700
	fileMappingFilePerm          = 0o600

	loggerName = "runner"
)

// SessionNotInitializedError indicates no NewTerraform call happened yet.
type SessionNotInitializedError struct{ RequestedInstanceID string }

func (e *SessionNotInitializedError) Error() string {
	return fmt.Sprintf("terraform session error: instance id is empty, expected %q", e.RequestedInstanceID)
}

// SessionMismatchError indicates a request for a different session.
type SessionMismatchError struct{ RequestedInstanceID, CurrentInstanceID string }

func (e *SessionMismatchError) Error() string {
	return fmt.Sprintf("terraform session mismatch: requested instance id %q does not match the current instance %q", e.RequestedInstanceID, e.CurrentInstanceID)
}

type printfer struct{ logger logr.Logger }

func (l printfer) Printf(format string, v ...any) { l.logger.Info(fmt.Sprintf(format, v...)) }

// Server implements RunnerServer. It holds at most one Terraform session.
type Server struct {
	UnimplementedRunnerServer
	client.Client
	Scheme *runtime.Scheme
	// Done is closed when the process receives SIGTERM; in-flight terraform
	// commands are cancelled.
	Done <-chan struct{}

	mu         sync.Mutex
	tf         *execWrapper
	terraform  *infrav1.Terraform
	instanceID string
}

// NewServer returns a Server using c to access the cluster.
func NewServer(c client.Client, scheme *runtime.Scheme, done <-chan struct{}) *Server {
	return &Server{Client: c, Scheme: scheme, Done: done}
}

func (r *Server) log(ctx context.Context) logr.Logger {
	return ctrl.LoggerFrom(ctx, "instance-id", r.instanceID).WithName(loggerName)
}

// cancelOnDone returns a ctx that is cancelled when the runner is shutting down.
func (r *Server) cancelOnDone(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-r.Done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (r *Server) validateInstanceID(requested string) error {
	if r.instanceID == "" {
		return &SessionNotInitializedError{RequestedInstanceID: requested}
	}
	if requested != r.instanceID {
		return &SessionMismatchError{RequestedInstanceID: requested, CurrentInstanceID: r.instanceID}
	}
	return nil
}

// lockErrStatus wraps err into a gRPC status, attaching the lock identifier
// as details when err is a StateLockError.
func lockErrStatus(err error, detail func(id string) protoadapt.MessageV1) error {
	st := status.New(codes.Internal, err.Error())
	if lockErr, ok := errors.AsType[*StateLockError](err); ok {
		if withDetails, derr := st.WithDetails(detail(lockErr.ID)); derr == nil {
			st = withDetails
		}
	}
	return st.Err()
}

// LookPath returns the first binary of req.Files present in PATH.
func (r *Server) LookPath(ctx context.Context, req *LookPathRequest) (*LookPathReply, error) {
	log := r.log(ctx)
	log.Info("looking for binaries", "files", req.Files)
	for _, file := range req.Files {
		if execPath, err := exec.LookPath(file); err == nil {
			log.Info("found binary", "file", file, "execPath", execPath)
			return &LookPathReply{ExecPath: execPath}, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "none of %v were found in the runner PATH", req.Files)
}

// UploadAndExtract extracts the source archive under a per-object temp dir.
func (r *Server) UploadAndExtract(ctx context.Context, req *UploadAndExtractRequest) (*UploadAndExtractReply, error) {
	log := r.log(ctx)
	tmpDir, err := securejoin.SecureJoin(os.TempDir(), fmt.Sprintf("%s-%s", req.Namespace, req.Name))
	if err != nil {
		return nil, err
	}
	// Start from a clean directory so files removed from the source disappear.
	if err := os.RemoveAll(tmpDir); err != nil {
		return nil, err
	}
	if err := tarball.Extract(bytes.NewReader(req.TarGz), tmpDir, 0); err != nil {
		log.Error(err, "unable to extract source archive")
		return nil, fmt.Errorf("failed to extract source archive: %w", err)
	}

	dirPath, err := securejoin.SecureJoin(tmpDir, req.Path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dirPath); err != nil {
		return nil, fmt.Errorf("terraform path %q not found in source: %w", req.Path, err)
	}
	return &UploadAndExtractReply{WorkingDir: dirPath, TmpDir: tmpDir}, nil
}

// CleanupDir removes a temp dir created by UploadAndExtract.
func (r *Server) CleanupDir(ctx context.Context, req *CleanupDirRequest) (*CleanupDirReply, error) {
	r.log(ctx).Info("cleaning up", "tmpDir", req.TmpDir)
	if !strings.HasPrefix(filepath.Clean(req.TmpDir), os.TempDir()) {
		return nil, status.Errorf(codes.InvalidArgument, "refusing to remove %q outside of %q", req.TmpDir, os.TempDir())
	}
	if err := os.RemoveAll(req.TmpDir); err != nil {
		return nil, err
	}
	return &CleanupDirReply{Message: "ok"}, nil
}

// WriteBackendConfig writes backend_override.tf into the working dir.
func (r *Server) WriteBackendConfig(ctx context.Context, req *WriteBackendConfigRequest) (*WriteBackendConfigReply, error) {
	const name = "backend_override.tf"
	filePath, err := securejoin.SecureJoin(req.DirPath, name)
	if err != nil {
		return nil, err
	}
	r.log(ctx).Info("writing backend config", "path", filePath)
	if err := os.WriteFile(filePath, req.BackendConfig, 0o644); err != nil {
		return nil, err
	}
	return &WriteBackendConfigReply{Message: "ok"}, nil
}

// ProcessCliConfig writes the *.tfrc file from the referenced Secret.
func (r *Server) ProcessCliConfig(ctx context.Context, req *ProcessCliConfigRequest) (*ProcessCliConfigReply, error) {
	log := r.log(ctx)
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, &secret); err != nil {
		return nil, err
	}
	if len(secret.Data) != 1 {
		return nil, fmt.Errorf("expected the CLI config secret %s/%s to contain exactly one key", req.Namespace, req.Name)
	}

	var tfrcPath string
	for filename, content := range secret.Data {
		if !strings.HasSuffix(filename, ".tfrc") {
			return nil, fmt.Errorf("expected the CLI config secret key to end with .tfrc, got %q", filename)
		}
		var err error
		if tfrcPath, err = securejoin.SecureJoin(req.DirPath, filename); err != nil {
			return nil, err
		}
		if err := os.WriteFile(tfrcPath, content, 0o600); err != nil {
			return nil, err
		}
		log.Info("wrote CLI config", "path", tfrcPath)
	}
	return &ProcessCliConfigReply{FilePath: tfrcPath}, nil
}

// initLogger routes terraform output to the process stdout/stderr unless
// DISABLE_TF_LOGS=1. ENABLE_SENSITIVE_TF_LOGS=1 also logs the executed
// commands (which may contain variable values).
func (r *Server) initLogger(log logr.Logger) {
	if os.Getenv("DISABLE_TF_LOGS") == "1" {
		return
	}
	r.tf.SetStdout(os.Stdout)
	r.tf.SetStderr(os.Stderr)
	if os.Getenv("ENABLE_SENSITIVE_TF_LOGS") == "1" {
		r.tf.SetLogger(printfer{logger: log})
	}
}

// NewTerraform creates the (single) Terraform session.
func (r *Server) NewTerraform(ctx context.Context, req *NewTerraformRequest) (*NewTerraformReply, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.instanceID = req.GetInstanceID()
	log := r.log(ctx)
	log.Info("creating terraform session", "workingDir", req.WorkingDir, "execPath", req.ExecPath)

	tf, err := tfexec.NewTerraform(req.WorkingDir, req.ExecPath)
	if err != nil {
		return nil, fmt.Errorf("unable to create terraform session: %w", err)
	}
	r.tf = newExecWrapper(tf)

	var terraform infrav1.Terraform
	if err := terraform.FromBytes(req.Terraform, r.Scheme); err != nil {
		return nil, fmt.Errorf("unable to decode the Terraform object: %w", err)
	}
	r.terraform = &terraform
	r.initLogger(log)

	version := ""
	r.tf.SetStdout(nil)
	r.tf.SetStderr(nil)
	if v, _, err := r.tf.Version(ctx, false); err == nil && v != nil {
		version = fmt.Sprintf("%s %s", binaryName(req.ExecPath), v.String())
	} else if err != nil {
		log.Info("unable to determine binary version", "error", err.Error())
	}
	r.initLogger(log)

	return &NewTerraformReply{Id: r.instanceID, Version: version}, nil
}

func binaryName(execPath string) string {
	return strings.TrimSuffix(filepath.Base(execPath), ".exe")
}

// SetEnv merges req.Envs over the process environment for the session.
func (r *Server) SetEnv(ctx context.Context, req *SetEnvRequest) (*SetEnvReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	envs := envMap(os.Environ())
	maps.Copy(envs, req.Envs)
	if err := r.tf.SetEnv(envs); err != nil {
		return nil, fmt.Errorf("unable to set environment: %w", err)
	}
	return &SetEnvReply{Message: "ok"}, nil
}

func envMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

// CreateFileMappings writes files into the home dir or workspace.
func (r *Server) CreateFileMappings(ctx context.Context, req *CreateFileMappingsRequest) (*CreateFileMappingsReply, error) {
	log := r.log(ctx)
	for _, fm := range req.FileMappings {
		var base string
		switch fm.Location {
		case fileMappingLocationHome:
			base = HomePath
		case fileMappingLocationWorkspace:
			base = req.WorkingDir
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unknown file mapping location %q", fm.Location)
		}
		fullPath, err := securejoin.SecureJoin(base, fm.Path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(fullPath), fileMappingDirPerm); err != nil {
			return nil, err
		}
		if err := os.WriteFile(fullPath, fm.Content, fileMappingFilePerm); err != nil {
			return nil, err
		}
		log.Info("wrote file mapping", "path", fullPath)
	}
	return &CreateFileMappingsReply{Message: "ok"}, nil
}

// SelectWorkspace creates (if needed) and selects the configured workspace.
func (r *Server) SelectWorkspace(ctx context.Context, req *WorkspaceRequest) (*WorkspaceReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	ws := r.terraform.WorkspaceName()
	if ws == infrav1.DefaultWorkspaceName {
		return &WorkspaceReply{Message: "ok"}, nil
	}
	log := r.log(ctx)
	if err := r.tf.WorkspaceNew(ctx, ws); err != nil {
		log.Info("workspace new", "workspace", ws, "result", err.Error())
	}
	if err := r.tf.WorkspaceSelect(ctx, ws); err != nil {
		return nil, fmt.Errorf("failed to select workspace %q: %w", ws, err)
	}
	return &WorkspaceReply{Message: "ok"}, nil
}

// Destroy runs terraform destroy.
func (r *Server) Destroy(ctx context.Context, req *DestroyRequest) (*DestroyReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	ctx, cancel := r.cancelOnDone(ctx)
	defer cancel()

	opts := make([]tfexec.DestroyOption, 0, len(req.Targets))
	for _, t := range req.Targets {
		opts = append(opts, tfexec.Target(t))
	}
	if err := r.tf.Destroy(ctx, opts...); err != nil {
		r.log(ctx).Error(err, "destroy failed")
		return nil, lockErrStatus(err, func(id string) protoadapt.MessageV1 {
			return &DestroyReply{Message: replyNotOK, StateLockIdentifier: id}
		})
	}
	return &DestroyReply{Message: "ok"}, nil
}

// Apply applies the saved plan (or the working dir when DirOrPlan is empty).
func (r *Server) Apply(ctx context.Context, req *ApplyRequest) (*ApplyReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	ctx, cancel := r.cancelOnDone(ctx)
	defer cancel()

	var opts []tfexec.ApplyOption
	if req.DirOrPlan != "" {
		opts = append(opts, tfexec.DirOrPlan(req.DirOrPlan))
	}
	if req.RefreshBeforeApply {
		opts = append(opts, tfexec.Refresh(true))
	}
	for _, t := range req.Targets {
		opts = append(opts, tfexec.Target(t))
	}
	if req.Parallelism > 0 {
		opts = append(opts, tfexec.Parallelism(int(req.Parallelism)))
	}

	if os.Getenv("LOG_HUMAN_READABLE_PLAN") == "1" {
		planName := req.DirOrPlan
		if planName == "" {
			planName = TFPlanName
		}
		if raw, err := r.showPlanFileRaw(ctx, planName); err == nil {
			fmt.Println(raw)
		}
	}

	if err := r.tf.Apply(ctx, opts...); err != nil {
		r.log(ctx).Error(err, "apply failed")
		return nil, lockErrStatus(err, func(id string) protoadapt.MessageV1 {
			return &ApplyReply{Message: replyNotOK, StateLockIdentifier: id}
		})
	}
	return &ApplyReply{Message: "ok"}, nil
}

func inventoryFromModule(m *tfjson.StateModule) []*Inventory {
	out := make([]*Inventory, 0, len(m.Resources))
	for _, res := range m.Resources {
		var id string
		if v, ok := res.AttributeValues["arn"].(string); ok {
			id = v
		} else if v, ok := res.AttributeValues["id"].(string); ok {
			id = v
		}
		out = append(out, &Inventory{Name: res.Name, Type: res.Type, Identifier: id})
	}
	for _, child := range m.ChildModules {
		out = append(out, inventoryFromModule(child)...)
	}
	return out
}

// GetInventory lists the resources in the state.
func (r *Server) GetInventory(ctx context.Context, req *GetInventoryRequest) (*GetInventoryReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	state, err := r.quietShow(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to read state: %w", err)
	}
	if state.Values == nil || state.Values.RootModule == nil {
		return &GetInventoryReply{Inventories: []*Inventory{}}, nil
	}
	return &GetInventoryReply{Inventories: inventoryFromModule(state.Values.RootModule)}, nil
}

func (r *Server) quietShow(ctx context.Context) (*tfjson.State, error) {
	log := r.log(ctx)
	r.tf.SetStdout(nil)
	r.tf.SetStderr(nil)
	defer r.initLogger(log)
	return r.tf.Show(ctx)
}

// FinalizeSecrets deletes plan objects and, when requested, the outputs Secret.
func (r *Server) FinalizeSecrets(ctx context.Context, req *FinalizeSecretsRequest) (*FinalizeSecretsReply, error) {
	log := r.log(ctx)
	deleted := 0

	for _, suffix := range []string{"", ".json", "-readable"} {
		secrets := &corev1.SecretList{}
		if err := r.List(ctx, secrets, client.InNamespace(req.Namespace), client.MatchingLabels(plan.Labels(req.Name, req.Workspace, suffix))); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		for i := range secrets.Items {
			if err := r.Delete(ctx, &secrets.Items[i]); client.IgnoreNotFound(err) != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			deleted++
		}
		cms := &corev1.ConfigMapList{}
		if err := r.List(ctx, cms, client.InNamespace(req.Namespace), client.MatchingLabels(plan.Labels(req.Name, req.Workspace, suffix))); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		for i := range cms.Items {
			if err := r.Delete(ctx, &cms.Items[i]); client.IgnoreNotFound(err) != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			deleted++
		}
	}

	if req.HasSpecifiedOutputSecret {
		var outputs corev1.Secret
		err := r.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.OutputSecretName}, &outputs)
		switch {
		case err == nil:
			if err := r.Delete(ctx, &outputs); client.IgnoreNotFound(err) != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
			deleted++
		case !apierrors.IsNotFound(err):
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	log.Info("finalized plan and output objects", "deleted", deleted)
	return &FinalizeSecretsReply{Message: "ok", NotFound: deleted == 0}, nil
}

// DeleteState removes the Kubernetes backend state Secret and lock Lease.
func (r *Server) DeleteState(ctx context.Context, req *DeleteStateRequest) (*DeleteStateReply, error) {
	log := r.log(ctx)
	notFound := true

	secret := &corev1.Secret{}
	secret.Namespace, secret.Name = req.Namespace, req.SecretName
	if err := r.Delete(ctx, secret); err == nil {
		notFound = false
		log.Info("deleted state secret", "name", req.SecretName)
	} else if !apierrors.IsNotFound(err) {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if req.LeaseName != "" {
		lease := &coordinationv1.Lease{}
		lease.Namespace, lease.Name = req.Namespace, req.LeaseName
		if err := r.Delete(ctx, lease); err == nil {
			log.Info("deleted state lock lease", "name", req.LeaseName)
		} else if !apierrors.IsNotFound(err) {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &DeleteStateReply{Message: "ok", NotFound: notFound}, nil
}

// ForceUnlock force-unlocks the state.
func (r *Server) ForceUnlock(ctx context.Context, req *ForceUnlockRequest) (*ForceUnlockReply, error) {
	if err := r.validateInstanceID(req.TfInstance); err != nil {
		return nil, err
	}
	if err := r.tf.ForceUnlock(ctx, req.LockIdentifier); err != nil {
		return &ForceUnlockReply{Success: false, Message: fmt.Sprintf("Error unlocking the state: %s", err)}, err
	}
	return &ForceUnlockReply{Success: true, Message: fmt.Sprintf("Successfully unlocked state with lock identifier: %s", req.LockIdentifier)}, nil
}
