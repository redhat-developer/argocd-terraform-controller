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
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrav1 "github.com/redhat-developer/argocd-terraform-controller/api/v1alpha1"
	"github.com/redhat-developer/argocd-terraform-controller/internal/mtls"
	"github.com/redhat-developer/argocd-terraform-controller/internal/runner"
	"github.com/redhat-developer/argocd-terraform-controller/internal/source"
)

// The suite runs the real controller against envtest with an in-process
// runner (INSECURE_LOCAL_RUNNER=1) that executes the local terraform or
// tofu binary. The Kubernetes backend cannot be used outside a cluster, so
// tests point the local backend at a stable path to keep state between
// reconciliations.

const (
	testNamespace = "default"
	runnerPort    = 30123
	timeout       = 90 * time.Second
	interval      = 500 * time.Millisecond
)

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
	rotator   *mtls.CertRotator
)

func TestControllers(t *testing.T) {
	if findBinary() == "" {
		t.Skip("neither tofu nor terraform found in PATH")
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

func findBinary() string {
	for _, name := range []string{"tofu", "terraform"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	ctx, cancel = context.WithCancel(context.TODO())

	Expect(os.Setenv("INSECURE_LOCAL_RUNNER", "1")).To(Succeed())
	Expect(os.Setenv("DISABLE_TF_LOGS", "1")).To(Succeed())

	Expect(infrav1.AddToScheme(scheme.Scheme)).To(Succeed())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}
	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	rotator = &mtls.CertRotator{
		Client:                 mgr.GetClient(),
		Ready:                  make(chan struct{}),
		CAName:                 "argocd-terraform-controller",
		CAOrganization:         "argocd.io",
		CAValidityDuration:     time.Hour,
		RotationCheckFrequency: time.Hour,
		LookaheadInterval:      time.Minute,
		ClusterDomain:          "cluster.local",
	}
	Expect(mgr.Add(rotator)).To(Succeed())

	reconciler := &TerraformReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		Recorder:                 mgr.GetEventRecorderFor("test"), //nolint:staticcheck // the new events API has a different recorder interface
		CertRotator:              rotator,
		Source:                   source.NewManager(mgr.GetClient(), source.Options{}),
		RunnerGRPCPort:           runnerPort,
		RunnerGRPCMaxMessageSize: 4,
		RunnerCreationTimeout:    time.Minute,
		RunnerRPCTimeout:         5 * time.Minute,
		ClusterDomain:            "cluster.local",
		NoCrossNamespaceRefs:     true,
	}
	Expect(reconciler.SetupWithManager(mgr, 2)).To(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()

	By("starting the local runner with the namespace TLS secret")
	Eventually(rotator.Ready, timeout).Should(BeClosed())
	tlsSecret, err := rotator.EnsureNamespaceTLS(ctx, testNamespace)
	Expect(err).NotTo(HaveOccurred())
	creds, err := mtls.ServerCredentials(tlsSecret)
	Expect(err).NotTo(HaveOccurred())
	runnerServer := runner.NewServer(k8sClient, scheme.Scheme, ctx.Done())
	go func() {
		defer GinkgoRecover()
		_ = runner.ServeWith(ctx, runnerServer, fmt.Sprintf("127.0.0.1:%d", runnerPort), creds, 4)
	}()
	Eventually(func() error {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", runnerPort), time.Second)
		if err == nil {
			_ = c.Close()
		}
		return err
	}, timeout, interval).Should(Succeed())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})

func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// testRepo is a local Git repository used as a Terraform source.
type testRepo struct {
	dir  string
	repo *git.Repository
}

func newTestRepo(files map[string]string) *testRepo {
	dir, err := os.MkdirTemp("", "tf-src-*")
	Expect(err).NotTo(HaveOccurred())
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{InitOptions: git.InitOptions{DefaultBranch: plumbing.Main}})
	Expect(err).NotTo(HaveOccurred())
	r := &testRepo{dir: dir, repo: repo}
	r.commit(files, "initial")
	DeferCleanup(os.RemoveAll, dir)
	return r
}

func (r *testRepo) commit(files map[string]string, msg string) string {
	wt, err := r.repo.Worktree()
	Expect(err).NotTo(HaveOccurred())
	for name, content := range files {
		path := filepath.Join(r.dir, name)
		Expect(os.MkdirAll(filepath.Dir(path), 0o750)).To(Succeed())
		Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
	}
	_, err = wt.Add(".")
	Expect(err).NotTo(HaveOccurred())
	h, err := wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()}})
	Expect(err).NotTo(HaveOccurred())
	return h.String()
}

// localBackend returns a backendConfig that stores state outside the runner
// working directory so it survives between reconciliations.
func localBackend() *infrav1.BackendConfigSpec {
	dir, err := os.MkdirTemp("", "tf-state-*")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(os.RemoveAll, dir)
	return &infrav1.BackendConfigSpec{
		CustomConfiguration: fmt.Sprintf("backend \"local\" {\n    path = %q\n  }", filepath.Join(dir, "terraform.tfstate")),
	}
}

func newTerraform(name string, repo *testRepo, mutate func(*infrav1.Terraform)) *infrav1.Terraform {
	tf := &infrav1.Terraform{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: infrav1.TerraformSpec{
			Source:            infrav1.SourceSpec{Git: &infrav1.GitSource{URL: repo.dir, Ref: &infrav1.GitRef{Branch: "main"}}},
			Interval:          metav1.Duration{Duration: 10 * time.Second},
			RetryInterval:     &metav1.Duration{Duration: 2 * time.Second},
			BackendConfig:     localBackend(),
			StoreReadablePlan: infrav1.StoreReadablePlanHuman,
		},
	}
	if mutate != nil {
		mutate(tf)
	}
	return tf
}

func getTF(key client.ObjectKey) *infrav1.Terraform {
	tf := &infrav1.Terraform{}
	ExpectWithOffset(1, k8sClient.Get(ctx, key, tf)).To(Succeed())
	return tf
}

func readyCondition(key client.ObjectKey) func() *metav1.Condition {
	return func() *metav1.Condition {
		tf := &infrav1.Terraform{}
		if err := k8sClient.Get(ctx, key, tf); err != nil {
			return nil
		}
		return tf.GetCondition(infrav1.ReadyCondition)
	}
}

func readyStatus(key client.ObjectKey) func() metav1.ConditionStatus {
	return func() metav1.ConditionStatus {
		if c := readyCondition(key)(); c != nil {
			return c.Status
		}
		return ""
	}
}

func deleteAndWait(tf *infrav1.Terraform) {
	Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, tf))).To(Succeed())
	Eventually(func() bool {
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tf), &infrav1.Terraform{})
		return err != nil && client.IgnoreNotFound(err) == nil
	}, timeout, interval).Should(BeTrue())
}

func jsonValue(raw string) *apiextensionsv1.JSON { return &apiextensionsv1.JSON{Raw: []byte(raw)} }

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// emptyState rewrites a local state file to contain no resources while
// keeping lineage and bumping the serial, simulating infrastructure that was
// removed behind Terraform's back.
func emptyState(path string) {
	data, err := os.ReadFile(path)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	var state map[string]any
	ExpectWithOffset(1, json.Unmarshal(data, &state)).To(Succeed())
	state["resources"] = []any{}
	state["outputs"] = map[string]any{}
	if serial, ok := state["serial"].(float64); ok {
		state["serial"] = serial + 1
	}
	out, err := json.MarshalIndent(state, "", "  ")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, os.WriteFile(path, out, 0o600)).To(Succeed())
}
