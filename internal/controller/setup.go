package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

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

// IsStartupComplete is kept for backward compatibility with existing tests.
func IsStartupComplete(pod *corev1.Pod, containerName string) bool {
	return IsContainerStarted(pod, containerName)
}
