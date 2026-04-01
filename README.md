# CPU Request Shrink Controller

A lightweight Kubernetes controller that performs a **one-time in-place CPU request shrink** on Pods after their startup is complete — without recreating the Pod.

## Purpose

Some workloads need extra CPU during startup (JVM warm-up, cache loading, etc.) but require far less at steady state. This controller watches opted-in Pods and automatically reduces the CPU request once the container has started, freeing up cluster resources without disrupting the application.

## Container Image

The controller image is automatically built and published to GitHub Container Registry (GHCR) by GitHub Actions on every push to `main` and on version tags.

**Image:** `ghcr.io/jvillalbaj2lc/k8-hot-shrunk-requests`

### Pull the image

```bash
docker pull ghcr.io/jvillalbaj2lc/k8-hot-shrunk-requests:latest
```

### Available tags

| Tag | Description |
|---|---|
| `latest` | Latest build from `main` branch |
| `v0.1.0` | Specific release version |
| `sha-abc1234` | Specific commit SHA |

> **Note:** The first time the image is published, you may need to change the package visibility to **Public** in the [GitHub package settings](https://github.com/jvillalbaj2lc/k8-hot-shrunk-requests/pkgs/container/k8-hot-shrunk-requests/settings).

## How It Works

1. The controller watches all Pods in the cluster.
2. It only acts on Pods that have the opt-in label `autosize.k8s.io/shrink-cpu-request: "true"`.
3. It reads the desired final CPU request from annotation `autosize.k8s.io/final-cpu-request`.
4. It waits until the Pod is `Running` and the target container has `started == true`.
5. It patches the Pod using the **`/resize` subresource** to reduce only `resources.requests.cpu`.
6. It marks the Pod as processed with annotation `autosize.k8s.io/cpu-request-shrunk: "true"`.
7. Everything else (CPU limit, memory request, memory limit) remains unchanged.

## Labels and Annotations

| Key | Type | Required | Description |
|---|---|---|---|
| `autosize.k8s.io/shrink-cpu-request` | Label | **Yes** | Must be `"true"` to opt in |
| `autosize.k8s.io/final-cpu-request` | Annotation | **Yes** | Target CPU request value (e.g. `50m`) |
| `autosize.k8s.io/target-container` | Annotation | No | Container name to resize. If absent, uses the only container in the Pod |
| `autosize.k8s.io/cpu-request-shrunk` | Annotation | — | Set by controller after successful resize |

## Permissions Needed

The controller requires these RBAC permissions:

```yaml
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch", "patch", "update"]
- apiGroups: [""]
  resources: ["pods/resize"]
  verbs: ["patch", "update"]
- apiGroups: [""]
  resources: ["events"]
  verbs: ["create", "patch"]
```

## Local Development

### Prerequisites

- Go 1.25+
- Access to a Kubernetes cluster (e.g. kind, minikube) with `InPlacePodVerticalScaling` feature gate enabled
- `kubectl` configured

### Run Locally

```bash
# Build
make build

# Run against your current kubeconfig
make run

# Run tests
make test

# Format and vet
make fmt
make vet
```

### Build and Push Image Manually

```bash
# Build the image
make docker-build

# Push to GHCR (requires: docker login ghcr.io)
make docker-push

# Build with a custom tag
make docker-build IMG=ghcr.io/jvillalbaj2lc/k8-hot-shrunk-requests:v0.1.0
make docker-push IMG=ghcr.io/jvillalbaj2lc/k8-hot-shrunk-requests:v0.1.0
```

## Deploy to Cluster

### Using the GHCR image (recommended)

```bash
kubectl apply -f deploy/install.yaml
```

### Using kustomize with a pinned version

```bash
# Edit deploy/kustomization.yaml to set the desired tag, then:
kubectl apply -k deploy/
```

### Local development with kind

```bash
make docker-build IMG=cpu-shrink-controller:latest
kind load docker-image cpu-shrink-controller:latest
kubectl apply -f deploy/install.yaml
```

### Try It Out

```bash
# Create an example Pod
kubectl apply -f examples/pod.yaml

# Or create an example Deployment
kubectl apply -f examples/deployment.yaml

# Watch the controller logs
kubectl logs -n cpu-shrink-system deploy/cpu-shrink-controller -f

# Check if the Pod was resized
kubectl get pod example-app -o jsonpath='{.spec.containers[0].resources.requests.cpu}'
# Expected: 50m

kubectl get pod example-app -o jsonpath='{.metadata.annotations.autosize\.k8s\.io/cpu-request-shrunk}'
# Expected: true
```

## Creating a Release

To publish a versioned image:

```bash
git tag v0.1.0
git push origin v0.1.0
```

This triggers the publish workflow and creates image tags `v0.1.0`, `0.1`, and `sha-<commit>`.

## CI/CD

| Workflow | File | Triggers | Purpose |
|---|---|---|---|
| **CI** | `.github/workflows/ci.yml` | Push/PR to `main` | Runs vet, fmt check, and tests |
| **Publish Image** | `.github/workflows/publish.yml` | Push to `main`, version tags, manual | Builds and pushes multi-arch image to GHCR |

## Example Pod Spec

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example-app
  labels:
    autosize.k8s.io/shrink-cpu-request: "true"
  annotations:
    autosize.k8s.io/final-cpu-request: "50m"
    autosize.k8s.io/target-container: "app"
spec:
  containers:
    - name: app
      image: busybox
      command: ["sh", "-c", "echo started && sleep 3600"]
      resources:
        requests:
          cpu: 500m
          memory: 64Mi
        limits:
          cpu: "1"
          memory: 128Mi
      resizePolicy:
        - resourceName: cpu
          restartPolicy: NotRequired
```

The Pod starts with `500m` CPU request (Burstable QoS) and gets shrunk to `50m` after the container has started.

## Project Structure

```
.
├── main.go                              # Manager entrypoint
├── internal/controller/
│   ├── pod_resize_controller.go         # Reconciler + helpers
│   └── pod_resize_controller_test.go    # Unit tests
├── deploy/
│   └── install.yaml                     # Namespace, SA, RBAC, Deployment
├── examples/
│   ├── pod.yaml                         # Example opted-in Pod
│   └── deployment.yaml                  # Example opted-in Deployment
├── Dockerfile
├── Makefile
└── README.md
```

## Limitations

- **CPU only**: Memory resizing is not supported in this version.
- **No CRD**: Configuration is entirely via labels and annotations.
- **Single resize**: The controller only shrinks once per Pod lifetime.
- **Feature gate required**: The cluster must have `InPlacePodVerticalScaling` enabled.
- **Burstable QoS**: The example Pods use Burstable QoS (requests ≠ limits) to allow in-place resize. Guaranteed QoS Pods would require changing both requests and limits simultaneously.
- **No Deployment mutation**: The controller only patches running Pods. New Pods from a Deployment will start with the original higher CPU request and be shrunk individually.
- **No VPA integration**: This is a standalone, targeted mechanism — not a replacement for VPA.
