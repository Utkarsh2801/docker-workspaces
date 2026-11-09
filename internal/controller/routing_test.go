package controller

import (
	"context"
	"encoding/json"
	"testing"

	workspacesv1alpha1 "github.com/utkarsh2801/docker-workspaces/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestEnsureRoutingCreatesServiceEndpointsAndIngress(t *testing.T) {
	workspace := testWorkspace(true)
	workspace.Labels = map[string]string{"workspaces.example.com/owner": "owner_at_example.com"}
	provider := &FakeProvider{}
	reconciler, k8sClient := newTestReconciler(t, workspace, provider)

	if err := reconciler.ensureRouting(context.Background(), workspace, "10.10.0.4"); err != nil {
		t.Fatalf("ensureRouting() error = %v", err)
	}

	key := types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}
	assertWorkspaceService(t, k8sClient, key, workspace)
	assertWorkspaceEndpoints(t, k8sClient, key, workspace, "10.10.0.4")
	assertWorkspaceIngress(t, k8sClient, key, workspace)
}

func assertWorkspaceEndpoints(t *testing.T, k8sClient client.Client, key types.NamespacedName, workspace *workspacesv1alpha1.Workspace, ip string) {
	t.Helper()
	var endpoints corev1.Endpoints //nolint:staticcheck // Pomerium still discovers backends from core Endpoints.
	if err := k8sClient.Get(context.Background(), key, &endpoints); err != nil {
		t.Fatalf("get Endpoints: %v", err)
	}
	if len(endpoints.Subsets) != 1 || len(endpoints.Subsets[0].Addresses) != 1 || len(endpoints.Subsets[0].Ports) != 1 {
		t.Fatalf("Endpoints = %+v, want one address and one port", endpoints.Subsets)
	}
	if got := endpoints.Subsets[0].Addresses[0].IP; got != ip {
		t.Errorf("Endpoints address = %q, want %q", got, ip)
	}
	if got := endpoints.Subsets[0].Ports[0]; got.Name != routePortName || got.Port != workspace.Spec.Port {
		t.Errorf("Endpoints port = %+v, want name %q port %d", got, routePortName, workspace.Spec.Port)
	}
}

func assertWorkspaceService(t *testing.T, k8sClient client.Client, key types.NamespacedName, workspace *workspacesv1alpha1.Workspace) {
	t.Helper()
	var service corev1.Service
	if err := k8sClient.Get(context.Background(), key, &service); err != nil {
		t.Fatalf("get Service: %v", err)
	}
	if service.Spec.Selector != nil {
		t.Errorf("Service selector = %v, want nil for manually managed Endpoints", service.Spec.Selector)
	}
	if len(service.Spec.Ports) != 1 {
		t.Fatalf("Service ports = %v, want one port", service.Spec.Ports)
	}
	if got := service.Spec.Ports[0]; got.Name != routePortName || got.Port != routeServicePort || got.TargetPort.IntVal != workspace.Spec.Port {
		t.Errorf("Service port = %+v, want name %q port %d targetPort %d", got, routePortName, routeServicePort, workspace.Spec.Port)
	}
	if service.Labels["workspaces.example.com/owner"] != "owner_at_example.com" {
		t.Errorf("Service labels = %v, owner label was not propagated", service.Labels)
	}
}

func assertWorkspaceIngress(t *testing.T, k8sClient client.Client, key types.NamespacedName, workspace *workspacesv1alpha1.Workspace) {
	t.Helper()
	var ingress networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), key, &ingress); err != nil {
		t.Fatalf("get Ingress: %v", err)
	}
	if ingress.Spec.IngressClassName == nil || *ingress.Spec.IngressClassName != "pomerium" {
		t.Errorf("IngressClassName = %v, want pomerium", ingress.Spec.IngressClassName)
	}
	if len(ingress.Spec.Rules) != 1 || ingress.Spec.Rules[0].Host != workspace.Name+hostSuffix {
		t.Fatalf("Ingress rules = %+v, want host %q", ingress.Spec.Rules, workspace.Name+hostSuffix)
	}
	if ingress.Spec.Rules[0].HTTP == nil || len(ingress.Spec.Rules[0].HTTP.Paths) != 1 {
		t.Fatalf("Ingress HTTP rules = %+v, want one path", ingress.Spec.Rules[0].HTTP)
	}
	path := ingress.Spec.Rules[0].HTTP.Paths[0]
	if path.Backend.Service == nil || path.Backend.Service.Name != workspace.Name || path.Backend.Service.Port.Name != routePortName {
		t.Errorf("Ingress backend = %+v, want Service %q port %q", path.Backend.Service, workspace.Name, routePortName)
	}
	wantPolicy := `[{"allow":{"and":[{"email":{"is":"owner@example.com"}}]}}]`
	if ingress.Annotations[pomeriumPolicy] != wantPolicy {
		t.Errorf("Ingress owner policy = %q, want %q", ingress.Annotations[pomeriumPolicy], wantPolicy)
	}
	var decodedPolicy []map[string]any
	if err := json.Unmarshal([]byte(ingress.Annotations[pomeriumPolicy]), &decodedPolicy); err != nil {
		t.Errorf("Ingress owner policy is not valid JSON: %v", err)
	}
	if len(decodedPolicy) != 1 {
		t.Errorf("Ingress owner policy has %d rules, want 1", len(decodedPolicy))
	}
	if _, isPublic := ingress.Annotations[pomeriumPublic]; isPublic {
		t.Errorf("Ingress has public access annotation: %v", ingress.Annotations)
	}
	if ingress.Annotations[pomeriumHost] != "true" {
		t.Errorf("Ingress annotations = %v, want websockets enabled", ingress.Annotations)
	}
	if len(ingress.OwnerReferences) != 1 || ingress.OwnerReferences[0].UID != workspace.UID {
		t.Errorf("Ingress owner references = %v, want Workspace owner", ingress.OwnerReferences)
	}
}

