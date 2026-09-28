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
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
	"github.com/atgardner/namespaceclass-controller/internal/manager"
)

// appliedResource identifies one resource this controller manages in a
// namespace, with the version needed to address it. It's also the entry
// format of the legacy applied-resources annotation.
type appliedResource struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
}

// resourceKey identifies a resource within a namespace regardless of API
// version: the same object is served under every version of its kind, and
// must not be mistaken for a stale copy of itself.
type resourceKey struct {
	schema.GroupKind
	Name string
}

func (a appliedResource) key() resourceKey {
	return resourceKey{GroupKind: schema.GroupKind{Group: a.Group, Kind: a.Kind}, Name: a.Name}
}

// NamespaceReconciler reconciles a Namespace object
type NamespaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	manager manager.ResourceWatcherManager
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=*,resources=*,verbs=*

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, reconcileErr error) {
	log := logf.FromContext(ctx)

	if err := r.manager.MaintainResourceWatchers(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed maintaining resource matchers: %w", err)
	}

	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, namespace); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	nsClass, err := r.getNamespaceClass(ctx, namespace)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if nsClass != nil {
		log = log.WithValues("nsClass", nsClass.Name)
		ctx = logf.IntoContext(ctx, log)
	}

	// Record this reconcile's outcome on the Namespace so the NamespaceClass
	// aggregator (which only lists Namespaces, never re-runs this diff logic
	// itself) can see it. Covers every return below this point; the early
	// returns above intentionally don't have a Namespace/NamespaceClass pair
	// worth reporting on yet.
	defer func() {
		if err := r.setReconcileError(ctx, namespace, reconcileErr); err != nil {
			log.Error(err, "Failed to record reconcile-error annotation")
		}
	}()

	desired := sets.New[resourceKey]()
	rb := common.NewResourceBuilder(r.Client)
	if nsClass != nil && nsClass.DeletionTimestamp.IsZero() {
		var errs field.ErrorList

		resolved := make([]*unstructured.Unstructured, len(nsClass.Spec.Resources))
		for i, orig := range nsClass.Spec.Resources {
			res, err := rb.BuildFinalResource(&orig, namespace.Name, nsClass) // scope check lives inside this
			if err != nil {
				log.Error(err, "Failed to get final resource to apply", "kind", orig.GroupVersionKind().Kind, "name", orig.GetName())
				errs = append(errs, field.Invalid(
					field.NewPath("spec").Child("resources").Index(i),
					orig.GroupVersionKind().String(),
					err.Error(),
				))
				continue
			}

			resolved[i] = res
		}

		if len(errs) > 0 {
			return ctrl.Result{}, fmt.Errorf("failed to resolve NamespaceClass resources: %w", errs.ToAggregate())
		}

		if err := r.recordAppliedGVKs(ctx, nsClass); err != nil {
			log.Error(err, "Failed to record applied GVKs on the NamespaceClass")
			return ctrl.Result{}, fmt.Errorf("failed to record applied GVKs on NamespaceClass %s: %w", nsClass.Name, err)
		}

		for _, res := range resolved {
			//nolint:staticcheck // client.Apply is deprecated in favor of client.Client.Apply(), which requires typed apply configurations we don't have for arbitrary unstructured resources
			if err := r.Patch(ctx, res, client.Apply, client.ForceOwnership, client.FieldOwner(common.FieldOwner)); err != nil {
				log.Error(err, "Failed to apply resource", "kind", res.GroupVersionKind().Kind, "name", res.GetName())
				return ctrl.Result{}, fmt.Errorf("failed to apply resource %s/%s: %w", res.GroupVersionKind().Kind, res.GetName(), err)
			}

			desired.Insert(toAppliedResource(res).key())
		}
	}

	stale, err := r.findStaleResources(ctx, namespace, desired)
	if err != nil {
		log.Error(err, "Failed to find stale resources")
		return ctrl.Result{}, fmt.Errorf("failed to find stale resources: %w", err)
	}

	for _, res := range stale {
		if err := r.deleteOrphanResource(ctx, res, namespace.Name); err != nil {
			log.Error(err, "Failed to delete stale resource", "kind", res.Kind, "name", res.Name)
			return ctrl.Result{}, fmt.Errorf("failed to delete stale resource %s/%s: %w", res.Kind, res.Name, err)
		}

		log.Info("Deleted stale resource", "kind", res.Kind, "name", res.Name)
	}

	// Only once every resource it lists is gone, so a failed delete above
	// leaves the annotation in place for the retry.
	if err := r.removeLegacyAppliedResources(ctx, namespace); err != nil {
		log.Error(err, "Failed to remove legacy applied-resources annotation")
		return ctrl.Result{}, fmt.Errorf("failed to remove legacy applied-resources annotation: %w", err)
	}

	log.Info("Done reconciling Namespace", "resources", desired.Len())
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	controller, err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				_, ok := e.Object.GetLabels()[common.NamespaceClassLabel]
				return ok
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				_, oldOk := e.ObjectOld.GetLabels()[common.NamespaceClassLabel]
				_, newOk := e.ObjectNew.GetLabels()[common.NamespaceClassLabel]
				return oldOk || newOk
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				_, ok := e.Object.GetLabels()[common.NamespaceClassLabel]
				return ok
			},
		})).
		Watches(
			&namespaceclassv1alpha1.NamespaceClass{},
			handler.EnqueueRequestsFromMapFunc(r.mapClassToNamespaces),
		).
		Named("namespace").
		Build(r)
	r.manager = manager.New(mgr.GetCache(), r.Client, controller)
	return err
}

