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

package enforcer

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
)

var _ = Describe("ResourceEnforcer Webhook", func() {
	var enforcer *ResourceEnforcer

	BeforeEach(func() {
		enforcer = &ResourceEnforcer{
			Client:             k8sClient,
			controllerIdentity: "some-identity", // tests will always be "", so needs to be different
			decoder:            admission.NewDecoder(k8sClient.Scheme()),
		}
	})

	It("allows a resource with no parent-class label untouched", func() {
		obj := toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: unlabeled-cm
data:
  foo: whatever
`)

		resp := enforcer.Handle(ctx, newAdmissionRequest(&obj))
		Expect(resp.Allowed).To(BeTrue())
		Expect(resp.Patches).To(BeEmpty())
		Expect(resp.Warnings).To(BeEmpty())
	})

	It("allows a resource whose class no longer exists", func() {
		obj := toUnstructured(fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: orphaned-cm
  labels:
    %s: does-not-exist
data:
  foo: whatever
`, common.ParentClassLabel))

		resp := enforcer.Handle(ctx, newAdmissionRequest(&obj))
		Expect(resp.Allowed).To(BeTrue())
		Expect(resp.Patches).To(BeEmpty())
	})

	It("reverts drift on a field the class manages, leaving unmanaged fields alone", func() {
		class := newTestClass("enforcer-drift-class", toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: drift-cm
data:
  foo: bar
`))
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, class)).To(Succeed()) })

		// Built the same way the real resource would be (ownerReference,
		// parent label and all), so the only difference from what the
		// enforcer computes as "desired" is the drift introduced below -
		// not an artifact of a hand-written fixture missing fields the real
		// object would always carry.
		incoming, err := common.NewResourceBuilder(k8sClient).BuildFinalResource(
			&class.Spec.Resources[0], "enforcer-ns", class)
		Expect(err).NotTo(HaveOccurred())

		// "foo" has drifted from the class's "bar"; "extra" isn't in the
		// class's template at all, so it belongs to some other writer.
		Expect(unstructured.SetNestedField(incoming.Object, "tampered", "data", "foo")).To(Succeed())
		Expect(unstructured.SetNestedField(incoming.Object, "keep-me", "data", "extra")).To(Succeed())

		resp := enforcer.Handle(ctx, newAdmissionRequest(incoming))
		Expect(resp.Allowed).To(BeTrue())
		Expect(resp.Warnings).NotTo(BeEmpty())

		var revertedFoo bool
		for _, p := range resp.Patches {
			Expect(p.Path).NotTo(Equal("/data/extra"), "unmanaged field should never be patched")
			if p.Path == "/data/foo" {
				revertedFoo = true
				Expect(p.Value).To(Equal("bar"))
			}
		}
		Expect(revertedFoo).To(BeTrue(), "expected a patch reverting /data/foo back to the class's value")
	})

	It("makes no patch when the resource already matches the class", func() {
		class := newTestClass("enforcer-clean-class", toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: clean-cm
data:
  foo: bar
`))
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, class)).To(Succeed()) })

		// Built via BuildFinalResource, same as the enforcer's own
		// getDesiredResource does - so it carries the real ownerReference
		// (with the class's actual UID) that a static YAML fixture can't
		// reproduce, and the two are byte-for-byte the same object.
		incoming, err := common.NewResourceBuilder(k8sClient).BuildFinalResource(
			&class.Spec.Resources[0], "enforcer-ns", class)
		Expect(err).NotTo(HaveOccurred())

		resp := enforcer.Handle(ctx, newAdmissionRequest(incoming))
		Expect(resp.Allowed).To(BeTrue())
		Expect(resp.Patches).To(BeEmpty())
		Expect(resp.Warnings).To(BeEmpty())
	})
})

func newTestClass(name string, resources ...unstructured.Unstructured) *namespaceclassv1alpha1.NamespaceClass {
	return &namespaceclassv1alpha1.NamespaceClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       namespaceclassv1alpha1.NamespaceClassSpec{Resources: resources},
	}
}

// newAdmissionRequest builds a request the way the real webhook server
// receives one: only Object.Raw is populated (the wire-format JSON bytes),
// never Object.Object. A handler that reads Object.Object without decoding
// Raw into it first will see a request that looks unlabeled no matter what
// obj actually contains.
func newAdmissionRequest(obj *unstructured.Unstructured) admission.Request {
	raw, err := obj.MarshalJSON()
	Expect(err).NotTo(HaveOccurred())

	gvk := obj.GroupVersionKind()
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UID:       "00000000-0000-0000-0000-000000000000",
			Kind:      metav1.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind},
			Name:      obj.GetName(),
			Namespace: "enforcer-ns",
			Operation: admissionv1.Update,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func toUnstructured(data string) unstructured.Unstructured {
	var res unstructured.Unstructured
	Expect(yaml.Unmarshal([]byte(data), &res)).To(Succeed())
	return res
}
