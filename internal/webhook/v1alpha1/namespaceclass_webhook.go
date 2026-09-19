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

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	namespaceclassv1alpha1 "github.com/atgardner/namespaceclass-controller/api/v1alpha1"
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

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-namespaceclass-akuity-io-v1alpha1-namespaceclass,mutating=false,failurePolicy=fail,sideEffects=None,groups=namespaceclass.akuity.io,resources=namespaceclasses,verbs=create;update,versions=v1alpha1,name=vnamespaceclass-v1alpha1.kb.io,admissionReviewVersions=v1

// NamespaceClassValidator struct is responsible for validating the NamespaceClass resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type NamespaceClassValidator struct {
	// TODO(user): Add more fields as needed for validation
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassValidator) ValidateCreate(_ context.Context, obj *namespaceclassv1alpha1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon creation", "name", obj.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, nil
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassValidator) ValidateUpdate(_ context.Context, oldObj, newObj *namespaceclassv1alpha1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon update", "name", newObj.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type NamespaceClass.
func (v *NamespaceClassValidator) ValidateDelete(_ context.Context, obj *namespaceclassv1alpha1.NamespaceClass) (admission.Warnings, error) {
	namespaceclasslog.Info("Validation for NamespaceClass upon deletion", "name", obj.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}
