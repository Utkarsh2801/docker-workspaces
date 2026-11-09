package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	workspacesv1alpha1 "github.com/utkarsh2801/docker-workspaces/api/v1alpha1"
)

const (
	hostSuffix       = ".localhost.pomerium.io"
	routePortName    = "http"
	routeServicePort = 80
	pomeriumPublic   = "ingress.pomerium.io/allow_public_unauthenticated_access"
	pomeriumHost     = "ingress.pomerium.io/allow_websockets"
	pomeriumPolicy   = "ingress.pomerium.io/policy"
)

func routeURL(ws *workspacesv1alpha1.Workspace) string {
	return "https://" + ws.Name + hostSuffix
}

func (r *WorkspaceReconciler) ensureRouting(ctx context.Context, ws *workspacesv1alpha1.Workspace, ip string) error {
	if err := r.ensureService(ctx, ws); err != nil {
		return err
	}
	if err := r.ensureEndpoints(ctx, ws, ip); err != nil {
		return err
	}
	return r.ensureIngress(ctx, ws)
}

func (r *WorkspaceReconciler) ensureService(ctx context.Context, ws *workspacesv1alpha1.Workspace) error {
	service := &corev1.Service{}
	service.Name = ws.Name
	service.Namespace = ws.Namespace

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = mergeLabels(service.Labels, ws.Labels)
		service.Spec.Selector = nil
		service.Spec.Type = corev1.ServiceTypeClusterIP
		service.Spec.Ports = []corev1.ServicePort{{
			Name:       routePortName,
			Port:       routeServicePort,
			TargetPort: intstr.FromInt32(ws.Spec.Port),
			Protocol:   corev1.ProtocolTCP,
		}}
		return controllerutil.SetControllerReference(ws, service, r.Scheme)
	})
	return err
}

func (r *WorkspaceReconciler) ensureEndpoints(ctx context.Context, ws *workspacesv1alpha1.Workspace, ip string) error {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return fmt.Errorf("workspace %s/%s has invalid endpoint IP %q", ws.Namespace, ws.Name, ip)
	}

	// Pomerium's ingress controller reads core Endpoints for backend discovery.
	endpoints := &corev1.Endpoints{} //nolint:staticcheck // Pomerium still discovers backends from core Endpoints.
	endpoints.Name = ws.Name
	endpoints.Namespace = ws.Namespace

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, endpoints, func() error {
		endpoints.Labels = mergeLabels(endpoints.Labels, ws.Labels)
		endpoints.Subsets = []corev1.EndpointSubset{{ //nolint:staticcheck // Required by Pomerium's Endpoints-based discovery.
			Addresses: []corev1.EndpointAddress{{IP: ip}},
			Ports: []corev1.EndpointPort{{
				Name:     routePortName,
				Port:     ws.Spec.Port,
				Protocol: corev1.ProtocolTCP,
			}},
		}}
		return controllerutil.SetControllerReference(ws, endpoints, r.Scheme)
	})
	if err != nil {
		return err
	}

	return r.deleteObsoleteEndpointSlice(ctx, ws)
}

func (r *WorkspaceReconciler) deleteObsoleteEndpointSlice(ctx context.Context, ws *workspacesv1alpha1.Workspace) error {
	endpointSlice := &discoveryv1.EndpointSlice{}
	key := types.NamespacedName{Name: ws.Name, Namespace: ws.Namespace}
	if err := r.Get(ctx, key, endpointSlice); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !metav1.IsControlledBy(endpointSlice, ws) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, endpointSlice))
}

func (r *WorkspaceReconciler) ensureIngress(ctx context.Context, ws *workspacesv1alpha1.Workspace) error {
	policy, err := ownerPolicy(ws.Spec.Owner)
	if err != nil {
		return err
	}

	ingress := &networkingv1.Ingress{}
	ingress.Name = ws.Name
	ingress.Namespace = ws.Namespace

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, ingress, func() error {
		ingress.Labels = mergeLabels(ingress.Labels, ws.Labels)
		ingress.Annotations = mergeLabels(ingress.Annotations, map[string]string{
			pomeriumHost:   "true",
			pomeriumPolicy: policy,
		})
		delete(ingress.Annotations, pomeriumPublic)
		className := "pomerium"
		pathType := networkingv1.PathTypePrefix
		ingress.Spec = networkingv1.IngressSpec{
			IngressClassName: &className,
			Rules: []networkingv1.IngressRule{{
				Host: ws.Name + hostSuffix,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: ws.Name,
									Port: networkingv1.ServiceBackendPort{Name: routePortName},
								},
							},
						}},
					},
				},
			}},
		}
		return controllerutil.SetControllerReference(ws, ingress, r.Scheme)
	})
	return err
}

func ownerPolicy(owner string) (string, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "", fmt.Errorf("workspace owner must be set to create an owner-only route policy")
	}

	policy := []map[string]any{
		{
			"allow": map[string]any{
				"and": []map[string]any{
					{"email": map[string]string{"is": strings.ToLower(owner)}},
				},
			},
		},
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("encode owner policy for %q: %w", owner, err)
	}
	return string(encoded), nil
}

func mergeLabels(existing, additions map[string]string) map[string]string {
	if existing == nil {
		existing = make(map[string]string, len(additions))
	}
	maps.Copy(existing, additions)
	return existing
}
