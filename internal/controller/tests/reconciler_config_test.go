package controllertests

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/jvillalbaj2lc/k8-hot-shrunk-requests/internal/config"
	"github.com/jvillalbaj2lc/k8-hot-shrunk-requests/internal/controller"
)

// --- Namespace filtering tests ---

func TestReconcile_AllowedNamespaces_PodInAllowed_Proceeds(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("allowed-ns-pod", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.Namespace = "prod"

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode: "started",
			AllowedNamespaces: []string{"prod"},
		},
	}

	req := controller.ReconcilePodByName("prod", "allowed-ns-pod")
	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	// Pod should have been processed (shrunk annotation set).
	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "prod", Name: "allowed-ns-pod"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Error("expected pod in allowed namespace to be processed")
	}
}

func TestReconcile_AllowedNamespaces_PodNotInAllowed_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("not-allowed-ns-pod", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.Namespace = "staging"

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode: "started",
			AllowedNamespaces: []string{"prod"},
		},
	}

	req := controller.ReconcilePodByName("staging", "not-allowed-ns-pod")
	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	// Pod should NOT have been processed (no shrunk annotation).
	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "staging", Name: "not-allowed-ns-pod"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] == "true" {
		t.Error("expected pod in non-allowed namespace to be silently skipped")
	}
}

func TestReconcile_ExcludedNamespaces_PodInExcluded_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("excluded-ns-pod", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.Namespace = "kube-system"

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode:  "started",
			ExcludedNamespaces: []string{"kube-system"},
		},
	}

	req := controller.ReconcilePodByName("kube-system", "excluded-ns-pod")
	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "kube-system", Name: "excluded-ns-pod"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] == "true" {
		t.Error("expected pod in excluded namespace to be silently skipped")
	}
}

func TestReconcile_ExcludedNamespaces_PodNotInExcluded_Proceeds(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("not-excluded-ns-pod", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.Namespace = "default"

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode:  "started",
			ExcludedNamespaces: []string{"kube-system"},
		},
	}

	req := controller.ReconcilePodByName("default", "not-excluded-ns-pod")
	res, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "not-excluded-ns-pod"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Error("expected pod not in excluded namespace to be processed")
	}
}

// --- Excluded pods tests ---

func TestReconcile_ExcludedPod_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("excluded-pod", withLabel, withFinalCPU("50m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode: "started",
			ExcludedPods:      []string{"default/excluded-pod"},
		},
	}

	res, err := r.Reconcile(context.Background(), reconcileReq("excluded-pod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "excluded-pod"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] == "true" {
		t.Error("expected excluded pod to be silently skipped")
	}
}

