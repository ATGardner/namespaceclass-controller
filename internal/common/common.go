package common

import (
	"bytes"
	"fmt"
	"text/template"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
)

// namespaceClassLabel marks a Namespace as managed by a NamespaceClass.
const NamespaceClassLabel = "namespaceclass.akuity.io/name"

// parentClassLabel marks each resource with the NamespaceClass that createad it.
const ParentClassLabel = "namespaceclass.akuity.io/parent"

// fieldOwner identifies this controller as the Server-Side Apply field
// manager for resources templated from a NamespaceClass.
const FieldOwner = "namespaceclass-controller"

// appliedResourcesAnnotation records, on the Namespace, the GVK+name of every
// resource this controller applied on its last successful reconcile. It's
// the source of truth for the diff — not a label selector — because a
// resource can be dropped from a class's spec (or the namespace can switch
// classes) without leaving any current signal behind to find it by.
const AppliedResourcesAnnotation = "namespaceclass.akuity.io/applied-resources"

const ReconcileErrorAnnotation = "namespaceclass.akuity.io/reconcile-error"

const NamespaceClassFinalizer = "namespaceclass.akuity.io/finalizer"

type (
	ResourceBuilder interface {
		BuildFinalResource(
			orig *unstructured.Unstructured,
			namespace string,
			nsClass *namespaceclassv1alpha1.NamespaceClass,
		) (*unstructured.Unstructured, error)
	}

	resourceBuilder struct {
		rm     meta.RESTMapper
		scheme *runtime.Scheme
	}
)

func NewResourceBuilder(clnt client.Client) ResourceBuilder {
	return &resourceBuilder{
		rm:     clnt.RESTMapper(),
		scheme: clnt.Scheme(),
	}
}

func (r *resourceBuilder) BuildFinalResource(
	orig *unstructured.Unstructured,
	namespace string,
	nsClass *namespaceclassv1alpha1.NamespaceClass,
) (*unstructured.Unstructured, error) {
	err := CheckNamespaceScoped(r.rm, orig)
	if err != nil {
		return nil, err
	}

	res, err := templateResourceNamespace(orig, namespace)
	if err != nil {
		return nil, err
	}

	// override any accidental "namespace" field that exist in the NamespaceClass spec
	res.SetNamespace(namespace)

	resLabels := res.GetLabels()
	if resLabels == nil {
		resLabels = map[string]string{}
	}

	resLabels[ParentClassLabel] = nsClass.Name
	res.SetLabels(resLabels)

	if err := controllerutil.SetControllerReference(nsClass, res, r.scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	return res, nil
}

func templateResourceNamespace(u *unstructured.Unstructured, namespace string) (*unstructured.Unstructured, error) {
	data, err := yaml.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("failed marshaling resource %s: %w", u.GetName(), err)
	}

	t, err := template.New("tmpl").Option("missingkey=error").Parse(string(data))
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
