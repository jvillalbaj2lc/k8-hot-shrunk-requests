package controllertests

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/jvillalbaj2lc/k8-hot-shrunk-requests/internal/controller"
)

// --- Reconciler integration tests ---

func TestPodWithoutLabel_Ignored(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("no-label", withRunningAndStarted, withFinalCPU("50m"))

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("ready-pod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ready-pod"}, &updated); err != nil {
		t.Fatalf("failed to get updated pod: %v", err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("terminating"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
}

// --- Tests for shrink mode ---

func TestDefaultMode_Started(t *testing.T) {
	s := setupScheme(t)
	// No shrink-mode annotation → defaults to "started"
	pod := basePod("default-mode", withLabel, withFinalCPU("50m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := controller.NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("default-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "default-mode"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Error("expected shrunk annotation after default mode shrink")
	}
}

func TestModeReady_ContainerReady_Shrinks(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("ready-mode", withLabel, withFinalCPU("50m"), withShrinkMode("ready"), withRunningAndReady)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := controller.NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("ready-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ready-mode"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("bad-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec)
}

// --- Tests for startup delay ---

func TestInvalidStartupDelay_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("bad-delay", withLabel, withFinalCPU("50m"), withStartupDelay("not-a-duration"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

	res, err := r.Reconcile(context.Background(), reconcileReq("neg-delay"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)
	expectEvent(t, rec)
}

// --- Tests for true shrink semantics ---

func TestFinalCPUEqualToCurrent_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("equal-cpu", withLabel, withFinalCPU("500m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
	r := controller.NewPodResizeReconciler(c, s, rec)

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
			got := controller.IsPodEligible(tc.pod)
			if got != tc.expected {
				t.Errorf("IsPodEligible() = %v, want %v", got, tc.expected)
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
			idx, err := controller.ResolveTargetContainer(tc.pod)
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
			got := controller.IsStartupComplete(tc.pod, tc.target)
			if got != tc.expected {
				t.Errorf("IsStartupComplete() = %v, want %v", got, tc.expected)
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
			wantMode: controller.ShrinkModeStarted,
			wantOK:   true,
		},
		{
			name:     "explicit started",
			pod:      basePod("b", withShrinkMode("started")),
			wantMode: controller.ShrinkModeStarted,
			wantOK:   true,
		},
		{
			name:     "explicit ready",
			pod:      basePod("c", withShrinkMode("ready")),
			wantMode: controller.ShrinkModeReady,
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
			mode, ok := controller.ResolveShrinkMode(tc.pod)
			if ok != tc.wantOK {
				t.Fatalf("ResolveShrinkMode() ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && mode != tc.wantMode {
				t.Errorf("ResolveShrinkMode() = %q, want %q", mode, tc.wantMode)
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
			_, ok := controller.MaybeParseStartupDelay(tc.pod)
			if ok != tc.wantOK {
				t.Errorf("MaybeParseStartupDelay() ok = %v, want %v", ok, tc.wantOK)
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
			got := controller.IsContainerReady(tc.pod, tc.target)
			if got != tc.expected {
				t.Errorf("IsContainerReady() = %v, want %v", got, tc.expected)
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
			mode:     controller.ShrinkModeStarted,
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
			mode:     controller.ShrinkModeReady,
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
			mode:     controller.ShrinkModeReady,
			expected: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := controller.ShouldShrinkNow(tc.pod, "app", tc.mode)
			if got != tc.expected {
				t.Errorf("ShouldShrinkNow() = %v, want %v", got, tc.expected)
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
			_, ok := controller.GetCurrentCPURequest(tc.pod, 0)
			if ok != tc.wantOK {
				t.Errorf("GetCurrentCPURequest() ok = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}