func TestReconcile_NonExcludedPod_Proceeds(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("not-excluded", withLabel, withFinalCPU("50m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode: "started",
			ExcludedPods:      []string{"default/other-pod"},
		},
	}

	res, err := r.Reconcile(context.Background(), reconcileReq("not-excluded"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "not-excluded"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Error("expected non-excluded pod to be processed")
	}
}

// --- Excluded containers tests ---

func TestReconcile_ExcludedContainer_Skipped(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("excl-container", withLabel, withFinalCPU("50m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode:  "started",
			ExcludedContainers: []string{"app"}, // "app" is the default container name in basePod
		},
	}

	res, err := r.Reconcile(context.Background(), reconcileReq("excl-container"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "excl-container"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] == "true" {
		t.Error("expected pod with excluded container to be silently skipped")
	}
}

func TestReconcile_NonExcludedContainer_Proceeds(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("not-excl-container", withLabel, withFinalCPU("50m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode:  "started",
			ExcludedContainers: []string{"sidecar"}, // doesn't match "app"
		},
	}

	res, err := r.Reconcile(context.Background(), reconcileReq("not-excl-container"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "not-excl-container"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Error("expected pod with non-excluded container to be processed")
	}
}

// --- Config default shrink mode tests ---

func TestReconcile_ConfigDefaultShrinkModeReady_NoAnnotation_UsesReady(t *testing.T) {
	s := setupScheme(t)
	// Pod without shrink-mode annotation, but started (not ready).
	// Config default is "ready" so it should requeue.
	pod := basePod("cfg-ready-mode", withLabel, withFinalCPU("50m"), withRunningStartedNotReady)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode: "ready",
		},
	}

	res, err := r.Reconcile(context.Background(), reconcileReq("cfg-ready-mode"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With config defaultShrinkMode=ready and container not ready, should requeue.
	expectRequeue(t, res)
}

func TestReconcile_ConfigDefaultShrinkModeReady_PodAnnotationOverrides(t *testing.T) {
	s := setupScheme(t)
	// Pod with explicit "started" annotation and container is started (not ready).
	// Config default is "ready" but annotation overrides → should shrink.
	pod := basePod("ann-overrides-cfg", withLabel, withFinalCPU("50m"), withShrinkMode("started"), withRunningStartedNotReady)
	// Need to add Started=true to container statuses
	pod.Status.ContainerStatuses[0].Started = boolPtr(true)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode: "ready",
		},
	}

	res, err := r.Reconcile(context.Background(), reconcileReq("ann-overrides-cfg"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Pod annotation says "started" which is met → should proceed (no requeue).
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ann-overrides-cfg"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Error("expected pod annotation to override config default shrink mode")
	}
}

// --- Config default startup delay tests ---

func TestReconcile_ConfigDefaultStartupDelay_NoAnnotation_UsesConfigDelay(t *testing.T) {
	s := setupScheme(t)
	// Pod without startup-delay annotation, container just started.
	// Config sets a large default delay so it should requeue.
	pod := basePod("cfg-delay", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	// The container started just now in the status — ContainerConditionAge will be ~0.

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode:   "started",
			DefaultStartupDelay: 10 * time.Minute,
		},
	}

	res, err := r.Reconcile(context.Background(), reconcileReq("cfg-delay"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With 10min default delay and container just started, should requeue.
	expectRequeue(t, res)
}

func TestReconcile_ConfigDefaultStartupDelay_AnnotationOverrides(t *testing.T) {
	s := setupScheme(t)
	// Pod with explicit "0s" startup-delay annotation.
	// Config sets 10m default, but annotation overrides to 0s → should proceed.
	pod := basePod("ann-delay-override", withLabel, withFinalCPU("50m"), withStartupDelay("0s"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := &controller.PodResizeReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: rec,
		Config: &config.ControllerConfig{
			DefaultShrinkMode:   "started",
			DefaultStartupDelay: 10 * time.Minute,
		},
	}

	res, err := r.Reconcile(context.Background(), reconcileReq("ann-delay-override"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Annotation says 0s so no delay → should proceed.
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ann-delay-override"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Error("expected startup-delay annotation to override config default")
	}
}

// --- Nil config fallback tests ---

func TestReconcile_NilConfig_UsesDefaults(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("nil-config", withLabel, withFinalCPU("50m"), withRunningAndStarted)

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).WithStatusSubresource(&corev1.Pod{}).Build()
	rec := newFakeRecorder()
	r := controller.NewPodResizeReconciler(c, s, rec) // Config is nil

	res, err := r.Reconcile(context.Background(), reconcileReq("nil-config"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectNoRequeue(t, res)

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "nil-config"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Error("expected nil config to fall back to defaults and process pod")
	}
}

// --- Silent skip verification (no events emitted) ---

func TestReconcile_SilentSkips_NoEvents(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.ControllerConfig
		pod  *corev1.Pod
		ns   string
	}{
		{
			name: "allowed namespace filter",
			cfg: &config.ControllerConfig{
				DefaultShrinkMode: "started",
				AllowedNamespaces: []string{"prod"},
			},
			pod: func() *corev1.Pod {
				p := basePod("silent-allowed", withLabel, withFinalCPU("50m"), withRunningAndStarted)
				p.Namespace = "staging"
				return p
			}(),
			ns: "staging",
		},
		{
			name: "excluded namespace filter",
			cfg: &config.ControllerConfig{
				DefaultShrinkMode:  "started",
				ExcludedNamespaces: []string{"kube-system"},
			},
			pod: func() *corev1.Pod {
				p := basePod("silent-excluded-ns", withLabel, withFinalCPU("50m"), withRunningAndStarted)
				p.Namespace = "kube-system"
				return p
			}(),
			ns: "kube-system",
		},
		{
			name: "excluded pod",
			cfg: &config.ControllerConfig{
				DefaultShrinkMode: "started",
				ExcludedPods:      []string{"default/silent-excluded-pod"},
			},
			pod: basePod("silent-excluded-pod", withLabel, withFinalCPU("50m"), withRunningAndStarted),
			ns:  "default",
		},
		{
			name: "excluded container",
			cfg: &config.ControllerConfig{
				DefaultShrinkMode:  "started",
				ExcludedContainers: []string{"app"},
			},
			pod: basePod("silent-excluded-container", withLabel, withFinalCPU("50m"), withRunningAndStarted),
			ns:  "default",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := setupScheme(t)
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(tc.pod).Build()
			rec := newFakeRecorder()
			r := &controller.PodResizeReconciler{
				Client:   c,
				Scheme:   s,
				Recorder: rec,
				Config:   tc.cfg,
			}

			req := controller.ReconcilePodByName(tc.ns, tc.pod.Name)
			res, err := r.Reconcile(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			expectNoRequeue(t, res)

			// Verify no events were emitted (silent skip).
			select {
			case event := <-rec.Events:
				t.Errorf("expected no events for silent skip, got: %s", event)
			default:
				// Good — no events.
			}
		})
	}
}
