package controller

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	workspacesv1alpha1 "github.com/utkarsh2801/docker-workspaces/api/v1alpha1"
	"github.com/utkarsh2801/docker-workspaces/internal/compute"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testWorkspaceName = "test-workspace"
	testNamespace     = "default"
)

var testUID = types.UID("workspace-uid")

func newTestReconciler(t *testing.T, workspace *workspacesv1alpha1.Workspace, provider *FakeProvider) (*WorkspaceReconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := workspacesv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Workspace API to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	if err := discoveryv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add discovery API to scheme: %v", err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add networking API to scheme: %v", err)
	}

	builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&workspacesv1alpha1.Workspace{})
	if workspace != nil {
		builder = builder.WithObjects(workspace)
	}
	k8sClient := builder.Build()

	return &WorkspaceReconciler{
		Client:  k8sClient,
		Scheme:  scheme,
		Compute: provider,
	}, k8sClient
}

func testWorkspace(withFinalizer bool) *workspacesv1alpha1.Workspace {
	finalizers := []string(nil)
	if withFinalizer {
		finalizers = []string{containerFinalizerName}
	}
	return &workspacesv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testWorkspaceName,
			Namespace:  testNamespace,
			UID:        testUID,
			Finalizers: finalizers,
		},
		Spec: workspacesv1alpha1.WorkspaceSpec{
			Owner: "owner@example.com",
			Image: "example/image:latest",
			Port:  8080,
		},
	}
}

func reconcileWorkspace(t *testing.T, reconciler *WorkspaceReconciler) (ctrl.Result, error) {
	t.Helper()
	return reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testWorkspaceName, Namespace: testNamespace},
	})
}

func TestReconcileNotFound(t *testing.T) {
	provider := &FakeProvider{}
	reconciler, _ := newTestReconciler(t, nil, provider)

	result, err := reconcileWorkspace(t, reconciler)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Errorf("Reconcile() result = %+v, want empty result", result)
	}
	if len(provider.Created) != 0 || len(provider.Deleted) != 0 {
		t.Errorf("provider was called for a missing Workspace: created=%d deleted=%d", len(provider.Created), len(provider.Deleted))
	}
}

func TestReconcileAddsFinalizerBeforeProvisioning(t *testing.T) {
	provider := &FakeProvider{}
	reconciler, k8sClient := newTestReconciler(t, testWorkspace(false), provider)

	result, err := reconcileWorkspace(t, reconciler)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Errorf("Reconcile() result = %+v, want empty result", result)
	}
	if len(provider.Created) != 0 {
		t.Fatalf("Create() called %d times before finalizer was stored, want 0", len(provider.Created))
	}

	var got workspacesv1alpha1.Workspace
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: testWorkspaceName, Namespace: testNamespace}, &got); err != nil {
		t.Fatalf("get Workspace after reconcile: %v", err)
	}
	if !containsFinalizer(got.Finalizers, containerFinalizerName) {
		t.Errorf("Workspace finalizers = %v, want %q", got.Finalizers, containerFinalizerName)
	}
}

func TestReconcileCreatesMissingInstanceAndUpdatesStatus(t *testing.T) {
	provider := &FakeProvider{}
	workspace := testWorkspace(true)
	reconciler, k8sClient := newTestReconciler(t, workspace, provider)

	result, err := reconcileWorkspace(t, reconciler)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Errorf("RequeueAfter = %s, want 10s for creating instance", result.RequeueAfter)
	}
	if len(provider.Created) != 1 {
		t.Fatalf("Create() called %d times, want 1", len(provider.Created))
	}
	gotRequest := provider.Created[0]
	if gotRequest.UID != string(testUID) ||
		gotRequest.Owner != "owner@example.com" ||
		gotRequest.Image != "example/image:latest" ||
		gotRequest.Port != 8080 ||
		len(gotRequest.Args) != 0 {
		t.Errorf("Create() request = %+v, want UID %q, owner %q, image %q, port %d, no args",
			gotRequest, testUID, "owner@example.com", "example/image:latest", 8080)
	}

	var got workspacesv1alpha1.Workspace
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: testWorkspaceName, Namespace: testNamespace}, &got); err != nil {
		t.Fatalf("get Workspace after reconcile: %v", err)
	}
	wantStatus := workspacesv1alpha1.WorkspaceStatus{
		Phase:       workspacesv1alpha1.PhaseCreating,
		ContainerID: "container-" + string(testUID),
		Message:     "containercreating",
	}
	if got.Status != wantStatus {
		t.Errorf("Workspace status = %+v, want %+v", got.Status, wantStatus)
	}
}

