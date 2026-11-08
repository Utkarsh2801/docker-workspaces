package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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

	var endpoints corev1.Endpoints
	if err := k8sClient.Get(context.Background(), key, &endpoints); err != nil {
		t.Fatalf("get Endpoints: %v", err)
	}
	if len(endpoints.Subsets) != 1 || len(endpoints.Subsets[0].Addresses) != 1 || len(endpoints.Subsets[0].Ports) != 1 {
		t.Fatalf("Endpoints subsets = %+v, want one subset with one address", endpoints.Subsets)
	}
	if got := endpoints.Subsets[0]; got.Addresses[0].IP != "10.10.0.4" || got.Ports[0].Name != routePortName || got.Ports[0].Port != workspace.Spec.Port {
		t.Errorf("Endpoints subset = %+v, want IP 10.10.0.4 and port %d named %q", got, workspace.Spec.Port, routePortName)
	}

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
	path := ingress.Spec.Rules[0].HTTP.Paths[0]
	if path.Backend.Service == nil || path.Backend.Service.Name != workspace.Name || path.Backend.Service.Port.Name != routePortName {
		t.Errorf("Ingress backend = %+v, want Service %q port %q", path.Backend.Service, workspace.Name, routePortName)
	}
	if ingress.Annotations[pomeriumPublic] != "true" || ingress.Annotations[pomeriumHost] != "true" {
		t.Errorf("Ingress annotations = %v, want public access and websockets enabled", ingress.Annotations)
	}
	if len(ingress.OwnerReferences) != 1 || ingress.OwnerReferences[0].UID != workspace.UID {
		t.Errorf("Ingress owner references = %v, want Workspace owner", ingress.OwnerReferences)
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
	provider := &FakeProvider{}
	reconciler, k8sClient := newTestReconciler(t, workspace, provider)
	if err := k8sClient.Create(context.Background(), existingService); err != nil {
		t.Fatalf("create existing Service: %v", err)
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

	var endpoints corev1.Endpoints
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(workspace), &endpoints); err != nil {
		t.Fatalf("get updated Endpoints: %v", err)
	}
	if got := endpoints.Subsets[0].Addresses[0].IP; got != "10.10.0.5" {
		t.Errorf("endpoint IP = %q, want updated IP 10.10.0.5", got)
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
