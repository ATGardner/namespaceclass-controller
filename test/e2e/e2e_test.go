//go:build e2e
// +build e2e

/*
Copyright 2026.

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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/atgardner/namespaceclass-controller/test/utils"
)

// namespaceClassLabel is namespaceclassv1alpha1's label key, duplicated here
// as a literal since these tests only ever speak to the cluster through
// kubectl, never by importing the controller's own Go types.
const namespaceClassLabel = "namespaceclass.akuity.io/name"

// namespace where the project is deployed in
const namespace = "namespaceclass-controller-system"

// serviceAccountName created for the project
const serviceAccountName = "namespaceclass-controller-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "namespaceclass-controller-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "namespaceclass-controller-metrics-binding"

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
				"--clusterrole=namespaceclass-controller-metrics-reader",
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

	// Each It here is fully independent — its own NamespaceClass and its own
	// Namespace, never shared with another It — specifically so a failure in
	// one never leaves a wrong precondition for the next. The controller
	// deployed in BeforeAll above is shared (that's the expensive part), but
	// nothing about a test's own resources is.
	Context("NamespaceClass", func() {
		It("applies a class's resources, resolving {{ .namespace }}", func() {
			className, nsName, cmName := "e2e-apply-class", "e2e-apply-ns", "e2e-apply-cm"
			DeferCleanup(func() {
				deleteTestNamespace(nsName)
				deleteNamespaceClass(className)
			})

			applyNamespaceClass(className, cmName)
			applyLabeledNamespace(nsName, className)

			Eventually(func(g Gomega) {
				val, err := configMapOwner(nsName, cmName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(val).To(Equal(nsName), "ConfigMap data should resolve {{ .namespace }} to the real namespace")
			}).Should(Succeed())
		})

		It("reports the class as Ready once applied", func() {
			className, nsName, cmName := "e2e-status-class", "e2e-status-ns", "e2e-status-cm"
			DeferCleanup(func() {
				deleteTestNamespace(nsName)
				deleteNamespaceClass(className)
			})

			applyNamespaceClass(className, cmName)
			applyLabeledNamespace(nsName, className)

			Eventually(func(g Gomega) {
				status, err := namespaceClassCondition(className, "Ready")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal("True"))
			}).Should(Succeed())
		})

		It("corrects drift on a managed resource", func() {
			className, nsName, cmName := "e2e-drift-class", "e2e-drift-ns", "e2e-drift-cm"
			DeferCleanup(func() {
				deleteTestNamespace(nsName)
				deleteNamespaceClass(className)
			})

			applyNamespaceClass(className, cmName)
			applyLabeledNamespace(nsName, className)

			Eventually(func(g Gomega) {
				val, err := configMapOwner(nsName, cmName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(val).To(Equal(nsName))
			}).Should(Succeed())

			By("directly patching the ConfigMap's data, simulating an external actor")
			cmd := exec.Command("kubectl", "patch", "configmap", cmName, "-n", nsName,
				"--type=merge", "-p", `{"data":{"owner":"someone-else"}}`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("the watcher noticing and reverting it, with no reconcile triggered by us")
			Eventually(func(g Gomega) {
				val, err := configMapOwner(nsName, cmName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(val).To(Equal(nsName))
			}).Should(Succeed())
		})

		It("deletes the managed resource when the class label is removed", func() {
			className, nsName, cmName := "e2e-unlabel-class", "e2e-unlabel-ns", "e2e-unlabel-cm"
			DeferCleanup(func() {
				deleteTestNamespace(nsName)
				deleteNamespaceClass(className)
			})

			applyNamespaceClass(className, cmName)
			applyLabeledNamespace(nsName, className)

			Eventually(func(g Gomega) {
				_, err := configMapOwner(nsName, cmName)
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			By("removing the class label from the Namespace")
			cmd := exec.Command("kubectl", "label", "ns", nsName, namespaceClassLabel+"-")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(configMapExists(nsName, cmName)).To(BeFalse())
			}).Should(Succeed())
		})

		It("recreates the managed resource when the class label is re-added", func() {
			className, nsName, cmName := "e2e-relabel-class", "e2e-relabel-ns", "e2e-relabel-cm"
			DeferCleanup(func() {
				deleteTestNamespace(nsName)
				deleteNamespaceClass(className)
			})

			applyNamespaceClass(className, cmName)
			applyLabeledNamespace(nsName, className)
			Eventually(func(g Gomega) {
				g.Expect(configMapExists(nsName, cmName)).To(BeTrue())
			}).Should(Succeed())

			By("removing the class label")
			cmd := exec.Command("kubectl", "label", "ns", nsName, namespaceClassLabel+"-")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				g.Expect(configMapExists(nsName, cmName)).To(BeFalse())
			}).Should(Succeed())

			By("re-adding the class label")
			cmd = exec.Command("kubectl", "label", "ns", nsName, fmt.Sprintf("%s=%s", namespaceClassLabel, className))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(configMapExists(nsName, cmName)).To(BeTrue())
			}).Should(Succeed())
		})

		It("cascades on class deletion: the resource and the class itself both disappear", func() {
			className, nsName, cmName := "e2e-cascade-class", "e2e-cascade-ns", "e2e-cascade-cm"
			DeferCleanup(func() {
				deleteTestNamespace(nsName)
				deleteNamespaceClass(className)
			})

			applyNamespaceClass(className, cmName)
			applyLabeledNamespace(nsName, className)
			Eventually(func(g Gomega) {
				g.Expect(configMapExists(nsName, cmName)).To(BeTrue())
			}).Should(Succeed())

			By("deleting the NamespaceClass")
			deleteNamespaceClass(className)

			By("the managed ConfigMap being cleaned up")
			Eventually(func(g Gomega) {
				g.Expect(configMapExists(nsName, cmName)).To(BeFalse())
			}).Should(Succeed())

			By("the finalizer eventually clearing, so the NamespaceClass itself is actually gone")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "namespaceclass", className)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "NamespaceClass should no longer exist")
			}).Should(Succeed())
		})
	})
})

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

// applyNamespaceClass creates a NamespaceClass named className whose single
// managed resource is a ConfigMap named cmName, with a data field templated
// on {{ .namespace }} — so callers can check both that the resource got
// applied at all and that templating actually resolved.
func applyNamespaceClass(className, cmName string) {
	manifest := fmt.Sprintf(`
apiVersion: namespaceclass.akuity.io/v1alpha1
kind: NamespaceClass
metadata:
  name: %s
spec:
  resources:
    - apiVersion: v1
      kind: ConfigMap
      metadata:
        name: %s
      data:
        owner: "{{ .namespace }}"
`, className, cmName)

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply NamespaceClass %s", className)
}

// applyLabeledNamespace creates a Namespace named nsName labeled to
// reference className.
func applyLabeledNamespace(nsName, className string) {
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    %s: %s
`, nsName, namespaceClassLabel, className)

	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply Namespace %s", nsName)
}

// deleteNamespaceClass deletes a NamespaceClass, tolerating one that's
// already gone — used both mid-test and in DeferCleanup, where the class may
// or may not still exist depending on how far the test got.
func deleteNamespaceClass(className string) {
	cmd := exec.Command("kubectl", "delete", "namespaceclass", className, "--ignore-not-found", "--wait=false")
	_, _ = utils.Run(cmd)
}

// deleteTestNamespace deletes one of these tests' own Namespaces, tolerating
// one that's already gone.
func deleteTestNamespace(nsName string) {
	cmd := exec.Command("kubectl", "delete", "ns", nsName, "--ignore-not-found", "--wait=false")
	_, _ = utils.Run(cmd)
}

// configMapOwner returns the value of data.owner on the named ConfigMap.
func configMapOwner(nsName, cmName string) (string, error) {
	cmd := exec.Command("kubectl", "get", "configmap", cmName, "-n", nsName,
		"-o", "jsonpath={.data.owner}")
	return utils.Run(cmd)
}

// configMapExists reports whether the named ConfigMap currently exists.
func configMapExists(nsName, cmName string) bool {
	cmd := exec.Command("kubectl", "get", "configmap", cmName, "-n", nsName)
	_, err := utils.Run(cmd)
	return err == nil
}

// namespaceClassCondition returns the Status of the named condition type on
// a NamespaceClass's status.conditions.
func namespaceClassCondition(className, conditionType string) (string, error) {
	cmd := exec.Command("kubectl", "get", "namespaceclass", className,
		"-o", fmt.Sprintf(`jsonpath={.status.conditions[?(@.type=="%s")].status}`, conditionType))
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
