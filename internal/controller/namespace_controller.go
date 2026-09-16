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
	"slices"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// namespaceClassLabel marks a Namespace as managed by a NamespaceClass.
const namespaceClassLabel = "namespaceclass.akuity.io/name"
const parentClassLabel = "namespaceclass.akuity.io/parent"

// fieldOwner identifies this controller as the Server-Side Apply field
// manager for resources templated from a NamespaceClass.
const fieldOwner = "namespaceclass-controller"

// appliedResourcesAnnotation records, on the Namespace, the GVK+name of every
// resource this controller applied on its last successful reconcile. It's
// the source of truth for the diff — not a label selector — because a
// resource can be dropped from a class's spec (or the namespace can switch
// classes) without leaving any current signal behind to find it by.
const appliedResourcesAnnotation = "namespaceclass.akuity.io/applied-resources"

// appliedResource identifies one resource this controller manages in a
// namespace. Group+Version+Kind+Name is the identity: it's already present
// wherever the resource is defined, so no separate tracking key is needed.
type appliedResource struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
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

// readAppliedResources returns the resources recorded from the last
// successful reconcile, or nil if none are recorded yet.
func readAppliedResources(ns *corev1.Namespace) ([]appliedResource, error) {
	raw, ok := ns.GetAnnotations()[appliedResourcesAnnotation]
	if !ok || raw == "" {
		return nil, nil
	}

	var resources []appliedResource
	if err := json.Unmarshal([]byte(raw), &resources); err != nil {
		return nil, err
	}

	return resources, nil
}

// setAppliedResources records the given resources as the current applied set
// on the Namespace.
func setAppliedResources(ns *corev1.Namespace, resources []appliedResource) error {
	raw, err := json.Marshal(resources)
	if err != nil {
		return err
	}

	annotations := ns.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	annotations[appliedResourcesAnnotation] = string(raw)
	ns.SetAnnotations(annotations)

	return nil
}

// NamespaceReconciler reconciles a Namespace object
type NamespaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=*,resources=*,verbs=*

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var namespace corev1.Namespace
	if err := r.Get(ctx, req.NamespacedName, &namespace); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Namespace resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}

		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Namespace")
		return ctrl.Result{}, err
	}

	nsClass, err := r.getNamespaceClass(ctx, namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("NamespaceClass resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}

		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get NamespaceClass")
		return ctrl.Result{}, err
	}
	if nsClass == nil {
		// No class label (see getNamespaceClass) - nothing to reconcile.
		return ctrl.Result{}, nil
	}

	log = log.WithValues("nsClass", nsClass.Name)
	ctx = logf.IntoContext(ctx, log)

	desired := make([]appliedResource, len(nsClass.Spec.Resources))
	for i := range nsClass.Spec.Resources {
		// Deep-copy so mutations below don't alter the NamespaceClass's own
		// in-memory spec, and so Server-Side Apply always sends the full
		// desired content rather than whatever a prior Get left behind.
		res := nsClass.Spec.Resources[i].DeepCopy()
		// override any accidental "namespace" field that exist in the NamespaceClass spec
		res.SetNamespace(namespace.Name)

		resLabels := res.GetLabels()
		if resLabels == nil {
			resLabels = map[string]string{}
		}
		resLabels[parentClassLabel] = nsClass.Name
		res.SetLabels(resLabels)

		if err := controllerutil.SetControllerReference(nsClass, res, r.Scheme); err != nil {
			log.Error(err, "Failed to set controller reference", "kind", res.GroupVersionKind().Kind, "name", res.GetName())
			return ctrl.Result{}, err
		}

		if err := r.Patch(ctx, res, client.Apply, client.ForceOwnership, client.FieldOwner(fieldOwner)); err != nil {
			log.Error(err, "Failed to apply resource", "kind", res.GroupVersionKind().Kind, "name", res.GetName())
			return ctrl.Result{}, err
		}

		desired[i] = toAppliedResource(res)
	}

	applied, err := readAppliedResources(&namespace)
	if err != nil {
		// Unreadable record: treat as empty rather than fail reconciliation.
		// The write below repairs it, and any orphan this misses is caught
		// once its identity resurfaces in a future applied set.
		log.Error(err, "Failed to parse applied-resources annotation, resetting it")
		applied = nil
	}

	for _, res := range applied {
		if slices.Contains(desired, res) {
			continue
		}

		if err := r.deleteOrphanResource(ctx, res, namespace.Name); err != nil {
			log.Error(err, "Failed to delete stale resource", "kind", res.Kind, "name", res.Name)
			return ctrl.Result{}, err
		}

		log.Info("Deleted stale resource", "kind", res.Kind, "name", res.Name)
	}

	if !slices.Equal(applied, desired) {
		patch := client.MergeFrom(namespace.DeepCopy())
		if err := setAppliedResources(&namespace, desired); err != nil {
			log.Error(err, "Failed to encode applied-resources annotation")
			return ctrl.Result{}, err
		}

		if err := r.Patch(ctx, &namespace, patch); err != nil {
			log.Error(err, "Failed to update applied-resources annotation")
			return ctrl.Result{}, err
		}
	}

	log.Info("Done reconciling Namespace", "resources", desired)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			_, ok := obj.GetLabels()[namespaceClassLabel]
			return ok
		}))).
		Watches(
			&namespaceclassv1alpha1.NamespaceClass{},
			handler.EnqueueRequestsFromMapFunc(r.mapClassToNamespaces),
		).
		Named("namespace").
		Complete(r)
}

func (r *NamespaceReconciler) getNamespaceClass(ctx context.Context, ns corev1.Namespace) (*namespaceclassv1alpha1.NamespaceClass, error) {
	log := logf.FromContext(ctx)

	nsClassName, ok := ns.GetLabels()[namespaceClassLabel]
	if !ok {
		log.Info("Namespace does not have class label")
		return nil, nil
	}

	var nsClass namespaceclassv1alpha1.NamespaceClass
	return &nsClass, r.Get(ctx, client.ObjectKey{
		Name: nsClassName,
	}, &nsClass)
}

func (r *NamespaceReconciler) mapClassToNamespaces(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	class := obj.(*namespaceclassv1alpha1.NamespaceClass)
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabels{namespaceClassLabel: class.Name}); err != nil {
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
	if err := r.Delete(ctx, u); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}
