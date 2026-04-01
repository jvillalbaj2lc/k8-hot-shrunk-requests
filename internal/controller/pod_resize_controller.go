package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Constants for labels and annotations used by the PodResize controller.
const (
	LabelShrinkCPU            = "autosize.k8s.io/shrink-cpu-request"
	AnnotationFinalCPU        = "autosize.k8s.io/final-cpu-request"
	AnnotationTargetContainer = "autosize.k8s.io/target-container"
	AnnotationShrunk          = "autosize.k8s.io/cpu-request-shrunk"
	AnnotationShrinkMode      = "autosize.k8s.io/shrink-mode"
	AnnotationStartupDelay    = "autosize.k8s.io/startup-delay"

	// Shrink mode values.
	ShrinkModeStarted = "started"
	ShrinkModeReady   = "ready"

	requeueDelay = 5 * time.Second
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=pods/resize,verbs=patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// PodResizeReconciler watches Pods and performs in-place CPU request shrink.
type PodResizeReconciler struct {
	Client   client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// Reconcile processes a single Pod event and performs in-place CPU request shrink if eligible.
func (r *PodResizeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Client.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !isPodEligible(&pod) {
		return ctrl.Result{}, nil
	}

	// Validate shrink mode annotation.
	mode, ok := resolveShrinkMode(&pod)
	if !ok {
		raw := pod.Annotations[AnnotationShrinkMode]
		logger.Info("invalid shrink-mode annotation, skipping pod",
			"pod", req.NamespacedName, "value", raw)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidShrinkMode",
			fmt.Sprintf("annotation %s has invalid value %q; valid values are %q and %q",
				AnnotationShrinkMode, raw, ShrinkModeStarted, ShrinkModeReady))
		return ctrl.Result{}, nil
	}

	// Parse optional startup delay.
	delay, delayOK := maybeParseStartupDelay(&pod)
	if !delayOK {
		raw := pod.Annotations[AnnotationStartupDelay]
		logger.Info("invalid startup-delay annotation, skipping pod",
			"pod", req.NamespacedName, "value", raw)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidStartupDelay",
			fmt.Sprintf("annotation %s has invalid value %q", AnnotationStartupDelay, raw))
		return ctrl.Result{}, nil
	}

	// Validate final-cpu-request annotation.
	finalCPU, ok := pod.Annotations[AnnotationFinalCPU]
	if !ok || finalCPU == "" {
		logger.Info("missing final-cpu-request annotation, skipping pod", "pod", req.NamespacedName)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "MissingAnnotation",
			fmt.Sprintf("annotation %s is required but missing", AnnotationFinalCPU))
		return ctrl.Result{}, nil
	}

	cpuQuantity, err := resource.ParseQuantity(finalCPU)
	if err != nil {
		logger.Info("invalid final-cpu-request value, skipping pod",
			"pod", req.NamespacedName, "value", finalCPU, "error", err)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidAnnotation",
			fmt.Sprintf("annotation %s has invalid value %q: %v", AnnotationFinalCPU, finalCPU, err))
		return ctrl.Result{}, nil
	}

	// Resolve target container.
	containerIdx, err := resolveTargetContainer(&pod)
	if err != nil {
		logger.Info("cannot resolve target container, skipping pod",
			"pod", req.NamespacedName, "error", err)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidTargetContainer", err.Error())
		return ctrl.Result{}, nil
	}

	containerName := pod.Spec.Containers[containerIdx].Name

	// Check current CPU request and enforce true shrink semantics.
	currentCPU, hasCPU := getCurrentCPURequest(&pod, containerIdx)
	if !hasCPU {
		logger.Info("current CPU request not set on target container, skipping pod",
			"pod", req.NamespacedName, "container", containerName)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "MissingCPURequest",
			fmt.Sprintf("container %s has no current CPU request set", containerName))
		return ctrl.Result{}, nil
	}

	cmp := cpuQuantity.Cmp(currentCPU)
	if cmp == 0 {
		logger.Info("final CPU equals current CPU, nothing to do",
			"pod", req.NamespacedName, "container", containerName, "cpu", currentCPU.String())
		r.Recorder.Event(&pod, corev1.EventTypeNormal, "ShrinkSkipped",
			fmt.Sprintf("final CPU request %s equals current request for container %s",
				cpuQuantity.String(), containerName))
		return ctrl.Result{}, nil
	}
	if cmp > 0 {
		logger.Info("final CPU is greater than current CPU, refusing to upscale",
			"pod", req.NamespacedName, "container", containerName,
			"currentCPU", currentCPU.String(), "finalCPU", cpuQuantity.String())
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "ShrinkRefused",
			fmt.Sprintf("final CPU request %s is greater than current %s for container %s; controller only shrinks",
				cpuQuantity.String(), currentCPU.String(), containerName))
		return ctrl.Result{}, nil
	}

	// Check if the trigger condition is met.
	if !shouldShrinkNow(&pod, containerName, mode) {
		reason := "container not yet started"
		if mode == ShrinkModeReady {
			reason = "container not yet ready"
		}
		logger.V(1).Info(reason+", requeuing",
			"pod", req.NamespacedName, "container", containerName, "mode", mode)
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	// If startup delay is configured, verify it has elapsed since the container became ready/started.
	if delay > 0 {
		elapsed := containerConditionAge(&pod, containerName, mode)
		if elapsed < delay {
			remaining := delay - elapsed
			logger.V(1).Info("startup delay not yet elapsed, requeuing",
				"pod", req.NamespacedName, "container", containerName,
				"delay", delay.String(), "remaining", remaining.String())
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	// Patch the resize subresource.
	if err := r.patchResize(ctx, &pod, containerIdx, cpuQuantity); err != nil {
		logger.Error(err, "failed to resize pod", "pod", req.NamespacedName)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "ResizeFailed",
			fmt.Sprintf("failed to patch pod resize: %v", err))
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	// Verify the resize was applied before marking as processed.
	if err := r.verifyResize(ctx, req.NamespacedName, containerIdx, cpuQuantity); err != nil {
		logger.Error(err, "resize patch applied but verification failed, requeuing",
			"pod", req.NamespacedName)
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	// Re-fetch the pod to get the latest resource version before marking processed.
	if err := r.Client.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.markProcessed(ctx, &pod); err != nil {
		logger.Error(err, "failed to mark pod as processed", "pod", req.NamespacedName)
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	logger.Info("successfully shrunk CPU request",
		"pod", req.NamespacedName,
		"container", containerName,
		"previousCPU", currentCPU.String(),
		"newCPURequest", cpuQuantity.String())
	r.Recorder.Event(&pod, corev1.EventTypeNormal, "CPURequestShrunk",
		fmt.Sprintf("shrunk CPU request of container %s from %s to %s",
			containerName, currentCPU.String(), cpuQuantity.String()))

	return ctrl.Result{}, nil
}

// --- Helpers ---

// resolveShrinkMode returns the validated shrink mode and whether it is valid.
// Defaults to "started" if the annotation is missing.
func resolveShrinkMode(pod *corev1.Pod) (string, bool) {
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

// maybeParseStartupDelay parses the optional startup-delay annotation.
// Returns (0, true) if the annotation is absent.
// Returns (duration, true) if valid.
// Returns (0, false) if present but invalid.
func maybeParseStartupDelay(pod *corev1.Pod) (time.Duration, bool) {
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

// isContainerStarted returns true if the named container has started == true.
func isContainerStarted(pod *corev1.Pod, containerName string) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerName {
			return cs.Started != nil && *cs.Started
		}
	}
	return false
}

// isContainerReady returns true if the named container has ready == true.
func isContainerReady(pod *corev1.Pod, containerName string) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerName {
			return cs.Ready
		}
	}
	return false
}

// shouldShrinkNow checks whether the trigger condition is met for the given mode.
func shouldShrinkNow(pod *corev1.Pod, containerName, mode string) bool {
	switch mode {
	case ShrinkModeReady:
		return isContainerReady(pod, containerName)
	default: // ShrinkModeStarted
		return isContainerStarted(pod, containerName)
	}
}

// containerConditionAge returns how long ago the container condition was met.
// For "started" mode it uses the container's started-at time; for "ready" it
// uses the ContainersReady pod condition transition time.
// If the time cannot be determined, returns 0 (so the delay is considered not elapsed yet on first pass,
// but the controller will requeue and the time will eventually be resolvable or the delay will be skipped).
func containerConditionAge(pod *corev1.Pod, containerName, mode string) time.Duration {
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

// getCurrentCPURequest returns the current CPU request for the container and whether it exists.
func getCurrentCPURequest(pod *corev1.Pod, containerIdx int) (resource.Quantity, bool) {
	reqs := pod.Spec.Containers[containerIdx].Resources.Requests
	if reqs == nil {
		return resource.Quantity{}, false
	}
	cpu, ok := reqs[corev1.ResourceCPU]
	return cpu, ok
}

// resolveTargetContainer determines which container to resize.
// If the target-container annotation is set, use that.
// Otherwise, if there is exactly one non-init, non-ephemeral container, use it.
func resolveTargetContainer(pod *corev1.Pod) (int, error) {
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

// Kept for backward compatibility with existing tests.
func isStartupComplete(pod *corev1.Pod, containerName string) bool {
	return isContainerStarted(pod, containerName)
}

// patchResize issues a strategic merge patch against the Pod /resize subresource.
func (r *PodResizeReconciler) patchResize(ctx context.Context, pod *corev1.Pod, containerIdx int, cpu resource.Quantity) error {
	patch := client.MergeFrom(pod.DeepCopy())

	if pod.Spec.Containers[containerIdx].Resources.Requests == nil {
		pod.Spec.Containers[containerIdx].Resources.Requests = corev1.ResourceList{}
	}
	pod.Spec.Containers[containerIdx].Resources.Requests[corev1.ResourceCPU] = cpu

	return r.Client.SubResource("resize").Patch(ctx, pod, patch)
}

// verifyResize re-reads the Pod and checks that the CPU request matches the expected value.
func (r *PodResizeReconciler) verifyResize(ctx context.Context, nn types.NamespacedName, containerIdx int, expectedCPU resource.Quantity) error {
	var updated corev1.Pod
	if err := r.Client.Get(ctx, nn, &updated); err != nil {
		return fmt.Errorf("failed to re-read pod after resize: %w", err)
	}
	if containerIdx >= len(updated.Spec.Containers) {
		return fmt.Errorf("container index %d out of range after re-read", containerIdx)
	}
	actual, ok := getCurrentCPURequest(&updated, containerIdx)
	if !ok {
		return fmt.Errorf("CPU request missing after resize patch")
	}
	if actual.Cmp(expectedCPU) != 0 {
		return fmt.Errorf("CPU request after resize is %s, expected %s", actual.String(), expectedCPU.String())
	}
	return nil
}

// markProcessed adds the "already shrunk" annotation to the Pod.
func (r *PodResizeReconciler) markProcessed(ctx context.Context, pod *corev1.Pod) error {
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[AnnotationShrunk] = "true"
	return r.Client.Patch(ctx, pod, patch)
}

// isPodEligible checks if a Pod should be processed by this controller.
func isPodEligible(pod *corev1.Pod) bool {
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

// SetupWithManager registers the controller with the manager.
func (r *PodResizeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return obj.GetLabels()[LabelShrinkCPU] == "true"
		})).
		Named("pod-resize").
		Complete(r)
}

// NewPodResizeReconciler creates a reconciler for use in tests or programmatic setup.
func NewPodResizeReconciler(c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) *PodResizeReconciler {
	return &PodResizeReconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: recorder,
	}
}

// ReconcilePodByName is a test helper to build a reconcile request.
func ReconcilePodByName(ns, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}
