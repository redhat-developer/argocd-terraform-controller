//go:build e2e

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

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/redhat-developer/argocd-terraform-controller/test/utils"
)

// namespace where the project is deployed in
const namespace = "argocd-terraform-controller-system"

// serviceAccountName created for the project
const serviceAccountName = "argocd-terraform-controller-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "argocd-terraform-controller-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "argocd-terraform-controller-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("pointing the controller at the runner image loaded into Kind")
		cmd = exec.Command("kubectl", "set", "env", "deployment/argocd-terraform-controller-controller-manager",
			"-n", namespace, "RUNNER_POD_IMAGE="+runnerImage)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to set the runner image")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=argocd-terraform-controller-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("Terraform", func() {
		const tfNamespace = "tf-e2e"

		BeforeAll(func() {
			By("creating the Terraform namespace with the runner ServiceAccount")
			_, err := utils.Run(exec.Command("kubectl", "create", "ns", tfNamespace))
			Expect(err).NotTo(HaveOccurred())
			cmd := exec.Command("make", "deploy-runner-rbac", "NAMESPACE="+tfNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the runner RBAC")

			By("letting the example manage Namespaces and ConfigMaps (e2e only)")
			cmd = exec.Command("kubectl", "create", "clusterrolebinding", "tf-e2e-runner-admin",
				"--clusterrole=cluster-admin", "--serviceaccount="+tfNamespace+":tf-runner")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			if os.Getenv("E2E_SOURCE_URL") == "" {
				By("serving the local checkout from a git daemon inside the cluster")
				Expect(deployGitServer(tfNamespace)).To(Succeed())
			}
		})

		AfterAll(func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "terraform", "--all", "-n", tfNamespace, "--wait=true", "--timeout=5m"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "clusterrolebinding", "tf-e2e-runner-admin"))
			_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", tfNamespace, "--wait=false"))
		})

		AfterEach(func() {
			if CurrentSpecReport().Failed() {
				for _, args := range [][]string{
					{"get", "terraform", "-n", tfNamespace, "-o", "yaml"},
					{"get", "pods,secrets,configmaps", "-n", tfNamespace},
					{"get", "events", "-n", tfNamespace, "--sort-by=.lastTimestamp"},
					{"logs", "-n", tfNamespace, "-l", "app.kubernetes.io/name=tf-runner", "--tail=200"},
					{"logs", "-n", namespace, "-l", "control-plane=controller-manager", "--tail=300"},
				} {
					out, _ := utils.Run(exec.Command("kubectl", args...))
					_, _ = fmt.Fprintf(GinkgoWriter, "$ kubectl %v\n%s\n", args, out)
				}
			}
		})

		for _, binary := range []string{"tofu", "terraform"} {
			It(fmt.Sprintf("applies, stores state in a Secret and destroys on deletion with %s", binary), func() {
				name := "kube-namespace-" + binary
				targetNS := "terraform-demo-" + binary
				manifest := fmt.Sprintf(`
apiVersion: infra.argocd.io/v1alpha1
kind: Terraform
metadata:
  name: %s
  namespace: %s
spec:
  source:
    git:
      url: %s
      ref:
        %s
  path: %s
  interval: 1m
  retryInterval: 15s
  approvePlan: auto
  destroyResourcesOnDeletion: true
  runner:
    binary: %s
  vars:
  - name: namespace
    value: %q
  tfstate:
    deleteOnDestroy: true
  writeOutputsToSecret:
    name: %s-outputs
`, name, tfNamespace, sourceURL(), sourceRef(), sourcePath(), binary, targetNS, name)
				Expect(kubectlApply(manifest)).To(Succeed())

				By("becoming Ready")
				Eventually(func(g Gomega) {
					out, err := utils.Run(exec.Command("kubectl", "get", "terraform", name, "-n", tfNamespace,
						"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}{\" \"}{.status.conditions[?(@.type==\"Ready\")].message}"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(out).To(HavePrefix("True "), out)
				}, 10*time.Minute, 5*time.Second).Should(Succeed())

				By("having used the requested binary")
				out, err := utils.Run(exec.Command("kubectl", "get", "terraform", name, "-n", tfNamespace, "-o", "jsonpath={.status.binary}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(HavePrefix(binary + " "))

				By("storing the state in the Kubernetes backend Secret")
				stateSecret := "tfstate-default-" + name
				_, err = utils.Run(exec.Command("kubectl", "get", "secret", stateSecret, "-n", tfNamespace))
				Expect(err).NotTo(HaveOccurred(), "state secret missing")
				out, err = utils.Run(exec.Command("kubectl", "get", "terraform", name, "-n", tfNamespace, "-o", "jsonpath={.status.stateSecretName}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(Equal(stateSecret))

				By("having created the managed resources and outputs")
				_, err = utils.Run(exec.Command("kubectl", "get", "ns", targetNS))
				Expect(err).NotTo(HaveOccurred())
				out, err = utils.Run(exec.Command("kubectl", "get", "secret", name+"-outputs", "-n", tfNamespace, "-o", "jsonpath={.data.namespace}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(Equal(base64.StdEncoding.EncodeToString([]byte(targetNS))))

				By("storing the human readable plan in a ConfigMap")
				out, err = utils.Run(exec.Command("kubectl", "get", "configmap", "tfplan-default-"+name+"-readable", "-n", tfNamespace, "-o", "jsonpath={.data.tfplan}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring("kubernetes_namespace.demo"))

				By("cleaning up the runner pod after the run")
				Eventually(func() error {
					_, err := utils.Run(exec.Command("kubectl", "get", "pod", name+"-tf-runner", "-n", tfNamespace))
					if err == nil {
						return fmt.Errorf("runner pod still present")
					}
					return nil
				}, 3*time.Minute, 5*time.Second).Should(Succeed())

				By("destroying resources and deleting the state on deletion")
				_, err = utils.Run(exec.Command("kubectl", "delete", "terraform", name, "-n", tfNamespace, "--wait=true", "--timeout=10m"))
				Expect(err).NotTo(HaveOccurred())
				Eventually(func() error {
					_, err := utils.Run(exec.Command("kubectl", "get", "ns", targetNS))
					if err == nil {
						return fmt.Errorf("namespace %s still exists", targetNS)
					}
					return nil
				}, 5*time.Minute, 5*time.Second).Should(Succeed())
				_, err = utils.Run(exec.Command("kubectl", "get", "secret", stateSecret, "-n", tfNamespace))
				Expect(err).To(HaveOccurred(), "state secret should have been deleted")
				_, err = utils.Run(exec.Command("kubectl", "get", "secret", name+"-outputs", "-n", tfNamespace))
				Expect(err).To(HaveOccurred(), "outputs secret should have been deleted")
			})
		}
	})
})

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// gitServerURL is the in-cluster git daemon used when E2E_SOURCE_URL is unset.
const gitServerURL = "git://git-server.tf-e2e.svc.cluster.local/repo.git"

func sourceURL() string {
	return envOr("E2E_SOURCE_URL", gitServerURL)
}

// sourceRef returns the YAML of spec.source.git.ref, e.g. "branch: main".
// The in-cluster server exposes the local HEAD as "main".
func sourceRef() string {
	return envOr("E2E_SOURCE_REF", "branch: main")
}

// deployGitServer runs a git daemon Pod in ns seeded with a bare clone of the
// local repository (HEAD becomes "main").
func deployGitServer(ns string) error {
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: git-server
  namespace: %[1]s
  labels: {app: git-server}
spec:
  containers:
  - name: git
    image: docker.io/library/alpine:3.23
    command: ["sh", "-c", "apk add --no-cache git git-daemon && mkdir -p /repos && touch /tmp/ready && exec git daemon --export-all --base-path=/repos --reuseaddr --verbose"]
    ports: [{containerPort: 9418, name: git}]
    readinessProbe:
      exec: {command: ["test", "-f", "/tmp/ready"]}
      periodSeconds: 2
---
apiVersion: v1
kind: Service
metadata:
  name: git-server
  namespace: %[1]s
spec:
  selector: {app: git-server}
  ports: [{port: 9418, targetPort: 9418}]
`, ns)
	if err := kubectlApply(manifest); err != nil {
		return err
	}
	if _, err := utils.Run(exec.Command("kubectl", "wait", "pod/git-server", "-n", ns, "--for=condition=Ready", "--timeout=5m")); err != nil {
		return err
	}

	projectDir, err := utils.GetProjectDir()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "e2e-repo-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	bare := filepath.Join(tmp, "repo.git")
	if _, err := utils.Run(exec.Command("git", "-c", "safe.bareRepository=all", "clone", "--quiet", "--bare", projectDir, bare)); err != nil {
		return err
	}
	// Expose the local HEAD as "main" regardless of the checked out branch.
	head, err := utils.Run(exec.Command("git", "-C", projectDir, "rev-parse", "HEAD"))
	if err != nil {
		return err
	}
	if _, err := utils.Run(exec.Command("git", "-c", "safe.bareRepository=all", "-C", bare, "update-ref", "refs/heads/main", strings.TrimSpace(head))); err != nil {
		return err
	}
	if _, err := utils.Run(exec.Command("git", "-c", "safe.bareRepository=all", "-C", bare, "symbolic-ref", "HEAD", "refs/heads/main")); err != nil {
		return err
	}
	_, err = utils.Run(exec.Command("kubectl", "cp", bare, ns+"/git-server:/repos/repo.git"))
	return err
}

func sourcePath() string {
	return envOr("E2E_SOURCE_PATH", "./examples/kube-namespace")
}

func kubectlApply(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
