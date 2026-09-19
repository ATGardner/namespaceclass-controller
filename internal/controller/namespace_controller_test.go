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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
	"github.com/atgardner/namespaceclass-controller/internal/manager"
)

var _ = Describe("Namespace Controller", func() {
	ctx := context.Background()

	It("adds a new resource and removes an old one when the class spec changes", func() {
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "diff-class"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-a
data:
  foo: bar
`)},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, class)).To(Succeed()) })

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "diff-ns",
				Labels: map[string]string{common.NamespaceClassLabel: class.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		By("reconciling once so cm-a exists")
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var cmA corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "cm-a"}, &cmA)).To(Succeed())

		By("editing the class to drop cm-a and add cm-b")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: class.Name}, class)).To(Succeed())
		class.Spec.Resources = []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-b
data:
  foo: baz
`)}
		Expect(k8sClient.Update(ctx, class)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var cmB corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "cm-b"}, &cmB)).To(Succeed())

		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "cm-a"}, &cmA)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("deletes previously applied resources when the class label is removed", func() {
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "unlabel-class"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: unlabel-cm
data:
  foo: bar
`)},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, class)).To(Succeed()) })

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "unlabel-ns",
				Labels: map[string]string{common.NamespaceClassLabel: class.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "unlabel-cm"}, &cm)).To(Succeed())

		By("removing the class label")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns.Name}, ns)).To(Succeed())
		delete(ns.Labels, common.NamespaceClassLabel)
		Expect(k8sClient.Update(ctx, ns)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "unlabel-cm"}, &cm)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns.Name}, ns)).To(Succeed())
		resources, err := readAppliedResources(ns)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources).To(BeEmpty())
	})

	It("applies the new class's resources and removes the old class's when the label value changes", func() {
		classA := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "switch-class-a"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-from-a
data:
  foo: bar
`)},
			},
		}
		Expect(k8sClient.Create(ctx, classA)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, classA)).To(Succeed()) })

		classB := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "switch-class-b"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-from-b
data:
  foo: baz
`)},
			},
		}
		Expect(k8sClient.Create(ctx, classB)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, classB)).To(Succeed()) })

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "switch-ns",
				Labels: map[string]string{common.NamespaceClassLabel: classA.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var cmFromA corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "cm-from-a"}, &cmFromA)).To(Succeed())

		By("switching the namespace from class A to class B")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns.Name}, ns)).To(Succeed())
		ns.Labels[common.NamespaceClassLabel] = classB.Name
		Expect(k8sClient.Update(ctx, ns)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var cmFromB corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "cm-from-b"}, &cmFromB)).To(Succeed())

		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "cm-from-a"}, &cmFromA)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("converges a Namespace to empty when its class is being deleted", func() {
		// Held alive through deletion by a finalizer that belongs to this test
		// only, not the controller's own — purely so the test can observe the
		// DeletionTimestamp-set-but-still-Gettable window a real finalizer-gated
		// deletion goes through.
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "terminating-class",
				Finalizers: []string{"test.namespaceclass.akuity.io/hold-for-test"},
			},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: terminating-cm
data:
  foo: bar
`)},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: class.Name}, class)).To(Succeed())
			class.Finalizers = nil
			Expect(k8sClient.Update(ctx, class)).To(Succeed())
		})

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "terminating-ns",
				Labels: map[string]string{common.NamespaceClassLabel: class.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "terminating-cm"}, &cm)).To(Succeed())

		By("deleting the class while its own finalizer keeps it around")
		Expect(k8sClient.Delete(ctx, class)).To(Succeed())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: class.Name}, class)).To(Succeed())
		Expect(class.DeletionTimestamp).NotTo(BeNil())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "terminating-cm"}, &cm)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("converges a Namespace to empty when its referenced class no longer exists", func() {
		// The class is created with no finalizer, so unlike the "terminating"
		// case above, Delete() here removes it immediately and fully — the
		// Namespace's label is left pointing at a class that's genuinely gone,
		// not one that's merely mid-deletion.
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "vanished-class"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: vanished-cm
data:
  foo: bar
`)},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "vanished-ns",
				Labels: map[string]string{common.NamespaceClassLabel: class.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "vanished-cm"}, &cm)).To(Succeed())

		By("deleting the class outright, with nothing holding it back")
		Expect(k8sClient.Delete(ctx, class)).To(Succeed())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: class.Name}, class)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "vanished-cm"}, &cm)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("records a real apply failure on the Namespace and clears it once fixed", func() {
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "failing-apply-class"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				// No such kind is registered anywhere: the apply step must fail.
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: bogus.example.com/v1
kind: TotallyFake
metadata:
  name: nope
`)},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, class)).To(Succeed()) })

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "failing-apply-ns",
				Labels: map[string]string{common.NamespaceClassLabel: class.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).To(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns.Name}, ns)).To(Succeed())
		Expect(ns.Annotations[common.ReconcileErrorAnnotation]).NotTo(BeEmpty())

		By("fixing the class to something appliable")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: class.Name}, class)).To(Succeed())
		class.Spec.Resources = []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: fixed-cm
data:
  foo: bar
`)}
		Expect(k8sClient.Update(ctx, class)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns.Name}, ns)).To(Succeed())
		Expect(ns.Annotations[common.ReconcileErrorAnnotation]).To(BeEmpty())
	})

	It("resolves {{ .namespace }} in an applied resource's data", func() {
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "templated-class"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: templated-cm
data:
  owner: "{{ .namespace }}"
`)},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, class)).To(Succeed()) })

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "templated-ns",
				Labels: map[string]string{common.NamespaceClassLabel: class.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var cm corev1.ConfigMap
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "templated-cm"}, &cm)).To(Succeed())
		Expect(cm.Data["owner"]).To(Equal(ns.Name))
	})

	It("rejects a cluster-scoped resource and never applies it", func() {
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-scoped-class"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				// ClusterRole is cluster-scoped; the resolve step must reject it
				// before it ever reaches Patch.
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-scoped-cr
rules: []
`)},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, class)).To(Succeed()) })

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "cluster-scoped-ns",
				Labels: map[string]string{common.NamespaceClassLabel: class.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not namespaced"))

		var cr rbacv1.ClusterRole
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-scoped-cr"}, &cr)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("aggregates errors from every bad resource, not just the first", func() {
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: "multi-bad-class"},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{
					// Cluster-scoped: rejected by the RESTMapper scope check.
					toUnstructured(`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: multi-bad-cr
rules: []
`),
					// Unresolvable: rejected because the RESTMapper can't map it at all.
					toUnstructured(`
apiVersion: bogus.example.com/v1
kind: TotallyFake
metadata:
  name: multi-bad-fake
`),
					// Otherwise valid, to prove nothing gets applied once resolution
					// as a whole has failed.
					toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: multi-bad-cm
data:
  foo: bar
`),
				},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, class)).To(Succeed()) })

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "multi-bad-ns",
				Labels: map[string]string{common.NamespaceClassLabel: class.Name},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { forceDeleteNamespace(ctx, ns) })

		reconciler := &NamespaceReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), manager: manager.NoOp()}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).To(HaveOccurred())

		By("reporting both bad resources in the same error, not just the first one hit")
		Expect(err.Error()).To(ContainSubstring("ClusterRole"))
		Expect(err.Error()).To(ContainSubstring("TotallyFake"))

		By("applying nothing at all, including the one otherwise-valid resource")
		var cm corev1.ConfigMap
		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: "multi-bad-cm"}, &cm)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})
