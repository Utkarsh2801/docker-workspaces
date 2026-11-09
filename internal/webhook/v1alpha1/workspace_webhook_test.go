package v1alpha1

import (
	"context"
	"strings"
	"testing"

	workspacesv1alpha1 "github.com/utkarsh2801/docker-workspaces/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	testCodeServerLatest = "codercom/code-server:latest"
	testOwnerEmail       = "owner@example.com"
)

func TestWorkspaceDefaulterDefault(t *testing.T) {
	tests := []struct {
		name       string
		image      string
		owner      string
		labels     map[string]string
		wantImage  string
		wantOwner  string
		wantLabels map[string]string
	}{
		{
			name:      "defaults image and initializes owner label",
			owner:     "Owner.Name+tag@Example.COM",
			wantImage: defaultWorkspaceImage,
			wantOwner: "owner.name-tag_at_example.com",
		},
		{
			name:       "preserves explicit image and existing labels",
			image:      testCodeServerLatest,
			owner:      testOwnerEmail,
			labels:     map[string]string{"team": "platform"},
			wantImage:  testCodeServerLatest,
			wantOwner:  "owner_at_example.com",
			wantLabels: map[string]string{"team": "platform"},
		},
	}

	defaulter := &WorkspaceDefaulter{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := &workspacesv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Labels: tt.labels},
				Spec:       workspacesv1alpha1.WorkspaceSpec{Owner: tt.owner, Image: tt.image},
			}
			if err := defaulter.Default(context.Background(), workspace); err != nil {
				t.Fatalf("Default() error = %v", err)
			}

			if workspace.Spec.Image != tt.wantImage {
				t.Errorf("image = %q, want %q", workspace.Spec.Image, tt.wantImage)
			}
			if got := workspace.Labels[OwnerLabel]; got != tt.wantOwner {
				t.Errorf("owner label = %q, want %q", got, tt.wantOwner)
			}
			if workspace.Labels["team"] != tt.wantLabels["team"] {
				t.Errorf("existing team label = %q, want %q", workspace.Labels["team"], tt.wantLabels["team"])
			}
		})
	}
}

func TestOwnerLabelValue(t *testing.T) {
	longOwner := strings.Repeat("A", 70) + "@example.com"
	tests := []struct {
		name  string
		owner string
		want  string
	}{
		{name: "normalizes owner email", owner: "Owner.Name+tag@Example.COM", want: "owner.name-tag_at_example.com"},
		{name: "truncates to label value limit", owner: longOwner, want: strings.Repeat("a", maxLabelValueLength)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ownerLabelValue(tt.owner)
			if got != tt.want {
				t.Fatalf("ownerLabelValue() = %q, want %q", got, tt.want)
			}
			if len(got) > maxLabelValueLength {
				t.Errorf("owner label length = %d, exceeds %d", len(got), maxLabelValueLength)
			}
			if problems := validation.IsValidLabelValue(got); len(problems) != 0 {
				t.Errorf("owner label value %q is invalid: %v", got, problems)
			}
		})
	}
}

func TestWorkspaceValidatorValidateCreate(t *testing.T) {
	tests := []struct {
		name    string
		image   string
		wantErr bool
	}{
		{name: "traefik repository", image: defaultWorkspaceImage},
		{name: "traefik tagged image", image: "traefik/whoami:v1.10.2"},
		{name: "code-server repository", image: "codercom/code-server"},
		{name: "code-server tagged image", image: testCodeServerLatest},
		{name: "empty image", image: "", wantErr: true},
		{name: "unapproved repository", image: "nginx:latest", wantErr: true},
		{name: "invalid image reference", image: "not an image", wantErr: true},
	}

	validator := &WorkspaceValidator{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := &workspacesv1alpha1.Workspace{Spec: workspacesv1alpha1.WorkspaceSpec{Image: tt.image}}
			_, err := validator.ValidateCreate(context.Background(), workspace)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCreate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !apierrors.IsInvalid(err) {
				t.Errorf("ValidateCreate() error = %T, want Kubernetes Invalid error", err)
			}
		})
	}
}

func TestWorkspaceValidatorValidateUpdate(t *testing.T) {
	tests := []struct {
		name     string
		oldOwner string
		newOwner string
		newImage string
		wantErr  bool
	}{
		{
			name:     "same owner and approved image",
			oldOwner: testOwnerEmail,
			newOwner: testOwnerEmail,
			newImage: "traefik/whoami:v1.10.2",
		},
		{
			name:     "owner cannot change",
			oldOwner: testOwnerEmail,
			newOwner: "other@example.com",
			newImage: defaultWorkspaceImage,
			wantErr:  true,
		},
		{
			name:     "updated image must be approved",
			oldOwner: testOwnerEmail,
			newOwner: testOwnerEmail,
			newImage: "nginx:latest",
			wantErr:  true,
		},
	}

	validator := &WorkspaceValidator{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldWorkspace := &workspacesv1alpha1.Workspace{Spec: workspacesv1alpha1.WorkspaceSpec{
				Owner: tt.oldOwner,
				Image: defaultWorkspaceImage,
			}}
			newWorkspace := &workspacesv1alpha1.Workspace{Spec: workspacesv1alpha1.WorkspaceSpec{
				Owner: tt.newOwner,
				Image: tt.newImage,
			}}

			_, err := validator.ValidateUpdate(context.Background(), oldWorkspace, newWorkspace)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateUpdate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !apierrors.IsInvalid(err) {
				t.Errorf("ValidateUpdate() error = %T, want Kubernetes Invalid error", err)
			}
		})
	}
}

func TestWorkspaceValidatorValidateDelete(t *testing.T) {
	validator := &WorkspaceValidator{}
	_, err := validator.ValidateDelete(context.Background(), &workspacesv1alpha1.Workspace{})
	if err != nil {
		t.Fatalf("ValidateDelete() error = %v, want nil", err)
	}
}

func TestValidateImageAllowsOnlyApprovedRepositories(t *testing.T) {
	tests := []struct {
		image   string
		wantErr bool
	}{
		{image: "docker.io/traefik/whoami:latest"},
		{image: "docker.io/codercom/code-server:4.96.4"},
		{image: "traefik/whoami@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{image: "ghcr.io/traefik/whoami:latest", wantErr: true},
		{image: "docker.io/library/traefik/whoami:latest", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			err := validateImage(tt.image)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateImage(%q) error = %v, wantErr %v", tt.image, err, tt.wantErr)
			}
		})
	}
}
