# Production install

`charts/nodra/values-production.yaml` is a single-replica PostgreSQL install.
File mode stays the chart default. This file does not turn on more than one
replica, and it does not configure point-in-time recovery.

## Helm

Create the Secret `nodra-production` before install. Required keys:
`admin-token`, `admin-user`, `admin-password`, `enrollment-token`,
`agent-local-token`, `database-url`. Do not put those values in the values file.

```bash
helm upgrade --install nodra charts/nodra -n nodra --create-namespace \
  -f charts/nodra/values-production.yaml
helm -n nodra test nodra
```

Before install, replace:

- `networkPolicy.ingressFrom` with the namespace that actually sends traffic (ingress controller, and any in-cluster API clients).
- `networkPolicy.egress.rules` with the PostgreSQL address. Add a rule for each webhook destination. Egress otherwise allows DNS only.
- `certManager.issuerName` with a ClusterIssuer that exists in the cluster.
- `ingress.hosts` and the TLS secret name.

`secrets.existingSecret` skips the chart-managed Secret. Set
`externalSecret.enabled` instead when External Secrets Operator should
create that Secret. Do not set both.

`helm test` enrolls a site named `helm-test` and posts one event. It is not
a soak and it does not prove HA.

## GitOps

Argo CD Application:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: nodra
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/zyvorai/nodra
    path: charts/nodra
    helm:
      valueFiles: [values-production.yaml]
  destination:
    server: https://kubernetes.default.svc
    namespace: nodra
```

Flux HelmRelease:

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: nodra
  namespace: nodra
spec:
  chart:
    spec:
      chart: charts/nodra
      sourceRef: {kind: GitRepository, name: nodra}
  valuesFrom:
    - kind: ConfigMap
      name: nodra-production-overrides
```

Keep the database URL and tokens in the cluster Secret or ExternalSecret, not in the Git values.

## Agent MQTT TLS

When the chart agent is enabled, set:

```yaml
agent:
  enabled: true
  mqtt:
    tls:
      existingSecret: edge-mqtt-tls
      requireClientCert: false
```

The Secret must contain `tls.crt` and `tls.key`. With `requireClientCert: true` it must also contain `ca.crt`. Those paths are written into `nodrad.json` only on first init; replace the agent PVC or edit the config to rotate them later.
