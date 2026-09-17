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
})

func toUnstructured(data string) unstructured.Unstructured {
	var res unstructured.Unstructured
	Expect(yaml.Unmarshal([]byte(data), &res)).To(Succeed())
	return res
}
