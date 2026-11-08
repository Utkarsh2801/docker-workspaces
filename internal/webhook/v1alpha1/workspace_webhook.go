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
	"fmt"
	"strings"

	"github.com/distribution/reference"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	workspacesv1alpha1 "github.com/utkarsh2801/docker-workspaces/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var workspacelog = logf.Log.WithName("workspace-resource")

var allowedImageRepositories = map[string]struct{}{
	"docker.io/traefik/whoami":       {},
	"docker.io/codercom/code-server": {},
}

const (
	defaultWorkspaceImage = "traefik/whoami"
	OwnerLabel            = "workspaces.example.com/owner"
	maxLabelValueLength   = 63
)

// SetupWorkspaceWebhookWithManager registers the webhook for Workspace in the manager.
func SetupWorkspaceWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &workspacesv1alpha1.Workspace{}).
		WithValidator(&WorkspaceValidator{}).
		WithDefaulter(&WorkspaceDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-workspaces-example-com-v1alpha1-workspace,mutating=true,failurePolicy=fail,sideEffects=None,groups=workspaces.example.com,resources=workspaces,verbs=create;update,versions=v1alpha1,name=mworkspace-v1alpha1.kb.io,admissionReviewVersions=v1

// WorkspaceDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Workspace when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type WorkspaceDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

// Default implements admission.Defaulter so a webhook will be registered for the Kind Workspace.
func (d *WorkspaceDefaulter) Default(_ context.Context, obj *workspacesv1alpha1.Workspace) error {
	workspacelog.Info("Defaulting for Workspace", "name", obj.GetName())

	if obj.Spec.Image == "" {
		obj.Spec.Image = defaultWorkspaceImage
	}
	if obj.Labels == nil {
		obj.Labels = make(map[string]string)
	}
	obj.Labels[OwnerLabel] = ownerLabelValue(obj.Spec.Owner)

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-workspaces-example-com-v1alpha1-workspace,mutating=false,failurePolicy=fail,sideEffects=None,groups=workspaces.example.com,resources=workspaces,verbs=create;update,versions=v1alpha1,name=vworkspace-v1alpha1.kb.io,admissionReviewVersions=v1

// WorkspaceValidator struct is responsible for validating the Workspace resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type WorkspaceValidator struct {
	// TODO(user): Add more fields as needed for validation
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type Workspace.
func (v *WorkspaceValidator) ValidateCreate(_ context.Context, obj *workspacesv1alpha1.Workspace) (admission.Warnings, error) {
	workspacelog.Info("Validation for Workspace upon creation", "name", obj.GetName())

	if err := validateImage(obj.Spec.Image); err != nil {
		return nil, invalidWorkspace(obj, field.ErrorList{
			field.Invalid(field.NewPath("spec", "image"), obj.Spec.Image, err.Error()),
		})
	}
	return nil, nil
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type Workspace.
func (v *WorkspaceValidator) ValidateUpdate(_ context.Context, oldObj, newObj *workspacesv1alpha1.Workspace) (admission.Warnings, error) {
	workspacelog.Info("Validation for Workspace upon update", "name", newObj.GetName())

	var errs field.ErrorList
	if newObj.Spec.Owner != oldObj.Spec.Owner {
		errs = append(errs, field.Forbidden(field.NewPath("spec", "owner"), "owner cannot be changed after workspace creation"))
	}
	if err := validateImage(newObj.Spec.Image); err != nil {
		errs = append(errs, field.Invalid(field.NewPath("spec", "image"), newObj.Spec.Image, err.Error()))
	}
	if len(errs) != 0 {
		return nil, invalidWorkspace(newObj, errs)
	}
	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type Workspace.
func (v *WorkspaceValidator) ValidateDelete(_ context.Context, obj *workspacesv1alpha1.Workspace) (admission.Warnings, error) {
	workspacelog.Info("Validation for Workspace upon deletion", "name", obj.GetName())

	return nil, nil
}

func validateImage(image string) error {
	if image == "" {
		return fmt.Errorf("image must be specified")
	}

	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return fmt.Errorf("image must be a valid container image reference")
	}
	if _, ok := allowedImageRepositories[reference.TrimNamed(named).Name()]; !ok {
		return fmt.Errorf("image repository is not allowed; allowed repositories: traefik/whoami, codercom/code-server")
	}
	return nil
}

func ownerLabelValue(owner string) string {
	var value strings.Builder
	for _, char := range strings.ToLower(owner) {
		switch {
		case char == '@':
			value.WriteString("_at_")
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_', char == '.':
			value.WriteRune(char)
		default:
			value.WriteByte('-')
		}
	}

	label := strings.Trim(value.String(), "-_.")
	if len(label) > maxLabelValueLength {
		label = strings.TrimRight(label[:maxLabelValueLength], "-_.")
	}
	return label
}

func invalidWorkspace(obj *workspacesv1alpha1.Workspace, errs field.ErrorList) error {
	return apierrors.NewInvalid(
		schema.GroupKind{Group: workspacesv1alpha1.GroupVersion.Group, Kind: "Workspace"},
		obj.Name,
		errs,
	)
}
