package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IsPodEligible checks if a Pod should be processed by this controller.
func IsPodEligible(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}

	if pod.Labels[LabelShrinkCPU] != "true" {
		return false
	}

	if pod.Annotations[AnnotationShrunk] == "true" {
		return false
	}

	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}

	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	return true
}

// ResolveShrinkMode returns the validated shrink mode and whether it is valid.
// Defaults to "started" if the annotation is missing.
func ResolveShrinkMode(pod *corev1.Pod) (string, bool) {
	raw, exists := pod.Annotations[AnnotationShrinkMode]
	if !exists || raw == "" {
		return ShrinkModeStarted, true
	}
	switch raw {
	case ShrinkModeStarted, ShrinkModeReady:
		return raw, true
	default:
		return "", false
	}
}

// MaybeParseStartupDelay parses the optional startup-delay annotation.
// Returns (0, true) if the annotation is absent.
// Returns (duration, true) if valid.
// Returns (0, false) if present but invalid.
func MaybeParseStartupDelay(pod *corev1.Pod) (time.Duration, bool) {
	raw, exists := pod.Annotations[AnnotationStartupDelay]
	if !exists || raw == "" {
		return 0, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}

// IsContainerStarted returns true if the named container has started == true.
func IsContainerStarted(pod *corev1.Pod, containerName string) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerName {
			return cs.Started != nil && *cs.Started
		}
	}
	return false
}

// IsContainerReady returns true if the named container has ready == true.
func IsContainerReady(pod *corev1.Pod, containerName string) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerName {
			return cs.Ready
		}
	}
	return false
}

// ShouldShrinkNow checks whether the trigger condition is met for the given mode.
func ShouldShrinkNow(pod *corev1.Pod, containerName, mode string) bool {
	switch mode {
	case ShrinkModeReady:
		return IsContainerReady(pod, containerName)
	default: // ShrinkModeStarted
		return IsContainerStarted(pod, containerName)
	}
}

// ContainerConditionAge returns how long ago the container condition was met.
// For "started" mode it uses the container's started-at time; for "ready" it
// uses the ContainersReady pod condition transition time.
// If the time cannot be determined, returns 0 (so the delay is considered not elapsed yet
// and the controller will requeue until the time becomes resolvable).
func ContainerConditionAge(pod *corev1.Pod, containerName, mode string) time.Duration {
	now := time.Now()

	if mode == ShrinkModeReady {
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.ContainersReady && cond.Status == corev1.ConditionTrue {
				return now.Sub(cond.LastTransitionTime.Time)
			}
		}
		return 0
	}

	// For "started" mode, use the running state start time of the container.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerName && cs.State.Running != nil {
			return now.Sub(cs.State.Running.StartedAt.Time)
		}
	}
	return 0
}

// GetCurrentCPURequest returns the current CPU request for the container and whether it exists.
func GetCurrentCPURequest(pod *corev1.Pod, containerIdx int) (resource.Quantity, bool) {
	reqs := pod.Spec.Containers[containerIdx].Resources.Requests
	if reqs == nil {
		return resource.Quantity{}, false
	}
	cpu, ok := reqs[corev1.ResourceCPU]
	return cpu, ok
}

// ResolveTargetContainer determines which container to resize.
// If the target-container annotation is set, use that.
// Otherwise, if there is exactly one non-init, non-ephemeral container, use it.
func ResolveTargetContainer(pod *corev1.Pod) (int, error) {
	annotation := pod.Annotations[AnnotationTargetContainer]

	if annotation != "" {
		for i, c := range pod.Spec.Containers {
			if c.Name == annotation {
				return i, nil
			}
		}
		return -1, fmt.Errorf("target container %q not found in pod spec", annotation)
	}

	if len(pod.Spec.Containers) == 1 {
		return 0, nil
	}

	return -1, fmt.Errorf("pod has %d containers and no %s annotation; cannot determine target",
		len(pod.Spec.Containers), AnnotationTargetContainer)
}

// containsString checks if a string slice contains the given value.
func containsString(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// PatchResize issues a strategic merge patch against the Pod /resize subresource.
func (r *PodResizeReconciler) PatchResize(ctx context.Context, pod *corev1.Pod, containerIdx int, cpu resource.Quantity) error {
	patch := client.MergeFrom(pod.DeepCopy())

	if pod.Spec.Containers[containerIdx].Resources.Requests == nil {
		pod.Spec.Containers[containerIdx].Resources.Requests = corev1.ResourceList{}
	}
	pod.Spec.Containers[containerIdx].Resources.Requests[corev1.ResourceCPU] = cpu

	return r.Client.SubResource("resize").Patch(ctx, pod, patch)
}

// VerifyResize re-reads the Pod and checks that the CPU request matches the expected value.
func (r *PodResizeReconciler) VerifyResize(ctx context.Context, nn types.NamespacedName, containerIdx int, expectedCPU resource.Quantity) error {
	var updated corev1.Pod
	if err := r.Client.Get(ctx, nn, &updated); err != nil {
		return fmt.Errorf("failed to re-read pod after resize: %w", err)
	}
	if containerIdx >= len(updated.Spec.Containers) {
		return fmt.Errorf("container index %d out of range after re-read", containerIdx)
	}
	actual, ok := GetCurrentCPURequest(&updated, containerIdx)
	if !ok {
		return fmt.Errorf("CPU request missing after resize patch")
	}
	if actual.Cmp(expectedCPU) != 0 {
		return fmt.Errorf("CPU request after resize is %s, expected %s", actual.String(), expectedCPU.String())
	}
	return nil
}

// MarkProcessed adds the "already shrunk" annotation to the Pod.
func (r *PodResizeReconciler) MarkProcessed(ctx context.Context, pod *corev1.Pod) error {
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[AnnotationShrunk] = "true"
	return r.Client.Patch(ctx, pod, patch)
}
