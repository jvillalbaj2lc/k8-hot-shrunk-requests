package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/jvillalbaj2lc/k8-hot-shrunk-requests/internal/config"
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
	Config   *config.ControllerConfig
}

// effectiveConfig returns the controller config, falling back to defaults if nil.
func (r *PodResizeReconciler) effectiveConfig() *config.ControllerConfig {
	if r.Config != nil {
		return r.Config
	}
	return config.DefaultConfig()
}

// Reconcile processes a single Pod event and performs in-place CPU request shrink if eligible.
func (r *PodResizeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	cfg := r.effectiveConfig()

	var pod corev1.Pod
	if err := r.Client.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Namespace filtering — silently skip if pod namespace is not allowed.
	if len(cfg.AllowedNamespaces) > 0 {
		if !containsString(cfg.AllowedNamespaces, pod.Namespace) {
			return ctrl.Result{}, nil
		}
	} else if len(cfg.ExcludedNamespaces) > 0 {
		if containsString(cfg.ExcludedNamespaces, pod.Namespace) {
			return ctrl.Result{}, nil
		}
	}

	// Excluded pods — silently skip if namespace/name is excluded.
	if containsString(cfg.ExcludedPods, pod.Namespace+"/"+pod.Name) {
		return ctrl.Result{}, nil
	}

	if !IsPodEligible(&pod) {
		return ctrl.Result{}, nil
	}

	// Validate shrink mode annotation, applying config default if absent.
	mode, ok := ResolveShrinkMode(&pod)
	if !ok {
		raw := pod.Annotations[AnnotationShrinkMode]
		logger.Info("invalid shrink-mode annotation, skipping pod",
			"pod", req.NamespacedName, "value", raw)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidShrinkMode",
			fmt.Sprintf("annotation %s has invalid value %q; valid values are %q and %q",
				AnnotationShrinkMode, raw, ShrinkModeStarted, ShrinkModeReady))
		return ctrl.Result{}, nil
	}
	if _, exists := pod.Annotations[AnnotationShrinkMode]; !exists {
		mode = cfg.DefaultShrinkMode
	}

	// Parse optional startup delay, applying config default if absent.
	delay, delayOK := MaybeParseStartupDelay(&pod)
	if !delayOK {
		raw := pod.Annotations[AnnotationStartupDelay]
		logger.Info("invalid startup-delay annotation, skipping pod",
			"pod", req.NamespacedName, "value", raw)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidStartupDelay",
			fmt.Sprintf("annotation %s has invalid value %q", AnnotationStartupDelay, raw))
		return ctrl.Result{}, nil
	}
	if _, exists := pod.Annotations[AnnotationStartupDelay]; !exists {
		delay = cfg.DefaultStartupDelay
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
	containerIdx, err := ResolveTargetContainer(&pod)
	if err != nil {
		logger.Info("cannot resolve target container, skipping pod",
			"pod", req.NamespacedName, "error", err)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidTargetContainer", err.Error())
		return ctrl.Result{}, nil
	}

	containerName := pod.Spec.Containers[containerIdx].Name

	// Excluded containers — silently skip if container name is excluded.
	if containsString(cfg.ExcludedContainers, containerName) {
		return ctrl.Result{}, nil
	}

	// Check current CPU request and enforce true shrink semantics.
	currentCPU, hasCPU := GetCurrentCPURequest(&pod, containerIdx)
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
	if !ShouldShrinkNow(&pod, containerName, mode) {
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
		elapsed := ContainerConditionAge(&pod, containerName, mode)
		if elapsed < delay {
			remaining := delay - elapsed
			logger.V(1).Info("startup delay not yet elapsed, requeuing",
				"pod", req.NamespacedName, "container", containerName,
				"delay", delay.String(), "remaining", remaining.String())
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	// Patch the resize subresource.
	if err := r.PatchResize(ctx, &pod, containerIdx, cpuQuantity); err != nil {
		logger.Error(err, "failed to resize pod", "pod", req.NamespacedName)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "ResizeFailed",
			fmt.Sprintf("failed to patch pod resize: %v", err))
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	// Verify the resize was applied before marking as processed.
	if err := r.VerifyResize(ctx, req.NamespacedName, containerIdx, cpuQuantity); err != nil {
		logger.Error(err, "resize patch applied but verification failed, requeuing",
			"pod", req.NamespacedName)
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	// Re-fetch the pod to get the latest resource version before marking processed.
	if err := r.Client.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.MarkProcessed(ctx, &pod); err != nil {
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