func TestOwnerPolicy(t *testing.T) {
	tests := []struct {
		name    string
		owner   string
		want    string
		wantErr bool
	}{
		{
			name:  "normalizes owner email",
			owner: " Owner@Example.COM ",
			want:  `[{"allow":{"and":[{"email":{"is":"owner@example.com"}}]}}]`,
		},
		{
			name:    "rejects empty owner",
			owner:   " ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ownerPolicy(tt.owner)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ownerPolicy() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ownerPolicy() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ownerPolicy() = %q, want %q", got, tt.want)
			}
			var decoded []map[string]any
			if err := json.Unmarshal([]byte(got), &decoded); err != nil {
				t.Errorf("ownerPolicy() returned invalid JSON: %v", err)
			}
		})
	}
}

func TestRouteURL(t *testing.T) {
	workspace := testWorkspace(true)
	if got, want := routeURL(workspace), "https://test-workspace.localhost.pomerium.io"; got != want {
		t.Errorf("routeURL() = %q, want %q", got, want)
	}
}

func TestEnsureRoutingUpdatesExistingResources(t *testing.T) {
	workspace := testWorkspace(true)
	existingService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspace.Name,
			Namespace: workspace.Namespace,
			Labels:    map[string]string{"existing": "preserved"},
		},
	}
	existingIngress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspace.Name,
			Namespace: workspace.Namespace,
			Annotations: map[string]string{
				pomeriumPublic: "true",
			},
		},
	}
	existingEndpoints := &corev1.Endpoints{ //nolint:staticcheck // Pomerium still discovers backends from core Endpoints.
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspace.Name,
			Namespace: workspace.Namespace,
		},
		Subsets: []corev1.EndpointSubset{{ //nolint:staticcheck // Required by Pomerium's Endpoints-based discovery.
			Addresses: []corev1.EndpointAddress{{IP: "10.10.0.4"}},
		}},
	}
	provider := &FakeProvider{}
	reconciler, k8sClient := newTestReconciler(t, workspace, provider)
	if err := k8sClient.Create(context.Background(), existingService); err != nil {
		t.Fatalf("create existing Service: %v", err)
	}
	if err := k8sClient.Create(context.Background(), existingIngress); err != nil {
		t.Fatalf("create existing Ingress: %v", err)
	}
	if err := k8sClient.Create(context.Background(), existingEndpoints); err != nil {
		t.Fatalf("create existing Endpoints: %v", err)
	}

	if err := reconciler.ensureRouting(context.Background(), workspace, "10.10.0.5"); err != nil {
		t.Fatalf("ensureRouting() error = %v", err)
	}

	var service corev1.Service
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(workspace), &service); err != nil {
		t.Fatalf("get updated Service: %v", err)
	}
	if service.Labels["existing"] != "preserved" {
		t.Errorf("existing Service label was not preserved: %v", service.Labels)
	}

	var endpoints corev1.Endpoints //nolint:staticcheck // Pomerium still discovers backends from core Endpoints.
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(workspace), &endpoints); err != nil {
		t.Fatalf("get updated Endpoints: %v", err)
	}
	if got := endpoints.Subsets[0].Addresses[0].IP; got != "10.10.0.5" {
		t.Errorf("Endpoints IP = %q, want updated IP 10.10.0.5", got)
	}

	var ingress networkingv1.Ingress
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(workspace), &ingress); err != nil {
		t.Fatalf("get updated Ingress: %v", err)
	}
	if _, isPublic := ingress.Annotations[pomeriumPublic]; isPublic {
		t.Errorf("public access annotation was not removed: %v", ingress.Annotations)
	}
	if ingress.Annotations[pomeriumPolicy] == "" {
		t.Errorf("owner policy annotation was not set: %v", ingress.Annotations)
	}
}

func TestEnsureRoutingRejectsInvalidEndpointIP(t *testing.T) {
	workspace := testWorkspace(true)
	reconciler, _ := newTestReconciler(t, workspace, &FakeProvider{})

	err := reconciler.ensureRouting(context.Background(), workspace, "not-an-ip")
	if err == nil {
		t.Fatal("ensureRouting() error = nil, want invalid endpoint IP error")
	}
}

func TestEnsureRoutingDeletesObsoleteOwnedEndpointSlice(t *testing.T) {
	workspace := testWorkspace(true)
	controller := true
	obsoleteSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspace.Name,
			Namespace: workspace.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: workspacesv1alpha1.GroupVersion.String(),
				Kind:       "Workspace",
				Name:       workspace.Name,
				UID:        workspace.UID,
				Controller: &controller,
			}},
		},
	}
	reconciler, k8sClient := newTestReconciler(t, workspace, &FakeProvider{})
	if err := k8sClient.Create(context.Background(), obsoleteSlice); err != nil {
		t.Fatalf("create obsolete EndpointSlice: %v", err)
	}

	if err := reconciler.ensureRouting(context.Background(), workspace, "10.10.0.4"); err != nil {
		t.Fatalf("ensureRouting() error = %v", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(workspace), &discoveryv1.EndpointSlice{}); !apierrors.IsNotFound(err) {
		t.Errorf("obsolete EndpointSlice lookup error = %v, want NotFound", err)
	}
}
