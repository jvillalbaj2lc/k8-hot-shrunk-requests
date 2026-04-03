package controllertests

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/jvillalbaj2lc/k8-hot-shrunk-requests/internal/controller"
)

func setupScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newFakeRecorder() *record.FakeRecorder {
	return record.NewFakeRecorder(10)
}

func boolPtr(b bool) *bool { return &b }

func basePod(name string, opts ...func(*corev1.Pod)) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "busybox",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
					},
				},
			},
		},
	}
	for _, o := range opts {
		o(pod)
	}
	return pod
}

func withLabel(pod *corev1.Pod) {
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[controller.LabelShrinkCPU] = "true"
}

func withFinalCPU(value string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[controller.AnnotationFinalCPU] = value
	}
}

func withTargetContainer(name string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[controller.AnnotationTargetContainer] = name
	}
}

func withShrinkMode(mode string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[controller.AnnotationShrinkMode] = mode
	}
}

func withStartupDelay(delay string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[controller.AnnotationStartupDelay] = delay
	}
}

func withShrunkAnnotation(pod *corev1.Pod) {
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[controller.AnnotationShrunk] = "true"
}

func withRunningAndStarted(pod *corev1.Pod) {
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:    pod.Spec.Containers[0].Name,
			Started: boolPtr(true),
		},
	}
}

func withRunningAndReady(pod *corev1.Pod) {
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:    pod.Spec.Containers[0].Name,
			Started: boolPtr(true),
			Ready:   true,
		},
	}
	pod.Status.Conditions = []corev1.PodCondition{
		{
			Type:               corev1.ContainersReady,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
		},
	}
}

func withRunningStartedNotReady(pod *corev1.Pod) {
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:    pod.Spec.Containers[0].Name,
			Started: boolPtr(true),
			Ready:   false,
		},
	}
}

func withPhase(phase corev1.PodPhase) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		pod.Status.Phase = phase
	}
}

func withContainerNotStarted(pod *corev1.Pod) {
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:    pod.Spec.Containers[0].Name,
			Started: boolPtr(false),
		},
	}
}

func reconcileReq(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}}
}

func expectNoRequeue(t *testing.T, res ctrl.Result) {
	t.Helper()
	if res.RequeueAfter > 0 {
		t.Error("expected no requeue")
	}
}

func expectRequeue(t *testing.T, res ctrl.Result) {
	t.Helper()
	if res.RequeueAfter == 0 {
		t.Error("expected requeue")
	}
}

func expectEvent(t *testing.T, rec *record.FakeRecorder) {
	t.Helper()
	select {
	case event := <-rec.Events:
		if event == "" {
			t.Error("expected a non-empty event")
		}
	default:
		t.Error("expected an event but none received")
	}
}
