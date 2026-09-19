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

package v1alpha1

import (
	"context"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
	"github.com/atgardner/namespaceclass-controller/internal/common"
)

// nolint:unused
// log is for logging in this package.
var namespaceclasslog = logf.Log.WithName("namespaceclass-resource")

// SetupNamespaceClassWebhookWithManager registers the webhook for NamespaceClass in the manager.
func SetupNamespaceClassWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &namespaceclassv1alpha1.NamespaceClass{}).
		WithValidator(&NamespaceClassValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-namespaceclass-akuity-io-v1alpha1-namespaceclass,mutating=false,failurePolicy=fail,sideEffects=None,groups=namespaceclass.akuity.io,resources=namespaceclasses,verbs=create;update,versions=v1alpha1,name=vnamespaceclass-v1alpha1.kb.io,admissionReviewVersions=v1

// NamespaceClassValidator struct is responsible for validating the NamespaceClass resource
// when it is created, updated, or deleted.
type NamespaceClassValidator struct {
	client.Client
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassValidator) ValidateCreate(_ context.Context, obj *namespaceclassv1alpha1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon creation", "name", obj.GetName())

	return nil, v.validateNamespaceClass(obj)
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassValidator) ValidateUpdate(_ context.Context, oldObj, newObj *namespaceclassv1alpha1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon update", "name", newObj.GetName())

	return nil, v.validateNamespaceClass(newObj)
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassValidator) ValidateDelete(_ context.Context, obj *namespaceclassv1alpha1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon deletion", "name", obj.GetName())

	return nil, nil
}

func (v *NamespaceClassValidator) validateNamespaceClass(obj *namespaceclassv1alpha1.NamespaceClass) error {
	var errs field.ErrorList

	for i, resource := range obj.Spec.Resources {
		if err := common.CheckNamespaceScoped(v.RESTMapper(), &resource); err != nil {
			errs = append(errs, field.Invalid(
				field.NewPath("spec").Child("resources").Index(i),
				resource.GroupVersionKind(),
				err.Error(),
			))
		}
	}

	if len(errs) == 0 {
		return nil
	}

	err := errs.ToAggregate()
	namespaceclasslog.Error(err, "Rejected NamespaceClass", "name", obj.GetName())
	return err
}
