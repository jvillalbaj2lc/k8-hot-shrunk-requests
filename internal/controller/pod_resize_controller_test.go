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

func withShrinkMode(mode string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[AnnotationShrinkMode] = mode
	}
}

func withStartupDelay(delay string) func(*corev1.Pod) {
	return func(pod *corev1.Pod) {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[AnnotationStartupDelay] = delay
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

// --- Reconciler integration tests ---

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
	expectNoRequeue(t, res)
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
	expectNoRequeue(t, res)
	expectEvent(t, rec)
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
	expectNoRequeue(t, res)
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
	expectRequeue(t, res)
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
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ready-pod"}, &updated); err != nil {
		t.Fatalf("failed to get updated pod: %v", err)
	}
	if updated.Annotations[AnnotationShrunk] != "true" {
		t.Error("expected shrunk annotation to be set")
	}

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
	expectNoRequeue(t, res)
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
	expectNoRequeue(t, res)
	expectEvent(t, rec)
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
	expectNoRequeue(t, res)
	expectEvent(t, rec)
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
	expectNoRequeue(t, res)
	expectEvent(t, rec)
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
	expectNoRequeue(t, res)
}

func TestTerminatingPod_Ignored(t *testing.T) {
	s := setupScheme(t)
	now := metav1.Now()
	pod := basePod("terminating", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.DeletionTimestamp = &now
	pod.Finalizers = []string{"test/finalizer"}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("terminating"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
}

// --- New tests for shrink mode ---

func TestDefaultMode_Started(t *testing.T) {
	s := setupScheme(t)
	// No shrink-mode annotation → defaults to "started"
	pod := basePod("default-mode", withLabel, withFinalCPU("50m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("default-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "default-mode"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[AnnotationShrunk] != "true" {
		t.Error("expected shrunk annotation after default mode shrink")
	}
}

func TestModeReady_ContainerReady_Shrinks(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("ready-mode", withLabel, withFinalCPU("50m"), withShrinkMode("ready"), withRunningAndReady)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("ready-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ready-mode"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[AnnotationShrunk] != "true" {
		t.Error("expected shrunk annotation after ready mode shrink")
	}
	gotCPU := updated.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	if gotCPU.String() != "50m" {
		t.Errorf("expected CPU 50m, got %s", gotCPU.String())
	}
}

func TestModeReady_ContainerNotReady_Requeues(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("not-ready", withLabel, withFinalCPU("50m"), withShrinkMode("ready"), withRunningStartedNotReady)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("not-ready"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectRequeue(t, res)
}

func TestInvalidShrinkMode_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("bad-mode", withLabel, withFinalCPU("50m"), withShrinkMode("bogus"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("bad-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec)
}

// --- New tests for startup delay ---

func TestInvalidStartupDelay_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("bad-delay", withLabel, withFinalCPU("50m"), withStartupDelay("not-a-duration"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("bad-delay"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec)
}

func TestNegativeStartupDelay_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("neg-delay", withLabel, withFinalCPU("50m"), withStartupDelay("-5s"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("neg-delay"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec)
}

// --- New tests for true shrink semantics ---

func TestFinalCPUEqualToCurrent_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("equal-cpu", withLabel, withFinalCPU("500m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("equal-cpu"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec) // ShrinkSkipped event
}

func TestFinalCPUGreaterThanCurrent_Refused(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("upscale", withLabel, withFinalCPU("1"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("upscale"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec) // ShrinkRefused event
}

func TestContainerMissingCPURequest_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("no-cpu-req", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	// Remove CPU request from the container
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("no-cpu-req"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec)
}

func TestContainerNilRequests_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("nil-req", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.Spec.Containers[0].Resources.Requests = nil

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("nil-req"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec)
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

func TestResolveShrinkMode(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		wantMode string
		wantOK   bool
	}{
		{
			name:     "no annotation defaults to started",
			pod:      basePod("a"),
			wantMode: ShrinkModeStarted,
			wantOK:   true,
		},
		{
			name:     "explicit started",
			pod:      basePod("b", withShrinkMode("started")),
			wantMode: ShrinkModeStarted,
			wantOK:   true,
		},
		{
			name:     "explicit ready",
			pod:      basePod("c", withShrinkMode("ready")),
			wantMode: ShrinkModeReady,
			wantOK:   true,
		},
		{
			name:   "invalid mode",
			pod:    basePod("d", withShrinkMode("bogus")),
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mode, ok := resolveShrinkMode(tc.pod)
			if ok != tc.wantOK {
				t.Fatalf("resolveShrinkMode() ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && mode != tc.wantMode {
				t.Errorf("resolveShrinkMode() = %q, want %q", mode, tc.wantMode)
			}
		})
	}
}

func TestMaybeParseStartupDelay(t *testing.T) {
	tests := []struct {
		name   string
		pod    *corev1.Pod
		wantOK bool
	}{
		{
			name:   "no annotation",
			pod:    basePod("a"),
			wantOK: true,
		},
		{
			name:   "valid duration",
			pod:    basePod("b", withStartupDelay("30s")),
			wantOK: true,
		},
		{
			name:   "invalid duration",
			pod:    basePod("c", withStartupDelay("nope")),
			wantOK: false,
		},
		{
			name:   "negative duration",
			pod:    basePod("d", withStartupDelay("-10s")),
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := maybeParseStartupDelay(tc.pod)
			if ok != tc.wantOK {
				t.Errorf("maybeParseStartupDelay() ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}

func TestIsContainerReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		target   string
		expected bool
	}{
		{
			name: "ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", Ready: true},
					},
				},
			},
			target:   "app",
			expected: true,
		},
		{
			name: "not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", Ready: false},
					},
				},
			},
			target:   "app",
			expected: false,
		},
		{
			name: "no status",
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
			got := isContainerReady(tc.pod, tc.target)
			if got != tc.expected {
				t.Errorf("isContainerReady() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestShouldShrinkNow(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		mode     string
		expected bool
	}{
		{
			name: "started mode, started",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", Started: boolPtr(true)},
					},
				},
			},
			mode:     ShrinkModeStarted,
			expected: true,
		},
		{
			name: "ready mode, not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", Started: boolPtr(true), Ready: false},
					},
				},
			},
			mode:     ShrinkModeReady,
			expected: false,
		},
		{
			name: "ready mode, ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", Started: boolPtr(true), Ready: true},
					},
				},
			},
			mode:     ShrinkModeReady,
			expected: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldShrinkNow(tc.pod, "app", tc.mode)
			if got != tc.expected {
				t.Errorf("shouldShrinkNow() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestGetCurrentCPURequest(t *testing.T) {
	tests := []struct {
		name   string
		pod    *corev1.Pod
		wantOK bool
	}{
		{
			name:   "has cpu",
			pod:    basePod("a"),
			wantOK: true,
		},
		{
			name: "nil requests",
			pod: func() *corev1.Pod {
				p := basePod("b")
				p.Spec.Containers[0].Resources.Requests = nil
				return p
			}(),
			wantOK: false,
		},
		{
			name: "no cpu key",
			pod: func() *corev1.Pod {
				p := basePod("c")
				p.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				}
				return p
			}(),
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := getCurrentCPURequest(tc.pod, 0)
			if ok != tc.wantOK {
				t.Errorf("getCurrentCPURequest() ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}
