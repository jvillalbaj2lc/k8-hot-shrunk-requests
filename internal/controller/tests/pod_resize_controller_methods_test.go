package controllertests

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/jvillalbaj2lc/k8-hot-shrunk-requests/internal/controller"
)

func TestPatchResize_UpdatesOnlyTargetContainerCPURequest(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("patch-resize", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name:  "sidecar",
		Image: "busybox",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
		},
	})

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	r := controller.NewPodResizeReconciler(c, s, record.NewFakeRecorder(10))

	var fetched corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "patch-resize"}, &fetched); err != nil {
		t.Fatalf("failed to fetch pod: %v", err)
	}

	if err := r.PatchResize(context.Background(), &fetched, 1, resource.MustParse("125m")); err != nil {
		t.Fatalf("PatchResize returned error: %v", err)
	}

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "patch-resize"}, &updated); err != nil {
		t.Fatalf("failed to fetch updated pod: %v", err)
	}

	sidecarCPU := updated.Spec.Containers[1].Resources.Requests[corev1.ResourceCPU]
	if got := sidecarCPU.String(); got != "125m" {
		t.Fatalf("unexpected sidecar cpu request: got %s want 125m", got)
	}
	mainCPU := updated.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	if got := mainCPU.String(); got != "500m" {
		t.Fatalf("main container cpu request should remain unchanged: got %s want 500m", got)
	}
}

func TestMarkProcessed_InitializesAnnotationsWhenNil(t *testing.T) {
	s := setupScheme(t)
	pod := basePod("mark-processed", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.Annotations = nil

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	r := controller.NewPodResizeReconciler(c, s, record.NewFakeRecorder(10))

	var fetched corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "mark-processed"}, &fetched); err != nil {
		t.Fatalf("failed to fetch pod: %v", err)
	}

	if err := r.MarkProcessed(context.Background(), &fetched); err != nil {
		t.Fatalf("MarkProcessed returned error: %v", err)
	}

	var updated corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "mark-processed"}, &updated); err != nil {
		t.Fatalf("failed to fetch updated pod: %v", err)
	}

	if updated.Annotations[controller.AnnotationShrunk] != "true" {
		t.Fatalf("expected %s annotation to be true", controller.AnnotationShrunk)
	}
}

func TestReconcilePodByName_BuildsExpectedRequest(t *testing.T) {
	req := controller.ReconcilePodByName("kube-system", "mypod")
	if req.Name != "mypod" {
		t.Fatalf("unexpected request name: got %q want %q", req.Name, "mypod")
	}
	if req.Namespace != "kube-system" {
		t.Fatalf("unexpected request namespace: got %q want %q", req.Namespace, "kube-system")
	}
}

func TestIsPodEligible_TerminatingPodIsNotEligible(t *testing.T) {
	now := metav1.Now()
	pod := basePod("terminating-eligibility", withLabel, withFinalCPU("50m"), withRunningAndStarted)
	pod.DeletionTimestamp = &now

	if controller.IsPodEligible(pod) {
		t.Fatal("expected pod with deletion timestamp to be ineligible")
	}
}