// setReconcileError records reconcileErr's message (or clears the
// annotation, on nil) as this Namespace's last reconcile outcome. Only
// patches when the recorded value actually changes, so a healthy namespace
// doesn't take a write on every reconcile.
func (r *NamespaceReconciler) setReconcileError(ctx context.Context, ns *corev1.Namespace, reconcileErr error) error {
	desired := ""
	if reconcileErr != nil {
		desired = reconcileErr.Error()
	}

	if ns.GetAnnotations()[common.ReconcileErrorAnnotation] == desired {
		return nil
	}

	patch := client.MergeFrom(ns.DeepCopy())
	annotations := ns.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	if desired == "" {
		delete(annotations, common.ReconcileErrorAnnotation)
	} else {
		annotations[common.ReconcileErrorAnnotation] = desired
	}

	ns.SetAnnotations(annotations)

	return r.Patch(ctx, ns, patch)
}

func (r *NamespaceReconciler) getNamespaceClass(ctx context.Context, ns *corev1.Namespace) (*namespaceclassv1alpha1.NamespaceClass, error) {
	nsClassName, ok := ns.GetLabels()[common.NamespaceClassLabel]
	if !ok {
		logf.FromContext(ctx).Info("Namespace does not have class label")
		return nil, nil
	}

	nsClass := &namespaceclassv1alpha1.NamespaceClass{}
	if err := r.Get(ctx, client.ObjectKey{Name: nsClassName}, nsClass); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	return nsClass, nil
}

func (r *NamespaceReconciler) mapClassToNamespaces(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	class := obj.(*namespaceclassv1alpha1.NamespaceClass)
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabels{common.NamespaceClassLabel: class.Name}); err != nil {
		return nil
	}

	reqs := make([]ctrl.Request, len(nsList.Items))
	for i, ns := range nsList.Items {
		reqs[i] = ctrl.Request{NamespacedName: types.NamespacedName{Name: ns.Name}}
	}

	log.Info("Generating requests for Namespaces", "count", len(nsList.Items))
	return reqs
}

func (r *NamespaceReconciler) deleteOrphanResource(ctx context.Context, res appliedResource, namespace string) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: res.Group, Version: res.Version, Kind: res.Kind})
	u.SetName(res.Name)
	u.SetNamespace(namespace)
	err := r.Delete(ctx, u)
	return client.IgnoreNotFound(err)
}

// configMapAppliedResource is the appliedResource identity for a ConfigMap
// with the given name.
func toAppliedResource(u *unstructured.Unstructured) appliedResource {
	return appliedResource{
		Group:   u.GroupVersionKind().Group,
		Version: u.GroupVersionKind().Version,
		Kind:    u.GetKind(),
		Name:    u.GetName(),
	}
}

