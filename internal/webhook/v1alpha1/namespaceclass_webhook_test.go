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

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
)

var _ = Describe("NamespaceClass Webhook", func() {
	var validator *NamespaceClassValidator

	BeforeEach(func() {
		// Wired against the suite's real client (backed by envtest's live
		// control plane), so RESTMapper() reflects real discovery: ConfigMap
		// is namespaced, ClusterRole is cluster-scoped, and a made-up kind is
		// genuinely unresolvable. Nothing here is persisted to the cluster -
		// ValidateCreate/ValidateUpdate are called directly, not through
		// k8sClient.Create - so there's no cleanup to do between tests.
		validator = &NamespaceClassValidator{Client: k8sClient}
	})

	Context("When creating or updating a NamespaceClass", func() {
		It("admits a class whose resources are all namespaced", func() {
			obj := newTestNamespaceClass(toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: valid-cm
`))

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("rejects a cluster-scoped resource", func() {
			obj := newTestNamespaceClass(toUnstructured(`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cr
rules: []
`))

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not namespaced"))
		})

		It("rejects an unresolvable kind", func() {
			obj := newTestNamespaceClass(toUnstructured(`
apiVersion: bogus.example.com/v1
kind: TotallyFake
metadata:
  name: fake
`))

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
		})

		It("aggregates errors from every bad resource, not just the first", func() {
			obj := newTestNamespaceClass(
				toUnstructured(`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: multi-cr
rules: []
`),
				toUnstructured(`
apiVersion: bogus.example.com/v1
kind: TotallyFake
metadata:
  name: multi-fake
`),
			)

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ClusterRole"))
			Expect(err.Error()).To(ContainSubstring("TotallyFake"))
		})

		It("validates updates the same way as creates", func() {
			oldObj := newTestNamespaceClass()
			newObj := newTestNamespaceClass(toUnstructured(`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: update-cr
rules: []
`))

			_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not namespaced"))
		})

		It("allows an update on an object already being deleted, even with invalid resources", func() {
			// Simulates reconcileDelete's finalizer-removal update on a class
			// that only ever got created because the webhook was unreachable
			// at the time. Without this, that update would be rejected by
			// the same check that should have caught it at creation - making
			// the object permanently undeletable.
			obj := newTestNamespaceClass(toUnstructured(`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: stuck-cr
rules: []
`))
			now := metav1.Now()
			obj.DeletionTimestamp = &now
			oldObj := obj.DeepCopy()

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

func newTestNamespaceClass(resources ...unstructured.Unstructured) *namespaceclassv1alpha1.NamespaceClass {
	return &namespaceclassv1alpha1.NamespaceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook-test-class"},
		Spec:       namespaceclassv1alpha1.NamespaceClassSpec{Resources: resources},
	}
}

func toUnstructured(data string) unstructured.Unstructured {
	var res unstructured.Unstructured
	Expect(yaml.Unmarshal([]byte(data), &res)).To(Succeed())
	return res
}
