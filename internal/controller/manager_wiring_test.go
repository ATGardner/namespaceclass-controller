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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
)

// This suite runs a real Manager, scoped to just this Describe block's own
// BeforeEach/AfterEach rather than the shared BeforeSuite — the other test
// files in this package call Reconcile directly and depend on controlling
// exactly how many times it runs; a manager shared across the whole suite
// would race in and reconcile their objects independently in the
// background. Ginkgo runs specs sequentially by default, so scoping the
// manager's lifetime to this block's own hooks keeps it from ever running
// concurrently with theirs.
var _ = Describe("Controller wiring (via manager)", func() {
	var (
		mgrCtx    context.Context
		mgrCancel context.CancelFunc
	)

	BeforeEach(func() {
		mgrCtx, mgrCancel = context.WithCancel(context.Background())

		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  k8sClient.Scheme(),
			Metrics: metricsserver.Options{BindAddress: "0"},
			// Each It in this Describe spins up its own Manager, so the same
			// controller names ("namespace", "namespaceclass") get
			// registered more than once in this one test binary process -
			// harmless here since only one Manager is ever running at a
			// time, but controller-runtime's cross-Manager uniqueness check
			// doesn't know that.
			Controller: config.Controller{SkipNameValidation: new(true)},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect((&NamespaceClassReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr)).To(Succeed())

		Expect((&NamespaceReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr)).To(Succeed())

		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
	})

	AfterEach(func() {
		mgrCancel()
	})

	It("converges and reports Ready without any explicit Reconcile call", func() {
		ctx := context.Background()
		const (
			className = "wired-class"
			nsName    = "wired-ns"
			cmName    = "wired-cm"
		)

		u := toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: wired-cm
data:
  foo: bar
`)
		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{u},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nsName,
				Labels: map[string]string{common.NamespaceClassLabel: className},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		By("the watched ConfigMap getting created without a manual Reconcile call")
		Eventually(func() error {
			cm := &corev1.ConfigMap{}
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: cmName}, cm)
		}).Should(Succeed())

		By("the NamespaceClass's status converging to Ready")
		Eventually(func(g Gomega) metav1.ConditionStatus {
			got := &namespaceclassv1alpha1.NamespaceClass{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: className}, got)).To(Succeed())
			cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			if cond == nil {
				return metav1.ConditionUnknown
			}
			return cond.Status
		}).Should(Equal(metav1.ConditionTrue))

		By("deleting the Namespace and the class, and the whole loop closing on its own")
		Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
		Expect(k8sClient.Delete(ctx, class)).To(Succeed())

		Eventually(func() bool {
			got := &namespaceclassv1alpha1.NamespaceClass{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: className}, got)
			return errors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("converges an existing Namespace after the class's resources are edited, without any explicit Reconcile call", func() {
		// Unlike the "editing the class to drop cm-a and add cm-b" unit test
		// in namespace_controller_test.go - which calls reconciler.Reconcile
		// directly - this goes through the real Watches(&NamespaceClass{})
		// wiring (mapClassToNamespaces), proving the "Updating classes"
		// requirement's automatic-requeue path actually fires, not just that
		// the resulting diff is computed correctly once reconciled.
		ctx := context.Background()
		const (
			className = "updated-class"
			nsName    = "updated-ns"
			cmAName   = "updated-cm-a"
			cmBName   = "updated-cm-b"
		)

		class := &namespaceclassv1alpha1.NamespaceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
			Spec: namespaceclassv1alpha1.NamespaceClassSpec{
				Resources: []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: updated-cm-a
data:
  foo: bar
`)},
			},
		}
		Expect(k8sClient.Create(ctx, class)).To(Succeed())

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nsName,
				Labels: map[string]string{common.NamespaceClassLabel: className},
			},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		By("cm-a getting created without a manual Reconcile call")
		Eventually(func() error {
			cm := &corev1.ConfigMap{}
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: cmAName}, cm)
		}).Should(Succeed())

		By("editing the class to drop cm-a and add cm-b")
		// Retried as a unit, not a single Get+Update: the live
		// NamespaceClassReconciler is concurrently writing this same
		// object's status in the background, so a resourceVersion picked up
		// by Get can easily be stale by the time Update is sent.
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: className}, class); err != nil {
				return err
			}
			class.Spec.Resources = []unstructured.Unstructured{toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: updated-cm-b
data:
  foo: baz
`)}
			return k8sClient.Update(ctx, class)
		}).Should(Succeed())

		By("the referencing Namespace picking up the edit on its own, via the NamespaceClass watch")
		Eventually(func() error {
			cm := &corev1.ConfigMap{}
			return k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: cmBName}, cm)
		}).Should(Succeed())

		Eventually(func() bool {
			cm := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: nsName, Name: cmAName}, cm)
			return errors.IsNotFound(err)
		}).Should(BeTrue())

		// Cleaned up here, synchronously, rather than via DeferCleanup: this
		// needs the manager still running to actually finish the cascade
		// delete, and AfterEach's mgrCancel() would otherwise race it.
		By("deleting the Namespace and the class, and the whole loop closing on its own")
		Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
		Expect(k8sClient.Delete(ctx, class)).To(Succeed())

		Eventually(func() bool {
			got := &namespaceclassv1alpha1.NamespaceClass{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: className}, got)
			return errors.IsNotFound(err)
		}).Should(BeTrue())
	})
})

func toUnstructured(data string) unstructured.Unstructured {
	var res unstructured.Unstructured
	Expect(yaml.Unmarshal([]byte(data), &res)).To(Succeed())
	return res
}