// recordAppliedGVKs adds any of nsClass's spec GVKs missing from its
// status.appliedGVKs, before any resource of them is applied. Otherwise a
// GVK dropped from the spec before the NamespaceClass controller recorded it
// would never be watched, and its resources never pruned.
func (r *NamespaceReconciler) recordAppliedGVKs(ctx context.Context, nsClass *namespaceclassv1alpha1.NamespaceClass) error {
	recorded := sets.New[schema.GroupVersionKind]()
	for _, gvk := range nsClass.Status.AppliedGVKs {
		recorded.Insert(schema.GroupVersionKind(gvk))
	}

	missing := nsClass.GetGvks().Difference(recorded)
	if missing.Len() == 0 {
		return nil
	}

	// Optimistic lock: the NamespaceClass controller writes this list too,
	// and a blind overwrite could drop a GVK it's still retiring.
	patch := client.MergeFromWithOptions(nsClass.DeepCopy(), client.MergeFromWithOptimisticLock{})
	for gvk := range missing {
		nsClass.Status.AppliedGVKs = append(nsClass.Status.AppliedGVKs, metav1.GroupVersionKind(gvk))
	}

	return r.Status().Patch(ctx, nsClass, patch)
}

// findStaleResources returns every resource in ns that some NamespaceClass
// applied and that isn't in desired. It sweeps every watched GVK for
// resources carrying the parent label and owned by a NamespaceClass, plus
// (for now) the entries of the legacy applied-resources annotation.
func (r *NamespaceReconciler) findStaleResources(
	ctx context.Context,
	ns *corev1.Namespace,
	desired sets.Set[resourceKey],
) (map[resourceKey]appliedResource, error) {
	gvks, err := manager.WatchedGVKs(ctx, r.Client)
	if err != nil {
		return nil, err
	}

	stale := map[resourceKey]appliedResource{}
	for gvk := range gvks {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := r.List(ctx, list, client.InNamespace(ns.Name), client.HasLabels{common.ParentClassLabel}); err != nil {
			// The kind itself is gone (e.g. its CRD was deleted), so no
			// resources of it can remain.
			if meta.IsNoMatchError(err) {
				continue
			}

			return nil, fmt.Errorf("failed listing %s resources: %w", gvk, err)
		}

		for i := range list.Items {
			u := &list.Items[i]
			if !common.IsOwnedByClass(u, u.GetLabels()[common.ParentClassLabel]) {
				continue
			}

			res := appliedResource{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind, Name: u.GetName()}
			if !desired.Has(res.key()) {
				stale[res.key()] = res
			}
		}
	}

	// TODO(#4): remove once no Namespace carries the annotation any more,
	// in the release after the one that stopped writing it. Until then it
	// still covers resources whose GVK no class watches any more.
	legacy, err := readAppliedResources(ns)
	if err != nil {
		// Unreadable record: skip it. The sweep above still covers every
		// resource of a watched GVK, and the annotation is removed after.
		logf.FromContext(ctx).Error(err, "Failed to parse legacy applied-resources annotation, ignoring it")
	}

	for _, res := range legacy {
		if !desired.Has(res.key()) {
			stale[res.key()] = res
		}
	}

	return stale, nil
}

// removeLegacyAppliedResources deletes the applied-resources annotation that
// earlier versions of the controller wrote.
//
// TODO(#4): remove together with readAppliedResources.
func (r *NamespaceReconciler) removeLegacyAppliedResources(ctx context.Context, ns *corev1.Namespace) error {
	if _, ok := ns.GetAnnotations()[common.AppliedResourcesAnnotation]; !ok {
		return nil
	}

	patch := client.MergeFrom(ns.DeepCopy())
	annotations := ns.GetAnnotations()
	delete(annotations, common.AppliedResourcesAnnotation)
	ns.SetAnnotations(annotations)

	return r.Patch(ctx, ns, patch)
}

// readAppliedResources returns the resources recorded in the legacy
// applied-resources annotation, or nil if there is none.
//
// TODO(#4): remove together with removeLegacyAppliedResources.
func readAppliedResources(ns *corev1.Namespace) ([]appliedResource, error) {
	raw, ok := ns.GetAnnotations()[common.AppliedResourcesAnnotation]
	if !ok || raw == "" {
		return nil, nil
	}

	var resources []appliedResource
	if err := json.Unmarshal([]byte(raw), &resources); err != nil {
		return nil, err
	}

	return resources, nil
}
