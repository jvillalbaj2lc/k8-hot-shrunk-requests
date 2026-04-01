package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	pod.Labels[LabelShrinkCPU] = "true"
}

func withFinalCPU(value string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[AnnotationFinalCPU] = value
	}
}

func withTargetContainer(name string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[AnnotationTargetContainer] = name
	}
}

func withShrunkAnnotation(pod *corev1.Pod) {
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[AnnotationShrunk] = "true"
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

func TestPodWithoutLabel_Ignored(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("no-label", withRunningAndStarted, withFinalCPU("50m"))

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("no-label"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue for pod without label")
	}
}

func TestPodWithLabelButMissingAnnotation_Warning(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("missing-ann", withLabel, withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("missing-ann"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue for missing annotation")
	}

	select {
	case event := <-rec.Events:
		if event == "" {
			t.Error("expected a warning event")
		}
	default:
		t.Error("expected a warning event but none received")
	}
}

func TestPodNotRunning_Ignored(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("pending-pod", withLabel, withFinalCPU("50m"), withPhase(corev1.PodPending))

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("pending-pod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Pending pod is not eligible (phase != Running), should be ignored without requeue
	if res.Requeue {
		t.Error("expected no requeue for pending pod")
	}
}

func TestPodRunningButNotStarted_Requeue(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("not-started", withLabel, withFinalCPU("50m"), withContainerNotStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("not-started"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected requeue for container not yet started")
	}
}

func TestPodRunningAndStarted_ResizePatched(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("ready-pod", withLabel, withFinalCPU("50m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("ready-pod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue after successful resize")
	}

	// Verify the processed annotation was set
	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ready-pod"}, &updated); err != nil {
		t.Fatalf("failed to get updated pod: %v", err)
	}
	if updated.Annotations[AnnotationShrunk] != "true" {
		t.Error("expected shrunk annotation to be set")
	}

	// Verify the CPU request was actually resized to the target value
	gotCPU := updated.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	if gotCPU.String() != "50m" {
		t.Errorf("expected CPU request to be 50m after resize, got %s", gotCPU.String())
	}
}

func TestAlreadyProcessed_Ignored(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("processed", withLabel, withFinalCPU("50m"), withRunningAndStarted, withShrunkAnnotation)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("processed"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue for already processed pod")
	}
}

func TestInvalidTargetContainer_Warning(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("bad-target", withLabel, withFinalCPU("50m"), withTargetContainer("nonexistent"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("bad-target"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue for invalid target container")
	}

	select {
	case event := <-rec.Events:
		if event == "" {
			t.Error("expected a warning event")
		}
	default:
		t.Error("expected a warning event but none received")
	}
}

func TestInvalidCPUValue_Warning(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("bad-cpu", withLabel, withFinalCPU("not-a-cpu-value"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("bad-cpu"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue for invalid CPU value")
	}

	select {
	case event := <-rec.Events:
		if event == "" {
			t.Error("expected a warning event")
		}
	default:
		t.Error("expected a warning event but none received")
	}
}

func TestMultipleContainersNoAnnotation_Warning(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("multi-container", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name:  "sidecar",
		Image: "busybox",
	})

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("multi-container"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue for ambiguous target container")
	}

	select {
	case event := <-rec.Events:
		if event == "" {
			t.Error("expected a warning event")
		}
	default:
		t.Error("expected a warning event but none received")
	}
}

func TestSucceededPod_Ignored(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("succeeded", withLabel, withFinalCPU("50m"), withPhase(corev1.PodSucceeded))

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("succeeded"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue for succeeded pod")
	}
}

func TestTerminatingPod_Ignored(t *testing.T) {
	s := setupScheme(t)
	now := metav1.Now()
	pod := basePod("terminating", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.DeletionTimestamp = &now
	// Fake client requires a finalizer for DeletionTimestamp to be preserved
	pod.Finalizers = []string{"test/finalizer"}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("terminating"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue for terminating pod")
	}
}

// --- Unit tests for helper functions ---

func TestIsPodEligible(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name:     "eligible pod",
			pod:      basePod("ok", withLabel, withFinalCPU("50m"), withRunningAndStarted),
			expected: true,
		},
		{
			name:     "no label",
			pod:      basePod("no-label", withFinalCPU("50m"), withRunningAndStarted),
			expected: false,
		},
		{
			name:     "already processed",
			pod:      basePod("done", withLabel, withFinalCPU("50m"), withRunningAndStarted, withShrunkAnnotation),
			expected: false,
		},
		{
			name:     "succeeded phase",
			pod:      basePod("succ", withLabel, withPhase(corev1.PodSucceeded)),
			expected: false,
		},
		{
			name:     "failed phase",
			pod:      basePod("fail", withLabel, withPhase(corev1.PodFailed)),
			expected: false,
		},
		{
			name:     "pending phase",
			pod:      basePod("pend", withLabel, withPhase(corev1.PodPending)),
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isPodEligible(tc.pod)
			if got != tc.expected {
				t.Errorf("isPodEligible() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestResolveTargetContainer(t *testing.T) {
	tests := []struct {
		name      string
		pod       *corev1.Pod
		expectIdx int
		expectErr bool
	}{
		{
			name:      "single container no annotation",
			pod:       basePod("single"),
			expectIdx: 0,
		},
		{
			name: "annotation matches",
			pod: func() *corev1.Pod {
				p := basePod("multi", withTargetContainer("app"))
				p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar", Image: "busybox"})
				return p
			}(),
			expectIdx: 0,
		},
		{
			name: "annotation does not match",
			pod: func() *corev1.Pod {
				p := basePod("bad", withTargetContainer("missing"))
				return p
			}(),
			expectErr: true,
		},
		{
			name: "multiple containers no annotation",
			pod: func() *corev1.Pod {
				p := basePod("multi")
				p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "sidecar", Image: "busybox"})
				return p
			}(),
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idx, err := resolveTargetContainer(tc.pod)
			if tc.expectErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if idx != tc.expectIdx {
				t.Errorf("got index %d, want %d", idx, tc.expectIdx)
			}
		})
	}
}

func TestIsStartupComplete(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		target   string
		expected bool
	}{
		{
			name: "started true",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", Started: boolPtr(true)},
					},
				},
			},
			target:   "app",
			expected: true,
		},
		{
			name: "started false",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", Started: boolPtr(false)},
					},
				},
			},
			target:   "app",
			expected: false,
		},
		{
			name: "no status for container",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{},
				},
			},
			target:   "app",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isStartupComplete(tc.pod, tc.target)
			if got != tc.expected {
				t.Errorf("isStartupComplete() = %v, want %v", got, tc.expected)
			}
		})
	}
}

// verifyNoRequeue asserts that the result has no requeue and no error.
func verifyNoRequeue(t *testing.T, res ctrl.Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Error("expected no requeue")
	}
}

// getPod retrieves a pod from the fake client.
func getPod(t *testing.T, c client.Client, ns, name string) corev1.Pod {
	t.Helper()
	var pod corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &pod); err != nil {
		t.Fatalf("failed to get pod: %v", err)
	}
	return pod
}
