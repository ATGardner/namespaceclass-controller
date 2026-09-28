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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
)

var _ = Describe("NamespaceClass Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		typeNamespacedName := types.NamespacedName{Name: resourceName}
		ctx := context.Background()

		BeforeEach(func() {
			By("creating the custom resource for the Kind NamespaceClass")
			namespaceclass := &namespaceclassv1alpha1.NamespaceClass{}
			err := k8sClient.Get(ctx, typeNamespacedName, namespaceclass)
			if err != nil && errors.IsNotFound(err) {
				resource := &namespaceclassv1alpha1.NamespaceClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &namespaceclassv1alpha1.NamespaceClass{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if errors.IsNotFound(err) {
				return // an It that tests deletion may have already finished it
			}
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance NamespaceClass")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// Delete() only requests deletion; the finalizer blocks actual
			// removal until reconcileDelete runs, and no manager is running
			// in this suite to do that on its own — drive it explicitly, or
			// the object leaks into the next It as a stuck Terminating
			// object that the next BeforeEach's IsNotFound check won't catch.
			controllerReconciler := &NamespaceClassReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("should successfully reconcile the resource", func() {
			controllerReconciler := &NamespaceClassReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			req := reconcile.Request{NamespacedName: typeNamespacedName}

			By("Reconciling the created resource (adds the finalizer, returns early)")
			_, err := controllerReconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			resource := &namespaceclassv1alpha1.NamespaceClass{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Finalizers).ToNot(BeEmpty())
			Expect(resource.Status.Conditions).To(BeEmpty()) // status body hasn't run yet

			By("Reconciling again (finalizer already present, now computes status)")
			_, err = controllerReconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			cond := meta.FindStatusCondition(resource.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(resource.Status.FailingNamespaces).To(Equal(0))
		})

		Context("and some referencing Namespaces have failed", func() {
			var failingNs, healthyNs *corev1.Namespace

			BeforeEach(func() {
				failingNs = &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "failing-ns",
						Labels:      map[string]string{common.NamespaceClassLabel: resourceName},
						Annotations: map[string]string{common.ReconcileErrorAnnotation: "boom"},
					},
				}
				Expect(k8sClient.Create(ctx, failingNs)).To(Succeed())

				healthyNs = &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "healthy-ns",
						Labels: map[string]string{common.NamespaceClassLabel: resourceName},
					},
				}
				Expect(k8sClient.Create(ctx, healthyNs)).To(Succeed())
			})

			AfterEach(func() {
				forceDeleteNamespace(ctx, failingNs)
				forceDeleteNamespace(ctx, healthyNs)
			})

			It("reports the failing count and a non-ready condition", func() {
				controllerReconciler := &NamespaceClassReconciler{
					Client: k8sClient,
					Scheme: k8sClient.Scheme(),
				}
				req := reconcile.Request{NamespacedName: typeNamespacedName}

				_, err := controllerReconciler.Reconcile(ctx, req) // adds finalizer
				Expect(err).NotTo(HaveOccurred())
				_, err = controllerReconciler.Reconcile(ctx, req) // computes status
				Expect(err).NotTo(HaveOccurred())

				resource := &namespaceclassv1alpha1.NamespaceClass{}
				Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())

				Expect(resource.Status.FailingNamespaces).To(Equal(1))
				cond := meta.FindStatusCondition(resource.Status.Conditions, "Ready")
				Expect(cond).NotTo(BeNil())
				Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				Expect(cond.Reason).To(Equal("NamespacesFailing"))
			})
		})

		Context("and a Namespace references a different class", func() {
			var otherNs *corev1.Namespace

			BeforeEach(func() {
				otherNs = &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "other-class-ns",
						Labels:      map[string]string{common.NamespaceClassLabel: "some-other-class"},
						Annotations: map[string]string{common.ReconcileErrorAnnotation: "boom"},
					},
				}
				Expect(k8sClient.Create(ctx, otherNs)).To(Succeed())
			})

			AfterEach(func() {
				forceDeleteNamespace(ctx, otherNs)
			})

			It("does not count it toward this class's failing total", func() {
				controllerReconciler := &NamespaceClassReconciler{
					Client: k8sClient,
					Scheme: k8sClient.Scheme(),
				}
				req := reconcile.Request{NamespacedName: typeNamespacedName}

				_, err := controllerReconciler.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				_, err = controllerReconciler.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())

				resource := &namespaceclassv1alpha1.NamespaceClass{}
				Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
				Expect(resource.Status.FailingNamespaces).To(Equal(0))
				cond := meta.FindStatusCondition(resource.Status.Conditions, "Ready")
				Expect(cond).NotTo(BeNil())
				Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			})
		})

		Context("and the class still owns resources when it is deleted", func() {
			var ns *corev1.Namespace

			BeforeEach(func() {
				ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "owned-on-delete-ns"}}
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			})

			AfterEach(func() {
				forceDeleteNamespace(ctx, ns)
			})

			It("deletes the resources it owns, and only those, before removing the finalizer", func() {
				controllerReconciler := &NamespaceClassReconciler{
					Client: k8sClient,
					Scheme: k8sClient.Scheme(),
				}
				req := reconcile.Request{NamespacedName: typeNamespacedName}

				_, err := controllerReconciler.Reconcile(ctx, req) // adds finalizer
				Expect(err).NotTo(HaveOccurred())

				resource := &namespaceclassv1alpha1.NamespaceClass{}
				Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
				resource.Spec.Resources = []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: owned-cm
`)}
				Expect(k8sClient.Update(ctx, resource)).To(Succeed())

				owned := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name:      "owned-cm",
					Namespace: ns.Name,
					Labels:    map[string]string{common.ParentClassLabel: resourceName},
				}}
				Expect(controllerutil.SetControllerReference(resource, owned, k8sClient.Scheme())).To(Succeed())
				Expect(k8sClient.Create(ctx, owned)).To(Succeed())

				unowned := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
					Name:      "unowned-cm",
					Namespace: ns.Name,
					Labels:    map[string]string{common.ParentClassLabel: resourceName},
				}}
				Expect(k8sClient.Create(ctx, unowned)).To(Succeed())

				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

				_, err = controllerReconciler.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())

				err = k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: owned.Name}, &corev1.ConfigMap{})
				Expect(errors.IsNotFound(err)).To(BeTrue())

				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: unowned.Name}, &corev1.ConfigMap{})).To(Succeed())

				err = k8sClient.Get(ctx, typeNamespacedName, resource)
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})
		})
	})

	Context("When tracking applied GVKs", func() {
		ctx := context.Background()

		configMapGVK := metav1.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
		secretGVK := metav1.GroupVersionKind{Version: "v1", Kind: "Secret"}

		var (
			class      *namespaceclassv1alpha1.NamespaceClass
			ns         *corev1.Namespace
			reconciler *NamespaceClassReconciler
			req        reconcile.Request
		)

		// createClass creates a class holding a ConfigMap and a Secret, and
		// reconciles it twice: once to add the finalizer, once for status.
		createClass := func(name string) {
			class = &namespaceclassv1alpha1.NamespaceClass{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: namespaceclassv1alpha1.NamespaceClassSpec{
					Resources: []unstructured.Unstructured{
						toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: tracked-cm
`),
						toUnstructured(`
apiVersion: v1
kind: Secret
metadata:
  name: tracked-secret
`),
					},
				},
			}
			Expect(k8sClient.Create(ctx, class)).To(Succeed())

			reconciler = &NamespaceClassReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			req = reconcile.Request{NamespacedName: types.NamespacedName{Name: name}}
			for range 2 {
				_, err := reconciler.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
			}

			Expect(k8sClient.Get(ctx, req.NamespacedName, class)).To(Succeed())
		}

		// dropSecretFromSpec edits the class to keep only its ConfigMap.
		dropSecretFromSpec := func() {
			Expect(k8sClient.Get(ctx, req.NamespacedName, class)).To(Succeed())
			class.Spec.Resources = class.Spec.Resources[:1]
			Expect(k8sClient.Update(ctx, class)).To(Succeed())
		}

		// createSecret creates a Secret labelled with the class as its
		// parent, optionally with the class as its controller.
		createSecret := func(name string, owned bool) *corev1.Secret {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns.Name,
					Labels:    map[string]string{common.ParentClassLabel: class.Name},
				},
			}
			if owned {
				Expect(controllerutil.SetControllerReference(class, secret, k8sClient.Scheme())).To(Succeed())
			}

			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			return secret
		}

		BeforeEach(func() {
			ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "applied-gvks-"}}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteNamespace(ctx, ns)

			Expect(k8sClient.Delete(ctx, class)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, req) // removes the finalizer
			Expect(err).NotTo(HaveOccurred())
		})

		It("records the spec's GVKs in status", func() {
			createClass("applied-gvks-spec")

			Expect(class.Status.AppliedGVKs).To(Equal([]metav1.GroupVersionKind{configMapGVK, secretGVK}))
		})

		It("keeps a GVK dropped from the spec until the class's last resource of it is gone", func() {
			createClass("applied-gvks-retiring")
			secret := createSecret("retiring-secret", true)

			By("dropping the Secret from the spec while an owned Secret remains")
			dropSecretFromSpec()

			result, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			Expect(k8sClient.Get(ctx, req.NamespacedName, class)).To(Succeed())
			Expect(class.Status.AppliedGVKs).To(Equal([]metav1.GroupVersionKind{configMapGVK, secretGVK}))

			By("deleting the last owned Secret")
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())

			result, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			Expect(k8sClient.Get(ctx, req.NamespacedName, class)).To(Succeed())
			Expect(class.Status.AppliedGVKs).To(Equal([]metav1.GroupVersionKind{configMapGVK}))
		})

		It("does not keep a dropped GVK for a labelled resource the class doesn't own", func() {
			createClass("applied-gvks-unowned")
			createSecret("unowned-secret", false)

			dropSecretFromSpec()

			result, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			Expect(k8sClient.Get(ctx, req.NamespacedName, class)).To(Succeed())
			Expect(class.Status.AppliedGVKs).To(Equal([]metav1.GroupVersionKind{configMapGVK}))
		})
	})

	Context("When the NamespaceClass does not exist", func() {
		It("returns without error", func() {
			controllerReconciler := &NamespaceClassReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist"},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

// forceDeleteNamespace deletes ns and clears its built-in "kubernetes"
// finalizer directly via the finalize subresource. envtest runs no
// namespace-lifecycle controller to do that on its own, so a plain Delete()
// only marks a Namespace Terminating forever — it never actually leaves
// etcd, and keeps matching later tests' label selectors indefinitely.
func forceDeleteNamespace(ctx context.Context, ns *corev1.Namespace) {
	Expect(k8sClient.Delete(ctx, ns)).To(Succeed())

	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns.Name}, ns)).To(Succeed())
	ns.Spec.Finalizers = []corev1.FinalizerName{}
	Expect(k8sClient.SubResource("finalize").Update(ctx, ns)).To(Succeed())
}
