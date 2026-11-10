# Local development

This guide runs the project on a local Kind cluster. The sample hostnames use
`localhost.pomerium.io` and are intended for access from the development
machine, not the public internet.

## Prerequisites

- Docker, Kind, `kubectl`, Go, and Make.
- OpenSSL and `htpasswd` for generating local secrets and password hashes.
- A local Docker daemon that can be mounted into the Kind node.
- cert-manager installed in the cluster for the operator and local certificates;
  follow the [cert-manager installation guide](https://cert-manager.io/docs/installation/).

## Create the cluster

```sh
kind create cluster --name docker-workspaces --config config/local-dev/kind.yaml
kubectl config use-context kind-docker-workspaces
```

The Kind configuration maps ports 80 and 443 to the host and mounts the Docker
socket. The controller's Docker provider expects the Kind network.

## Configure local authentication and routing

Install the Pomerium ingress controller and its CRDs by applying the local
Kustomize configuration:

```sh
kubectl apply -k config/local-dev/pomerium
```

The local CA and wildcard certificate are development examples. Apply them
only to this local cluster:

```sh
kubectl apply -f config/local-dev/auth/tls.yaml
kubectl -n pomerium wait --for=condition=Ready certificate/local-ca --timeout=120s
```

Trust the generated local CA in your browser/OS to avoid certificate warnings.
The certificate files and CoreDNS example are not intended for shared or
production clusters.

Create Pomerium's bootstrap secret with fresh local values:

```sh
kubectl create secret generic bootstrap -n pomerium \
  --from-literal=shared_secret="$(openssl rand -base64 32)" \
  --from-literal=cookie_secret="$(openssl rand -base64 32)"
```

Create a local Dex config from the safe template, then replace the client
secret in both locations and replace each bcrypt hash placeholder. The
resulting `dex.yaml` is git-ignored and must remain local:

```sh
cp config/local-dev/auth/dex.example.yaml config/local-dev/auth/dex.yaml
htpasswd -nBC 10 alice
```

`htpasswd` prompts for a password and prints a bcrypt hash; copy only the hash
portion into the matching Dex entry. Repeat for each account you intend to use.
Keep the client secret identical in Dex's `staticClients` entry and the
Kubernetes Secret at the end of the file. Do not commit `dex.yaml`.

Apply Dex and the Pomerium identity-provider configuration:

```sh
kubectl apply -f config/local-dev/auth/dex.yaml
kubectl apply -f config/local-dev/pomerium/global.yaml
```

The dashboard route has an example allowlist in
[`dashboard.yaml`](../config/local-dev/pomerium/dashboard.yaml). Update it to
match the local Dex accounts that should be allowed to create workspaces.
Workspace routes are generated with an owner policy.

The optional CoreDNS example rewrites these local hostnames to the Pomerium
Service for in-cluster clients. It changes cluster-wide DNS behavior; inspect
it before applying, and skip it unless Pods need that resolution:

```sh
kubectl apply -f config/local-dev/auth/coredns.yaml
kubectl -n kube-system rollout restart deployment/coredns
```

## Deploy and try a workspace

Build and load the development image, deploy the operator, and wait for its
rollout:

```sh
bash scripts/redeploy.sh
```

The script defaults to the `docker-workspaces` Kind cluster. Set
`CLUSTER=<kind-cluster-name>` if using another cluster.

Create a sample Workspace and check its status:

```sh
kubectl apply -f config/samples/ide.yaml
kubectl get ws -w
kubectl get ws ide -o jsonpath='{.status.url}{"\n"}'
```

The sample URL is `https://ide.localhost.pomerium.io`. Open
`https://workspaces.localhost.pomerium.io` for the dashboard. Both are local
routes that pass through Pomerium; no public tunnel is configured.

## Tests

```sh
make test
make lint
make test-e2e
```

The e2e target creates and deletes a dedicated Kind cluster named
`docker-workspace-test-e2e`. Do not use a cluster containing workloads or data
you need to keep.

## Troubleshooting

### The API server does not recognize `ws`

Check the current context and install the CRD with the project target:

```sh
kubectl config current-context
kubectl get crd workspaces.workspaces.example.com
make install
```

### A Workspace stays Pending or Creating

Check its status, the manager logs, and the Docker socket mount:

```sh
kubectl get ws <name> -o yaml
kubectl logs -n docker-workspace-system deployment/docker-workspace-controller-manager
docker exec docker-workspaces-control-plane sh -c 'test -S /var/run/docker.sock'
```

Also verify that the manager can reach Docker and that the requested image can
be pulled. A deleting Workspace remains until the controller can remove its
container.

### A workspace URL does not resolve or reach the container

Check the generated Ingress, service endpoints, Workspace status, and Pomerium
logs:

```sh
kubectl get ingress -A
kubectl get svc,endpoints -n <namespace> <name>
kubectl get ws <name> -o jsonpath='{.status.phase}{" "}{.status.ip}{" "}{.status.url}{"\n"}'
kubectl logs -n pomerium deployment/pomerium
```

Confirm the local hostname resolves to the proxy and that your browser trusts
the wildcard certificate.

### The dashboard redirects to login or denies access

Verify Dex is healthy, the dashboard allowlist includes the signed-in email,
and Pomerium forwards the email identity header:

```sh
kubectl get ingress -n docker-workspace-system workspace-dashboard -o yaml
kubectl logs -n dex deployment/dex
kubectl logs -n pomerium deployment/pomerium
```

To add an account, edit the local Dex file and dashboard allowlist, apply both,
and restart Dex:

```sh
kubectl apply -f config/local-dev/auth/dex.yaml
kubectl -n dex rollout restart deployment/dex
kubectl apply -f config/local-dev/pomerium/dashboard.yaml
```

For security boundaries and known limitations, see
[`security-and-production.md`](security-and-production.md).
