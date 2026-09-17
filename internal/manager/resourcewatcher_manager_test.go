package manager

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/util/workqueue"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
)

func TestManager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Manager Suite")
}

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	Expect(namespaceclassv1alpha1.AddToScheme(scheme)).To(Succeed())
	return scheme
}

func classWithResources(name string, resources ...unstructured.Unstructured) *namespaceclassv1alpha1.NamespaceClass {
	return &namespaceclassv1alpha1.NamespaceClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       namespaceclassv1alpha1.NamespaceClassSpec{Resources: resources},
	}
}

func configMap(name string) unstructured.Unstructured {
	u := unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind("ConfigMap")
	u.SetName(name)
	return u
}

func serviceAccount(name string) unstructured.Unstructured {
	u := unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind("ServiceAccount")
	u.SetName(name)
	return u
}

// stubController implements controller.TypedController[reconcile.Request] by
// embedding the (nil) interface, so only Watch needs a real body — anything
// else this suite doesn't expect to be called panics on its own rather than
// silently doing nothing.
type stubController struct {
	controller.TypedController[reconcile.Request]
	watchFn func(source.TypedSource[reconcile.Request]) error
}

func (s *stubController) Watch(src source.TypedSource[reconcile.Request]) error {
	return s.watchFn(src)
}

// stubCache implements cache.Cache the same way; only RemoveInformer matters
// for these tests.
type stubCache struct {
	cache.Cache
	removeInformerFn func(ctx context.Context, obj client.Object) error
}

func (s *stubCache) RemoveInformer(ctx context.Context, obj client.Object) error {
	return s.removeInformerFn(ctx, obj)
}

