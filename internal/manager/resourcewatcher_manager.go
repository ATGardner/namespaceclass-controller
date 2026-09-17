package manager

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
)

type (
	resourceWatcherManager struct {
		cache      cache.Cache
		client     client.Client
		controller controller.TypedController[reconcile.Request]
		watchers   sets.Set[schema.GroupVersionKind]
	}

	ResourceWatcherManager interface {
		MaintainResourceWatchers(ctx context.Context) error
	}
)

func New(c cache.Cache, clnt client.Client, ctrlr controller.TypedController[reconcile.Request]) ResourceWatcherManager {
	return &resourceWatcherManager{
		cache:      c,
		client:     clnt,
		controller: ctrlr,
		watchers:   sets.New[schema.GroupVersionKind](),
	}
}

type noopManager struct{}

// NoOp returns a ResourceWatcherManager that does nothing. For tests that
// construct a NamespaceReconciler directly, without going through
// SetupWithManager — those exercise the apply/diff/delete logic in
// isolation and were never meant to exercise watcher lifecycle too.
func NoOp() ResourceWatcherManager {
	return noopManager{}
}

func (noopManager) MaintainResourceWatchers(context.Context) error {
	return nil
}

func (r *resourceWatcherManager) MaintainResourceWatchers(ctx context.Context) error {
	log := logf.FromContext(ctx)

	allGvks, err := r.getAllGVKs(ctx)
	if err != nil {
		return err
	}

	for gvk := range allGvks {
		if _, exists := r.watchers[gvk]; !exists {
			if err := r.addWatcher(gvk); err != nil {
				return fmt.Errorf("failed adding watcher for %s: %w", gvk, err)
			}

			r.watchers.Insert(gvk)
			log.Info("Added watcher", "gvk", gvk)
		}
	}

	for gvk := range r.watchers {
		if !allGvks.Has(gvk) {
			if err := r.removeWatcher(ctx, gvk); err != nil {
				return fmt.Errorf("failed removing watcher for %s: %w", gvk, err)
			}

			r.watchers.Delete(gvk)
			log.Info("Removed watcher", "gvk", gvk)
		}
	}

	log.Info("Done maintaining resource watchers", "#watchers", len(r.watchers))
	return nil
}

func (r *resourceWatcherManager) getAllGVKs(ctx context.Context) (sets.Set[schema.GroupVersionKind], error) {
	list := &namespaceclassv1alpha1.NamespaceClassList{}
	if err := r.client.List(ctx, list); err != nil {
		return nil, fmt.Errorf("failed getting NamespaceClassList: %w", err)
	}

	res := sets.New[schema.GroupVersionKind]()
	for _, nsClass := range list.Items {
		s := nsClass.GetGvks()
		res = res.Union(s)
	}

	return res, nil
}

func (r *resourceWatcherManager) addWatcher(gvk schema.GroupVersionKind) error {
	targetObj := &unstructured.Unstructured{}
	targetObj.SetGroupVersionKind(gvk)
	src := source.Kind[client.Object](
		r.cache,
		targetObj,
		handler.EnqueueRequestsFromMapFunc(mapNamespaceForRes),
		predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return true
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectOld.GetResourceVersion() != e.ObjectNew.GetResourceVersion()
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return true
			},
		},
	)
	return r.controller.Watch(src)
}

func (r *resourceWatcherManager) removeWatcher(ctx context.Context, gvk schema.GroupVersionKind) error {
	targetObj := &unstructured.Unstructured{}
	targetObj.SetGroupVersionKind(gvk)
	return r.cache.RemoveInformer(ctx, targetObj)
}

func mapNamespaceForRes(ctx context.Context, obj client.Object) []reconcile.Request {
	_, ok := obj.GetLabels()[common.ParentClassLabel]
	if !ok {
		return nil
	}

	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: obj.GetNamespace()}}}
}
