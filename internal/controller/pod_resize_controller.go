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

const (
	LabelShrinkCPU            = "autosize.k8s.io/shrink-cpu-request"
	AnnotationFinalCPU        = "autosize.k8s.io/final-cpu-request"
	AnnotationTargetContainer = "autosize.k8s.io/target-container"
	AnnotationShrunk          = "autosize.k8s.io/cpu-request-shrunk"

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

func (r *PodResizeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Client.Get(ctx, req.NamespacedName, &pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !isPodEligible(&pod) {
		return ctrl.Result{}, nil
	}

	finalCPU, ok := pod.Annotations[AnnotationFinalCPU]
	if !ok || finalCPU == "" {
		logger.Info("missing final-cpu-request annotation", "pod", req.NamespacedName)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "MissingAnnotation",
			fmt.Sprintf("annotation %s is required but missing", AnnotationFinalCPU))
		return ctrl.Result{}, nil
	}

	cpuQuantity, err := resource.ParseQuantity(finalCPU)
	if err != nil {
		logger.Info("invalid final-cpu-request value", "pod", req.NamespacedName, "value", finalCPU, "error", err)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidAnnotation",
			fmt.Sprintf("annotation %s has invalid value %q: %v", AnnotationFinalCPU, finalCPU, err))
		return ctrl.Result{}, nil
	}

	containerIdx, err := resolveTargetContainer(&pod)
	if err != nil {
		logger.Info("cannot resolve target container", "pod", req.NamespacedName, "error", err)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "InvalidTargetContainer", err.Error())
		return ctrl.Result{}, nil
	}

	if !isStartupComplete(&pod, pod.Spec.Containers[containerIdx].Name) {
		logger.V(1).Info("target container not yet started, requeuing", "pod", req.NamespacedName)
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	if err := r.patchResize(ctx, &pod, containerIdx, cpuQuantity); err != nil {
		logger.Error(err, "failed to resize pod", "pod", req.NamespacedName)
		r.Recorder.Event(&pod, corev1.EventTypeWarning, "ResizeFailed",
			fmt.Sprintf("failed to patch pod resize: %v", err))
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	if err := r.markProcessed(ctx, &pod); err != nil {
		logger.Error(err, "failed to mark pod as processed", "pod", req.NamespacedName)
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	logger.Info("successfully shrunk CPU request",
		"pod", req.NamespacedName,
		"container", pod.Spec.Containers[containerIdx].Name,
		"newCPURequest", cpuQuantity.String())
	r.Recorder.Event(&pod, corev1.EventTypeNormal, "CPURequestShrunk",
		fmt.Sprintf("shrunk CPU request of container %s to %s",
			pod.Spec.Containers[containerIdx].Name, cpuQuantity.String()))

	return ctrl.Result{}, nil
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

// isStartupComplete returns true if the named container has started == true.
func isStartupComplete(pod *corev1.Pod, containerName string) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerName {
			return cs.Started != nil && *cs.Started
		}
	}
	return false
}

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