var _ = Describe("resourceWatcherManager", func() {
	var scheme *runtime.Scheme

	BeforeEach(func() {
		scheme = newScheme()
	})

	Describe("getAllGVKs", func() {
		It("unions GVKs across classes without duplicating shared ones", func() {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				classWithResources("a", configMap("cm-a")),
				classWithResources("b", configMap("cm-b"), serviceAccount("sa-b")),
			).Build()

			mgr := New(nil, c, nil).(*resourceWatcherManager)
			gvks, err := mgr.getAllGVKs(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(gvks).To(HaveLen(2))
			Expect(gvks.Has(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})).To(BeTrue())
			Expect(gvks.Has(schema.GroupVersionKind{Version: "v1", Kind: "ServiceAccount"})).To(BeTrue())
		})

		It("returns an empty set when there are no classes", func() {
			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			mgr := New(nil, c, nil).(*resourceWatcherManager)
			gvks, err := mgr.getAllGVKs(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(gvks).To(BeEmpty())
		})
	})

	Describe("MaintainResourceWatchers", func() {
		It("adds a watcher for a newly-referenced GVK", func() {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				classWithResources("a", configMap("cm-a")),
			).Build()

			var watchCalls int
			ctrl := &stubController{watchFn: func(source.TypedSource[reconcile.Request]) error {
				watchCalls++
				return nil
			}}

			mgr := New(nil, c, ctrl).(*resourceWatcherManager)
			Expect(mgr.MaintainResourceWatchers(context.Background())).To(Succeed())

			Expect(watchCalls).To(Equal(1))
			Expect(mgr.watchers.Has(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})).To(BeTrue())
		})

		It("removes a watcher once nothing references its GVK anymore", func() {
			c := fake.NewClientBuilder().WithScheme(scheme).Build()

			ctrl := &stubController{watchFn: func(source.TypedSource[reconcile.Request]) error { return nil }}
			var removed []schema.GroupVersionKind
			cch := &stubCache{removeInformerFn: func(_ context.Context, obj client.Object) error {
				removed = append(removed, obj.GetObjectKind().GroupVersionKind())
				return nil
			}}

			mgr := New(cch, c, ctrl).(*resourceWatcherManager)
			mgr.watchers.Insert(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})

			Expect(mgr.MaintainResourceWatchers(context.Background())).To(Succeed())

			Expect(removed).To(ConsistOf(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}))
			Expect(mgr.watchers).To(BeEmpty())
		})

		It("keeps a successfully-added GVK recorded even when a later one in the same pass fails", func() {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				classWithResources("a", configMap("cm-a"), serviceAccount("sa-a")),
			).Build()

			var seen int
			ctrl := &stubController{watchFn: func(source.TypedSource[reconcile.Request]) error {
				seen++
				if seen == 2 {
					return fmt.Errorf("boom")
				}
				return nil
			}}

			mgr := New(nil, c, ctrl).(*resourceWatcherManager)
			Expect(mgr.MaintainResourceWatchers(context.Background())).To(HaveOccurred())

			// Exactly one of the two GVKs succeeded before the failure — its
			// bookkeeping must have survived the early return. If it hadn't,
			// a retry would call Watch on it again, registering a second
			// event handler on the same already-running informer.
			Expect(mgr.watchers).To(HaveLen(1))

			// A retry should only attempt the GVK that's still missing.
			seen = 0
			ctrl.watchFn = func(source.TypedSource[reconcile.Request]) error {
				seen++
				return nil
			}
			Expect(mgr.MaintainResourceWatchers(context.Background())).To(Succeed())
			Expect(seen).To(Equal(1))
			Expect(mgr.watchers).To(HaveLen(2))
		})
	})

	Describe("mapNamespaceForRes, wired through EnqueueRequestsFromMapFunc", func() {
		// mapNamespaceForRes itself only ever sees one object, but its caller
		// (handler.EnqueueRequestsFromMapFunc) runs it twice per Update event
		// — once against ObjectOld, once against ObjectNew — and enqueues
		// whatever either call returns. So a label being removed still
		// produces a request, via the old object's call, even though the
		// function has no old-vs-new logic of its own. These tests exercise
		// that real dispatch path rather than calling mapNamespaceForRes
		// directly, since the guarantee lives in the caller, not the function.

		newRateLimitingQueue := func() workqueue.TypedRateLimitingInterface[reconcile.Request] {
			return workqueue.NewTypedRateLimitingQueue[reconcile.Request](
				workqueue.DefaultTypedControllerRateLimiter[reconcile.Request](),
			)
		}

		It("still enqueues a reconcile when an update removes the parent-class label", func() {
			oldObj := &unstructured.Unstructured{}
			oldObj.SetAPIVersion("v1")
			oldObj.SetKind("ConfigMap")
			oldObj.SetNamespace("web-portal")
			oldObj.SetName("cm")
			oldObj.SetLabels(map[string]string{common.ParentClassLabel: "public-network"})

			newObj := oldObj.DeepCopy()
			newObj.SetLabels(nil)

			q := newRateLimitingQueue()
			defer q.ShutDown()

			handler.EnqueueRequestsFromMapFunc(mapNamespaceForRes).Update(
				context.Background(),
				event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj},
				q,
			)

			Expect(q.Len()).To(Equal(1))
			item, _ := q.Get()
			Expect(item).To(Equal(reconcile.Request{NamespacedName: types.NamespacedName{Name: "web-portal"}}))
		})

		It("enqueues nothing when neither old nor new carries the label", func() {
			oldObj := &unstructured.Unstructured{}
			oldObj.SetAPIVersion("v1")
			oldObj.SetKind("ConfigMap")
			oldObj.SetNamespace("web-portal")
			oldObj.SetName("cm")

			newObj := oldObj.DeepCopy()

			q := newRateLimitingQueue()
			defer q.ShutDown()

			handler.EnqueueRequestsFromMapFunc(mapNamespaceForRes).Update(
				context.Background(),
				event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj},
				q,
			)

			Expect(q.Len()).To(Equal(0))
		})

		It("enqueues exactly one request, not two, when both old and new carry the label", func() {
			oldObj := &unstructured.Unstructured{}
			oldObj.SetAPIVersion("v1")
			oldObj.SetKind("ConfigMap")
			oldObj.SetNamespace("web-portal")
			oldObj.SetName("cm")
			oldObj.SetLabels(map[string]string{common.ParentClassLabel: "public-network"})

			newObj := oldObj.DeepCopy()

			q := newRateLimitingQueue()
			defer q.ShutDown()

			handler.EnqueueRequestsFromMapFunc(mapNamespaceForRes).Update(
				context.Background(),
				event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj},
				q,
			)

			// Both calls map to the identical request; the workqueue
			// dedupes it down to one pending item.
			Expect(q.Len()).To(Equal(1))
		})
	})
})
