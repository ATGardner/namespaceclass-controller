package common

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func CheckNamespaceScoped(rm meta.RESTMapper, u *unstructured.Unstructured) error {
	gvk := u.GroupVersionKind()
	mapping, err := rm.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to get REST mapping for %s: %w", gvk, err)
	}

	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		return fmt.Errorf("resource %s is not namespaced", gvk)
	}

	return nil
}
