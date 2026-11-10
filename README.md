# Docker Workspaces

Docker Workspaces is a Kubernetes operator for creating browser-accessible
development environments from `Workspace` resources. A dashboard lets users
create, open, and delete their workspaces.

## How it works

The operator creates and manages a workspace container through the Docker
daemon available to the cluster. The dashboard and workspace URLs are routed
through an authenticated proxy. In the included local setup, those URLs resolve
only on the development machine; they are not public internet endpoints.

## Architecture

```mermaid
flowchart TD
    Browser[User browser]
    Dex[Dex identity provider]
    Pomerium[Pomerium authenticate and proxy]
    Dashboard[Workspace dashboard<br/>Go HTTP server]
    KubeAPI[Kubernetes API server]
    CRD[Workspace custom resource]
    Controller[Workspace controller]
    Docker[Docker Engine<br/>via mounted socket]
    Container[Workspace container]
    Service[Selectorless Service and Endpoints]
    Ingress[Owner-only Pomerium Ingress]

    Browser -->|Open dashboard URL| Pomerium
    Pomerium -->|OIDC login| Dex
    Dex -->|Identity assertion| Pomerium
    Pomerium -->|Authenticated request and email claim| Dashboard
    Dashboard -->|Create Workspace resource| KubeAPI
    KubeAPI --> CRD
    CRD -->|Watch event| Controller
    Controller -->|Pull, create, inspect, delete| Docker
    Docker --> Container
    Controller -->|Reconcile route target| Service
    Controller -->|Reconcile host and owner policy| Ingress
    Browser -->|Open workspace URL| Pomerium
    Pomerium -->|Check authenticated owner policy| Ingress
    Ingress --> Service
    Service --> Container
```

## Getting started

See the [local development guide](docs/local-development.md) to create a Kind
cluster, configure local authentication and routing, and deploy the operator.

Useful development commands:

```sh
make test
make lint
make test-e2e
```

End-to-end tests create and delete their own Kind cluster. Run them only when
that isolated cluster can safely be created on your machine.

## Security and production considerations

This project is intended for local development and learning. The current
controller uses the Docker socket, which grants broad control over its Docker
host, and workspace editor containers rely on the authenticated proxy for
access control. It does not yet provide hardened workload isolation or
persistent workspace storage.

Before deploying for multiple users, review the [security and production
considerations](docs/security-and-production.md) and design suitable
isolation, access control, storage, and operational safeguards.

## Project layout

- `api/` and `internal/`: Workspace API and controller.
- `config/`: Kubernetes resources, samples, and local development configuration.
- `dashboard/`: Workspace dashboard.
- `docs/`: Development and security guidance.
- `scripts/`: Local development helpers.