func TestReconcileKeepsRunningInstance(t *testing.T) {
	provider := &FakeProvider{
		Instances: map[string]*compute.Instance{
			string(testUID): {
				ID:    "container-running",
				UID:   string(testUID),
				State: compute.StateRunning,
				IP:    "10.0.0.4",
				URL:   "http://workspace.example.com",
			},
		},
	}
	workspace := testWorkspace(true)
	reconciler, k8sClient := newTestReconciler(t, workspace, provider)

	result, err := reconcileWorkspace(t, reconciler)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != time.Minute {
		t.Errorf("RequeueAfter = %s, want 1m for running instance", result.RequeueAfter)
	}
	if len(provider.Created) != 0 {
		t.Errorf("Create() called %d times for running instance, want 0", len(provider.Created))
	}

	var got workspacesv1alpha1.Workspace
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: testWorkspaceName, Namespace: testNamespace}, &got); err != nil {
		t.Fatalf("get Workspace after reconcile: %v", err)
	}
	wantStatus := workspacesv1alpha1.WorkspaceStatus{
		Phase:       workspacesv1alpha1.PhaseRunning,
		ContainerID: "container-running",
		IP:          "10.0.0.4",
		URL:         routeURL(workspace),
		Message:     "containerrunning",
	}
	if got.Status != wantStatus {
		t.Errorf("Workspace status = %+v, want %+v", got.Status, wantStatus)
	}
	for _, resource := range []client.Object{
		&corev1.Service{},
		&discoveryv1.EndpointSlice{},
		&networkingv1.Ingress{},
	} {
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: workspace.Name, Namespace: workspace.Namespace}, resource); err != nil {
			t.Errorf("get reconciled %T: %v", resource, err)
		}
	}
}

func TestReconcileRecreatesStoppedInstance(t *testing.T) {
	provider := &FakeProvider{
		Instances: map[string]*compute.Instance{
			string(testUID): {ID: "container-stopped", State: "exited"},
		},
	}
	reconciler, _ := newTestReconciler(t, testWorkspace(true), provider)

	_, err := reconcileWorkspace(t, reconciler)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(provider.Created) != 1 {
		t.Errorf("Create() called %d times for stopped instance, want 1", len(provider.Created))
	}
}

func TestReconcileReturnsProviderErrors(t *testing.T) {
	getErr := errors.New("provider lookup failed")
	createErr := errors.New("provider create failed")

	for _, tt := range []struct {
		name     string
		provider *FakeProvider
	}{
		{name: "get error", provider: &FakeProvider{GetErr: getErr}},
		{name: "create error", provider: &FakeProvider{CreateErr: createErr}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, _ := newTestReconciler(t, testWorkspace(true), tt.provider)
			_, err := reconcileWorkspace(t, reconciler)
			if err == nil {
				t.Fatal("Reconcile() error = nil, want provider error")
			}
			if tt.name == "get error" && !errors.Is(err, getErr) {
				t.Errorf("Reconcile() error = %v, want %v", err, getErr)
			}
			if tt.name == "create error" && !errors.Is(err, createErr) {
				t.Errorf("Reconcile() error = %v, want %v", err, createErr)
			}
		})
	}
}

func TestReconcileDeletesInstanceAndRemovesFinalizer(t *testing.T) {
	provider := &FakeProvider{}
	reconciler, k8sClient := newTestReconciler(t, testWorkspace(true), provider)
	key := types.NamespacedName{Name: testWorkspaceName, Namespace: testNamespace}

	var workspace workspacesv1alpha1.Workspace
	if err := k8sClient.Get(context.Background(), key, &workspace); err != nil {
		t.Fatalf("get Workspace: %v", err)
	}
	if err := k8sClient.Delete(context.Background(), &workspace); err != nil {
		t.Fatalf("delete Workspace: %v", err)
	}

	_, err := reconcileWorkspace(t, reconciler)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(provider.Deleted) != 1 || provider.Deleted[0] != string(testUID) {
		t.Errorf("Delete() calls = %v, want [%q]", provider.Deleted, testUID)
	}
	err = k8sClient.Get(context.Background(), key, &workspace)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get Workspace after deletion: %v", err)
	}
	if err == nil {
		if containsFinalizer(workspace.Finalizers, containerFinalizerName) {
			t.Errorf("Workspace finalizer %q remains after deletion", containerFinalizerName)
		}
	}
}

func TestReconcileDoesNotRemoveFinalizerWhenDeleteFails(t *testing.T) {
	deleteErr := errors.New("provider delete failed")
	provider := &FakeProvider{DeleteErr: deleteErr}
	reconciler, k8sClient := newTestReconciler(t, testWorkspace(true), provider)
	key := types.NamespacedName{Name: testWorkspaceName, Namespace: testNamespace}

	var workspace workspacesv1alpha1.Workspace
	if err := k8sClient.Get(context.Background(), key, &workspace); err != nil {
		t.Fatalf("get Workspace: %v", err)
	}
	if err := k8sClient.Delete(context.Background(), &workspace); err != nil {
		t.Fatalf("delete Workspace: %v", err)
	}

	_, err := reconcileWorkspace(t, reconciler)
	if !errors.Is(err, deleteErr) {
		t.Fatalf("Reconcile() error = %v, want %v", err, deleteErr)
	}

	if err := k8sClient.Get(context.Background(), key, &workspace); err != nil {
		t.Fatalf("get Workspace after failed provider deletion: %v", err)
	}
	if !containsFinalizer(workspace.Finalizers, containerFinalizerName) {
		t.Errorf("Workspace finalizers = %v, want deletion finalizer retained", workspace.Finalizers)
	}
}

func containsFinalizer(finalizers []string, name string) bool {
	return slices.Contains(finalizers, name)
}
