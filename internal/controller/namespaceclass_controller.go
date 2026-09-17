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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
)

const namespaceClassFinalizer = "namespaceclass.akuity.io/finalizer"

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
		if controllerutil.AddFinalizer(nsClass, namespaceClassFinalizer) {
			return ctrl.Result{}, r.Update(ctx, nsClass)
		}
	} else {
		return r.reconcileDelete(ctx, nsClass)
	}

	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabels{namespaceClassLabel: nsClass.Name}); err != nil {
		log.Error(err, "Failed to list referencing Namespaces")
		return ctrl.Result{}, err
	}

	failing := 0
	for _, ns := range nsList.Items {
		if ns.GetAnnotations()[reconcileErrorAnnotation] != "" {
			failing++
		}
	}

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
	return ctrl.Result{}, nil
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

func (r *NamespaceClassReconciler) reconcileDelete(ctx context.Context, nsClass *namespaceclassv1alpha1.NamespaceClass) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(nsClass, namespaceClassFinalizer) {
		return ctrl.Result{}, nil
	}

	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabels{namespaceClassLabel: nsClass.Name}); err != nil {
		return ctrl.Result{}, err
	}

	for _, ns := range nsList.Items {
		applied, err := readAppliedResources(&ns)
		if err != nil || len(applied) > 0 {
			// Still converging (or annotation unreadable — be conservative
			// and wait rather than remove the finalizer prematurely).
			return ctrl.Result{}, nil
		}
	}

	controllerutil.RemoveFinalizer(nsClass, namespaceClassFinalizer)
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
	className, ok := obj.GetLabels()[namespaceClassLabel]
	if !ok {
		return nil
	}

	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: className}}}
}
