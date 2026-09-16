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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"text/template"

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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
)

// namespaceClassLabel marks a Namespace as managed by a NamespaceClass.
const namespaceClassLabel = "namespaceclass.akuity.io/name"

// parentClassLabel marks each resource with the NamespaceClass that createad it.
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

const reconcileErrorAnnotation = "namespaceclass.akuity.io/reconcile-error"

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

func templateResourceNamespace(u *unstructured.Unstructured, namespace string) (*unstructured.Unstructured, error) {
	data, err := yaml.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("failed marshaling resource %s: %w", u.GetName(), err)
	}

	t, err := template.New("tmpl").Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("failed creating template for resource %s: %w", u.GetName(), err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]string{
		"namespace": namespace,
	}); err != nil {
		return nil, fmt.Errorf("failed executing template for resource %s: %w", u.GetName(), err)
	}

	res := &unstructured.Unstructured{}
	err = yaml.Unmarshal(buf.Bytes(), res)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal generated YAML for resource %s: %w", u.GetName(), err)
	}

	return res, nil
}

// NamespaceReconciler reconciles a Namespace object
type NamespaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=*,resources=*,verbs=*

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, reconcileErr error) {
	log := logf.FromContext(ctx)

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

	var desired []appliedResource
	if nsClass != nil && nsClass.DeletionTimestamp.IsZero() {
		desired = make([]appliedResource, len(nsClass.Spec.Resources))
		for i := range nsClass.Spec.Resources {
			orig := nsClass.Spec.Resources[i]
			res, err := r.getFinalResource(&orig, namespace.Name, nsClass)
			if err != nil {
				log.Error(err, "Failed to get final resource to apply", "kind", orig.GroupVersionKind().Kind, "name", orig.GetName())
				return ctrl.Result{}, fmt.Errorf("failed to get final resource %s/%s: %w", orig.GroupVersionKind().Kind, orig.GetName(), err)
			}

			if err := r.Patch(ctx, res, client.Apply, client.ForceOwnership, client.FieldOwner(fieldOwner)); err != nil {
				log.Error(err, "Failed to apply resource", "kind", res.GroupVersionKind().Kind, "name", res.GetName())
				return ctrl.Result{}, fmt.Errorf("failed to apply resource %s/%s: %w", res.GroupVersionKind().Kind, res.GetName(), err)
			}

			desired[i] = toAppliedResource(res)
		}
	}

	applied, err := readAppliedResources(namespace)
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
			return ctrl.Result{}, fmt.Errorf("failed to delete stale resource %s/%s: %w", res.Kind, res.Name, err)
		}

		log.Info("Deleted stale resource", "kind", res.Kind, "name", res.Name)
	}

	if !slices.Equal(applied, desired) {
		patch := client.MergeFrom(namespace.DeepCopy())
		if err := setAppliedResources(namespace, desired); err != nil {
			log.Error(err, "Failed to encode applied-resources annotation")
			return ctrl.Result{}, fmt.Errorf("failed to encode applied-resources annotation: %w", err)
		}

		if err := r.Patch(ctx, namespace, patch); err != nil {
			log.Error(err, "Failed to update applied-resources annotation")
			return ctrl.Result{}, fmt.Errorf("failed to update applied-resources annotation: %w", err)
		}
	}

	log.Info("Done reconciling Namespace", "resources", desired)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				_, ok := e.Object.GetLabels()[namespaceClassLabel]
				return ok
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				_, oldOk := e.ObjectOld.GetLabels()[namespaceClassLabel]
				_, newOk := e.ObjectNew.GetLabels()[namespaceClassLabel]
				return oldOk || newOk
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				_, ok := e.Object.GetLabels()[namespaceClassLabel]
				return ok
			},
		})).
		Watches(
			&namespaceclassv1alpha1.NamespaceClass{},
			handler.EnqueueRequestsFromMapFunc(r.mapClassToNamespaces),
		).
		Named("namespace").
		Complete(r)
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

	if ns.GetAnnotations()[reconcileErrorAnnotation] == desired {
		return nil
	}

	patch := client.MergeFrom(ns.DeepCopy())
	annotations := ns.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	if desired == "" {
		delete(annotations, reconcileErrorAnnotation)
	} else {
		annotations[reconcileErrorAnnotation] = desired
	}

	ns.SetAnnotations(annotations)

	return r.Patch(ctx, ns, patch)
}

func (r *NamespaceReconciler) getNamespaceClass(ctx context.Context, ns *corev1.Namespace) (*namespaceclassv1alpha1.NamespaceClass, error) {
	nsClassName, ok := ns.GetLabels()[namespaceClassLabel]
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

func (r *NamespaceReconciler) getFinalResource(u *unstructured.Unstructured, namespace string, nsClass *namespaceclassv1alpha1.NamespaceClass) (*unstructured.Unstructured, error) {
	res, err := templateResourceNamespace(u, namespace)
	if err != nil {
		return nil, err
	}

	// override any accidental "namespace" field that exist in the NamespaceClass spec
	res.SetNamespace(namespace)

	resLabels := res.GetLabels()
	if resLabels == nil {
		resLabels = map[string]string{}
	}

	resLabels[parentClassLabel] = nsClass.Name
	res.SetLabels(resLabels)

	if err := controllerutil.SetControllerReference(nsClass, res, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	return res, nil
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
	err := r.Delete(ctx, u)
	return client.IgnoreNotFound(err)
}
