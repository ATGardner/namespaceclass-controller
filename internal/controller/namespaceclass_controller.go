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
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
)

// retiringGVKRequeueInterval is how often a class with a GVK dropped from its
// spec re-checks whether that GVK's last owned resource is gone.
const retiringGVKRequeueInterval = 30 * time.Second

// NamespaceClassReconciler reconciles a NamespaceClass object
type NamespaceClassReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=namespaceclass.akuity.io,resources=namespaceclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=namespaceclass.akuity.io,resources=namespaceclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=namespaceclass.akuity.io,resources=namespaceclasses/finalizers,verbs=update

func (r *NamespaceClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	nsClass := &namespaceclassv1alpha1.NamespaceClass{}
	if err := r.Get(ctx, req.NamespacedName, nsClass); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if nsClass.DeletionTimestamp.IsZero() {
		if controllerutil.AddFinalizer(nsClass, common.NamespaceClassFinalizer) {
			return ctrl.Result{}, r.Update(ctx, nsClass)
		}
	} else {
		return r.reconcileDelete(ctx, nsClass)
	}

	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabels{common.NamespaceClassLabel: nsClass.Name}); err != nil {
		log.Error(err, "Failed to list referencing Namespaces")
		return ctrl.Result{}, err
	}

	failing := 0
	for _, ns := range nsList.Items {
		if ns.GetAnnotations()[common.ReconcileErrorAnnotation] != "" {
			failing++
		}
	}

	appliedGVKs, retiring, err := r.computeAppliedGVKs(ctx, nsClass)
	if err != nil {
		log.Error(err, "Failed to compute applied GVKs")
		return ctrl.Result{}, err
	}

	nsClass.Status.AppliedGVKs = appliedGVKs
	nsClass.Status.FailingNamespaces = failing
	meta.SetStatusCondition(&nsClass.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             boolToConditionStatus(failing == 0),
		ObservedGeneration: nsClass.Generation,
		Reason:             readyReason(failing),
		Message:            readyMessage(failing, len(nsList.Items)),
	})

	if err := r.Status().Update(ctx, nsClass); err != nil {
		log.Error(err, "Failed to update NamespaceClass status")
		return ctrl.Result{}, err
	}

	log.Info("Done reconciling NamespaceClass status", "namespaces", len(nsList.Items), "failing", failing)

	// Nothing watches the retiring GVK's resources on this class's behalf,
	// so poll until Namespace reconciles have pruned the last of them.
	if retiring {
		return ctrl.Result{RequeueAfter: retiringGVKRequeueInterval}, nil
	}

	return ctrl.Result{}, nil
}

// computeAppliedGVKs returns the new status.appliedGVKs: the spec's GVKs,
// plus every GVK already in status that still has resources owned by
// nsClass. retiring reports whether any GVK was kept only for that reason.
func (r *NamespaceClassReconciler) computeAppliedGVKs(
	ctx context.Context,
	nsClass *namespaceclassv1alpha1.NamespaceClass,
) (applied []metav1.GroupVersionKind, retiring bool, err error) {
	gvks := nsClass.GetGvks()
	for _, gvk := range nsClass.Status.AppliedGVKs {
		gvk := schema.GroupVersionKind(gvk)
		if gvks.Has(gvk) {
			continue
		}

		owned, err := r.hasOwnedResources(ctx, nsClass, gvk)
		if err != nil {
			return nil, false, fmt.Errorf("failed checking for remaining %s resources: %w", gvk, err)
		}

		if owned {
			gvks.Insert(gvk)
			retiring = true
		}
	}

	applied = make([]metav1.GroupVersionKind, 0, gvks.Len())
	for gvk := range gvks {
		applied = append(applied, metav1.GroupVersionKind(gvk))
	}

	// Sorted so an unchanged set doesn't produce a status diff.
	slices.SortFunc(applied, func(a, b metav1.GroupVersionKind) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Version, b.Version), cmp.Compare(a.Kind, b.Kind))
	})

	return applied, retiring, nil
}

// hasOwnedResources reports whether any resource of the given GVK, in any
// namespace, is owned by nsClass.
func (r *NamespaceClassReconciler) hasOwnedResources(
	ctx context.Context,
	nsClass *namespaceclassv1alpha1.NamespaceClass,
	gvk schema.GroupVersionKind,
) (bool, error) {
	owned, err := r.listOwnedResources(ctx, nsClass, gvk)
	return len(owned) > 0, err
}

// listOwnedResources returns every resource of the given GVK, in any
// namespace, owned by nsClass.
func (r *NamespaceClassReconciler) listOwnedResources(
	ctx context.Context,
	nsClass *namespaceclassv1alpha1.NamespaceClass,
	gvk schema.GroupVersionKind,
) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err := r.List(ctx, list, client.MatchingLabels{common.ParentClassLabel: nsClass.Name}); err != nil {
		// The kind itself is gone (e.g. its CRD was deleted), so no
		// resources of it can remain.
		if meta.IsNoMatchError(err) {
			return nil, nil
		}

		return nil, err
	}

	return slices.DeleteFunc(list.Items, func(u unstructured.Unstructured) bool {
		return !common.IsOwnedByClass(&u, nsClass.Name)
	}), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&namespaceclassv1alpha1.NamespaceClass{}).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(mapNamespaceToClass),
		).
		Named("namespaceclass").
		Complete(r)
}

// reconcileDelete deletes every resource the class still owns, in any
// namespace, before letting the class go. Namespace reconciles would prune
// them too, but only for Namespaces that get reconciled; this doesn't wait
// on that.
func (r *NamespaceClassReconciler) reconcileDelete(ctx context.Context, nsClass *namespaceclassv1alpha1.NamespaceClass) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(nsClass, common.NamespaceClassFinalizer) {
		return ctrl.Result{}, nil
	}

	for gvk := range nsClass.GetWatchedGvks() {
		owned, err := r.listOwnedResources(ctx, nsClass, gvk)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed listing remaining %s resources: %w", gvk, err)
		}

		for i := range owned {
			if err := r.Delete(ctx, &owned[i]); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, fmt.Errorf("failed deleting %s %s/%s: %w", gvk.Kind, owned[i].GetNamespace(), owned[i].GetName(), err)
			}

			log.Info("Deleted resource of deleted class", "kind", gvk.Kind, "namespace", owned[i].GetNamespace(), "name", owned[i].GetName())
		}
	}

	controllerutil.RemoveFinalizer(nsClass, common.NamespaceClassFinalizer)
	return ctrl.Result{}, r.Update(ctx, nsClass)
}

// boolToConditionStatus converts a plain boolean into the tri-state
// metav1.ConditionStatus the Conditions field expects.
func boolToConditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// readyReason is the CamelCase machine-readable Reason paired with the
// Ready condition.
func readyReason(failing int) string {
	if failing == 0 {
		return "AllNamespacesApplied"
	}
	return "NamespacesFailing"
}

// readyMessage is the human-readable detail paired with the Ready condition.
func readyMessage(failing, total int) string {
	if failing == 0 {
		return fmt.Sprintf("All %d referencing namespace(s) have applied this class's resources", total)
	}
	return fmt.Sprintf("%d of %d referencing namespace(s) failed to apply this class's resources", failing, total)
}

func mapNamespaceToClass(ctx context.Context, obj client.Object) []reconcile.Request {
	className, ok := obj.GetLabels()[common.NamespaceClassLabel]
	if !ok {
		return nil
	}

	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: className}}}
}
