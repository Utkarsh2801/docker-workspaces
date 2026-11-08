#!/usr/bin/env bash
#rebuilds the operator image and redeploys it to the cluster
set -euo pipefail
cluster="${CLUSTER:-docker-workspaces}"
make docker-build IMG=mini-workspaces:dev
kind load docker-image mini-workspaces:dev --name "$cluster"
make deploy IMG=mini-workspaces:dev

#same tags as last time, so the deployment looks unchanged. Force new pods.
kubectl -n docker-workspace-system rollout restart deploy/docker-workspace-controller-manager
kubectl -n docker-workspace-system rollout status deploy/docker-workspace-controller-manager --timeout=120s
