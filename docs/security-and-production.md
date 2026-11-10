# Security and production considerations

The current setup is designed for local development and learning, not as a
hardened multi-user service. The key limitations are specific to how this
project currently creates and exposes workspaces:

- **Docker host access:** The controller mounts `/var/run/docker.sock`. A
  compromised controller or a user who can influence Docker operations may
  gain broad control of the Docker host. Treat access to the controller and
  its RBAC permissions as highly privileged.
- **Workspace isolation:** Workspaces are Docker containers managed through
  that daemon. They do not provide the isolation boundary of dedicated
  virtual machines or hardened per-tenant Kubernetes workloads.
- **Editor access:** The sample code-server configuration uses `--auth none`.
  It is safe only while every route remains behind Pomerium and its owner
  policy. Do not expose the container port directly or bypass that proxy.
- **Local identity and certificates:** The included Dex accounts and
  self-signed certificates are for development. Use a managed identity
  provider, protected secret storage, and appropriately issued certificates
  outside a local cluster.
- **Workspace data:** Workspace files are stored in the container filesystem.
  Deleting a Workspace removes that container and its local data; persistent
  volumes, backup, and recovery are not implemented.
- **Local-only routing:** `*.localhost.pomerium.io` is configured for local
  development. The project does not publish these URLs or configure a public
  tunnel.

Before considering a multi-user or production deployment, establish and test
at least the following controls:

1. Replace direct Docker-daemon control with a properly isolated workload
   execution model, and verify tenant boundaries.
2. Apply least-privilege Kubernetes RBAC, workload security policies, and
   resource quotas/limits.
3. Enforce authentication and authorization on every route; remove
   unauthenticated access paths and validate owner checks.
4. Use managed secrets with rotation, trusted TLS, and explicit network
   policies.
5. Define persistent storage, backup, and recovery behavior, including what
   happens when a workspace is deleted.
6. Add image provenance/scanning, audit logs, monitoring, and tested upgrade
   and incident-response procedures.

These are design and validation requirements, not settings that can be made
production-safe merely by changing the local sample manifests.
