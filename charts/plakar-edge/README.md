# plakar-edge

Helm chart for running a StatefulSet of [plakar-edge](../../README.md) pods.

Each replica is an independent edge: it enrolls against the control plane on
first boot, is assigned its own edge identity/token, and persists that
identity plus its plaklet package cache on its own PersistentVolumeClaim
(`data-<release>-plakar-edge-<ordinal>`). Because it's a StatefulSet, each pod
also gets a stable hostname (`<release>-plakar-edge-0`, `-1`, ...) that
`plakar-edge` uses as its registered edge name by default.

plakar-edge has no listening port - it only makes outbound calls to the
control plane - so this chart deploys a headless Service (required by
StatefulSet) with no ports, and no liveness/readiness probes. If the process
crashes, the pod's `restartPolicy: Always` already restarts it.

## Prerequisites

Create a Secret holding the enrollment key **before** installing the chart:

```sh
kubectl create secret generic plakar-edge-enroll-key \
  --from-literal=enroll-key=<key from the control plane>
```

The chart only references this Secret by name; it never accepts the key
directly in `values.yaml`, so it can't leak into `helm history` or rendered
manifests.

## Install

`image.tag` can be omitted when installing a chart version published by the
release workflow (e.g. `helm install my-edges oci://ghcr.io/plakarkorp/charts/plakar-edge --version X.Y.Z`) -
the default already points at the matching `vX.Y.Z` image. It's shown
explicitly below for a local/dev install, which otherwise has no release
version to derive it from:

```sh
helm install my-edges ./charts/plakar-edge \
  --set controlPlane=https://plakman.example.com \
  --set image.tag=<sha>-<run_id> \
  --set enrollKey.secretName=plakar-edge-enroll-key \
  --set replicaCount=3
```

## Values

| Key | Default | Description |
|-----|---------|--------------|
| `replicaCount` | `1` | Number of edges to run |
| `image.repository` | `ghcr.io/plakarkorp/plakar-edge` | Image repository |
| `image.tag` | `""` (falls back to `Chart.AppVersion`, which matches the `vX.Y.Z` image pushed for a released chart version) | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `controlPlane` | `""` | Control plane API base URL (required) |
| `pollHold` | `""` | `-poll-hold`; empty uses the binary's own default |
| `enrollKey.secretName` | `""` | Name of an existing Secret with the enrollment key (required) |
| `enrollKey.secretKey` | `enroll-key` | Key within that Secret |
| `persistence.size` | `5Gi` | PVC size per replica |
| `persistence.storageClassName` | `""` | StorageClass; empty uses the cluster default |
| `persistence.mountPath` | `/data` | Mount path; `-state-dir`/`-pkg` are `<mountPath>/state` and `<mountPath>/pkg` |
| `resources` | `{}` | Container resource requests/limits |
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `nodeSelector`, `tolerations`, `affinity` | `{}` / `[]` / `{}` | Standard pod scheduling controls |
