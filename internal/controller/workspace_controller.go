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
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	workspacesv1alpha1 "github.com/utkarsh2801/docker-workspaces/api/v1alpha1"
	"github.com/utkarsh2801/docker-workspaces/internal/compute"
)

// WorkspaceReconciler reconciles a Workspace object
type WorkspaceReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Compute compute.Provider
}

// +kubebuilder:rbac:groups=workspaces.example.com,resources=workspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workspaces.example.com,resources=workspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workspaces.example.com,resources=workspaces/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Workspace object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/reconcile
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	var ws workspacesv1alpha1.Workspace
	if err := r.Get(ctx, req.NamespacedName, &ws); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	uid := string(ws.UID)

	inst, err := r.Compute.Get(ctx, uid)
	if errors.Is(err, compute.ErrNotFound) || (err == nil && inst.State != "running") {
		logf.Log.Info("Creating workspace", "uid", uid)
		inst, err = r.Compute.Create(ctx, compute.CreateRequest{
			UID:   uid,
			Owner: ws.Spec.Owner,
			Image: ws.Spec.Image,
			Port:  int(ws.Spec.Port),
		})
	}

	if err != nil {
		return ctrl.Result{}, err
	}

	want := workspacesv1alpha1.WorkspaceStatus{
		Phase:       workspacesv1alpha1.PhaseCreating,
		ContainerID: inst.ID,
		IP:          inst.IP,
		URL:         inst.URL,
		Message:     "container" + inst.State,
	}

	if inst.State == "running" {
		want.Phase = workspacesv1alpha1.PhaseRunning
	}

	if ws.Status != want {
		ws.Status = want
		if err := r.Status().Update(ctx, &ws); err != nil {
			return ctrl.Result{}, err
		}
	}

	if want.Phase != workspacesv1alpha1.PhaseRunning {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&workspacesv1alpha1.Workspace{}).
		Named("workspace").
		Complete(r)
}
