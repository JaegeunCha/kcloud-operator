// ============================================================
// state_machine_drain_gate_test.go: drain 게이트의 장치 점유 판정 단위 테스트
// 상세: 드라이버 교체 전 노드를 비우는 판정이 등록된 네 벤더·containers 와 initContainers·Pending 단계를 모두 보는지,
//   hasDeviceWorkloads 와 evictDevicePods 가 같은 집합을 보는지, 그리고 다른 노드의 장치 Pod·종료 단계·장치를 쓰지
//   않는 Pod 만 있는 음성 사례를 고정한다. 이 라인에는 resource.k8s.io 가 없어 DRA 경로는 시험하지 않는다.
//   fake client 기반 — envtest 불필요.
// 생성일: 2026-10-05 | 수정일: 2026-11-16
// ============================================================

package upgrade

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "kcloud-operator/api/v1alpha1"
)

const (
	drainGateNode   = "n-drain"
	drainGateVendor = "nvidia"
)

// makeDrainingDUS 는 Draining 상태의 DUS 를 반환한다(LastTransitionTime=now 로 drain 타임아웃 회피).
func makeDrainingDUS() *v1alpha1.DriverUpgradeState {
	return &v1alpha1.DriverUpgradeState{
		ObjectMeta: metav1.ObjectMeta{Name: drainGateNode + "-" + drainGateVendor},
		Spec: v1alpha1.DriverUpgradeStateSpec{
			NodeName: drainGateNode,
			Vendor:   drainGateVendor,
		},
		Status: v1alpha1.DriverUpgradeStateStatus{
			State:              v1alpha1.UpgradeStateDraining,
			LastTransitionTime: metav1.Now(),
		},
	}
}

// makeDrainEnabledDIP 는 drain 활성화 DIP 를 반환한다(ForceUpgrade=false, RollbackOnFailure=false).
func makeDrainEnabledDIP() *v1alpha1.DriverInstallPolicy {
	return &v1alpha1.DriverInstallPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: drainGateVendor},
		Spec: v1alpha1.DriverInstallPolicySpec{
			Vendor:        drainGateVendor,
			UpgradePolicy: &v1alpha1.UpgradePolicy{DrainEnabled: true},
		},
	}
}

// makeDevicePod 는 지정 단계의 Pod 를 반환한다. limits 는 호출자가 컨테이너에 붙인다.
func makeDevicePod(name string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName:   drainGateNode,
			Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// deviceLimits 는 가속기 extended resource 1개를 요청하는 ResourceList 다.
func deviceLimits(resName string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceName(resName): resource.MustParse("1")}
}

// assertStaysDraining 은 handleDraining 이 Upgrading 으로 넘어가지 않고 재조정을 예약하는지 본다.
func assertStaysDraining(t *testing.T, sm *UpgradeStateMachine, dus *v1alpha1.DriverUpgradeState,
	dip *v1alpha1.DriverInstallPolicy) {
	t.Helper()
	requeue, after, err := sm.handleDraining(context.Background(), dus, dip)
	if err != nil {
		t.Fatalf("handleDraining 실패: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStateDraining {
		t.Fatalf("장치를 쥔 Pod 가 있는데 Draining 을 벗어났다: got=%s", dus.Status.State)
	}
	if !requeue || after == 0 {
		t.Fatalf("drain 미완인데 재조정 예약이 없다: requeue=%v after=%v", requeue, after)
	}
}

// assertDrainCompletes 는 handleDraining 이 Upgrading 으로 넘어가는지 본다.
func assertDrainCompletes(t *testing.T, pod *corev1.Pod, why string) {
	t.Helper()
	dus, dip := makeDrainingDUS(), makeDrainEnabledDIP()
	sm := newUpgradeSMWithRecorder(dus, dip, pod)
	if _, _, err := sm.handleDraining(context.Background(), dus, dip); err != nil {
		t.Fatalf("handleDraining 실패: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStateUpgrading {
		t.Fatalf("%s: drain 이 끝나지 않았다: got=%s", why, dus.Status.State)
	}
}

// ── 시험 1: rebellions ATOM 을 쥔 Pod ────────────────────────────────
// 판정 목록에서 rebellions.ai/ 가 빠지면 이 Pod 는 drain 에서 보이지 않고 그 밑에서 커널 모듈이
// 교체된다(과거 결함 재발 지점).
func TestHandleDrainingRebellionsDevicePodBlocksUpgrading(t *testing.T) {
	pod := makeDevicePod("rbln-pod", corev1.PodRunning)
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{Limits: deviceLimits("rebellions.ai/ATOM")}

	dus, dip := makeDrainingDUS(), makeDrainEnabledDIP()
	assertStaysDraining(t, newUpgradeSMWithRecorder(dus, dip, pod), dus, dip)
}

// ── 시험 2: initContainer 만 장치를 요청하는 Pod ─────────────────────
// initContainer 가 장치를 쥐는 동안에도 커널 모듈은 사용 중이다.
func TestHandleDrainingInitContainerDevicePodBlocksUpgrading(t *testing.T) {
	pod := makeDevicePod("init-pod", corev1.PodRunning)
	pod.Spec.InitContainers = []corev1.Container{{
		Name: "warmup", Image: "busybox",
		Resources: corev1.ResourceRequirements{Limits: deviceLimits("nvidia.com/gpu")},
	}}

	dus, dip := makeDrainingDUS(), makeDrainEnabledDIP()
	assertStaysDraining(t, newUpgradeSMWithRecorder(dus, dip, pod), dus, dip)
}

// ── 시험 3: Pending 단계의 장치 요청 Pod ─────────────────────────────
// 스케줄만 되고 아직 시작되지 않은 Pod 는 교체 도중에 시작된다. 종료 단계만 제외한다.
func TestHandleDrainingPendingDevicePodBlocksUpgrading(t *testing.T) {
	pod := makeDevicePod("pending-pod", corev1.PodPending)
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{Limits: deviceLimits("nvidia.com/gpu")}

	dus, dip := makeDrainingDUS(), makeDrainEnabledDIP()
	assertStaysDraining(t, newUpgradeSMWithRecorder(dus, dip, pod), dus, dip)
}

// ── 시험 3-B: Pending Pod 가 eviction 대상에 들어간다 ────────────────
// hasDeviceWorkloads 가 Pending 을 세기 시작하면 evictDevicePods 도 같은 집합을 봐야 한다.
// 아니면 Draining 이 참인 채 eviction 이 그 Pod 를 건너뛰어 drain 타임아웃까지 머문다.
func TestEvictDevicePodsIncludesPendingPod(t *testing.T) {
	pod := makeDevicePod("pending-evict", corev1.PodPending)
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{Limits: deviceLimits("nvidia.com/gpu")}

	dus := makeDrainingDUS()
	sm := newUpgradeSMWithRecorder(dus, pod)
	if err := sm.evictDevicePods(context.Background(), dus, false); err != nil {
		t.Fatalf("evictDevicePods 실패: %v", err)
	}

	var got corev1.Pod
	err := sm.Get(context.Background(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Pending 장치 Pod 가 eviction 대상에서 빠졌다: get err=%v", err)
	}
}

// ── 시험 3-C: initContainer 장치 Pod 가 eviction 대상에 들어간다 ─────
// hasDeviceWorkloads 가 막기만 하고 evictDevicePods 가 그 Pod 를 집지 않으면
// Draining 이 참인 채 drain 타임아웃까지 머문다.
func TestEvictDevicePodsIncludesInitContainerDevicePod(t *testing.T) {
	pod := makeDevicePod("init-evict", corev1.PodRunning)
	pod.Spec.InitContainers = []corev1.Container{{
		Name: "warmup", Image: "busybox",
		Resources: corev1.ResourceRequirements{Limits: deviceLimits("rebellions.ai/ATOM")},
	}}

	dus := makeDrainingDUS()
	sm := newUpgradeSMWithRecorder(dus, pod)
	if err := sm.evictDevicePods(context.Background(), dus, false); err != nil {
		t.Fatalf("evictDevicePods 실패: %v", err)
	}

	var got corev1.Pod
	err := sm.Get(context.Background(), types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("initContainer 장치 Pod 가 eviction 대상에서 빠졌다: get err=%v", err)
	}
}

// ── 시험 4: 종료 단계 Pod 는 drain 을 막지 않는다 ────────────────────
// Pending 을 포함시키는 변경이 "모든 단계를 센다" 로 번지지 않았는지 본다.
// podPhaseDrainable 이 제외하는 단계는 Succeeded·Failed 둘이므로 둘 다 본다.
func TestHandleDrainingTerminalDevicePodAllowsUpgrading(t *testing.T) {
	for _, phase := range []corev1.PodPhase{corev1.PodSucceeded, corev1.PodFailed} {
		pod := makeDevicePod("done-pod", phase)
		pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{Limits: deviceLimits("nvidia.com/gpu")}
		assertDrainCompletes(t, pod, string(phase)+" 단계 Pod 만 남았다")
	}
}

// ── 시험 5: 다른 노드의 장치 Pod 는 이 노드의 drain 을 막지 않는다 ──────
// 판정이 노드 이름을 보지 않고 "장치를 쥔 Pod 가 클러스터에 있는가" 로 느슨해지면
// 관련 없는 노드의 Pod 하나가 모든 노드의 drain 을 영원히 막는다.
func TestHandleDrainingOtherNodeDevicePodDoesNotBlockUpgrading(t *testing.T) {
	pod := makeDevicePod("other-node-pod", corev1.PodRunning)
	pod.Spec.NodeName = "n-other"
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{Limits: deviceLimits("nvidia.com/gpu")}
	assertDrainCompletes(t, pod, "다른 노드의 장치 Pod")
}

// ── 시험 6: 장치를 쓰지 않는 Running Pod 만 있으면 drain 이 끝난다 ──────
// 앞의 시험들은 장치 Pod 가 있을 때 막히는지만 본다 — 판정 함수가 무조건 true 를
// 반환해도 전부 통과한다. 장치와 무관한 Pod 만 있을 때 실제로 Upgrading 으로
// 넘어가는지를 봐야 판정이 상수 true 로 퇴화하는 것을 잡아낸다.
func TestHandleDrainingNoDevicePodAllowsUpgrading(t *testing.T) {
	assertDrainCompletes(t, makeDevicePod("plain-pod", corev1.PodRunning), "장치를 쓰지 않는 Pod 만 남았다")
}

// ── 시험 7: 확장 리소스 판정 범위가 좁아지지 않았다 ──────────────────
// 판정은 intent.AcceleratorResourceDomains 에 부분 일치를 걸어 벤더를 가리지 않는다.
// 벤더별 정확 일치·접두 일치로 바꾸면 device-plugin 변형 이름(gpu.shared 등)과
// 카탈로그 밖 제품명이 조용히 빠진다.
func TestPodUsesDeviceKeepsVendorAgnosticSubstringMatch(t *testing.T) {
	caught := []string{
		"nvidia.com/gpu",
		"nvidia.com/mig-1g.5gb",
		"nvidia.com/gpu.shared",
		"furiosa.ai/rngd",
		"beta.furiosa.ai/npu",
		"rebellions.ai/ATOM",
		"rebellions.ai/ATOM-plus",
		"tenstorrent.com/blackhole",
		"tenstorrent.com/wormhole",
	}
	for _, rn := range caught {
		pod := makeDevicePod("p", corev1.PodRunning)
		pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{Limits: deviceLimits(rn)}
		if !podUsesDevice(pod) {
			t.Errorf("가속기 리소스를 놓쳤다: %s", rn)
		}
	}

	ignored := []string{"cpu", "memory", "example.com/fpga"}
	for _, rn := range ignored {
		pod := makeDevicePod("p", corev1.PodRunning)
		pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{Limits: deviceLimits(rn)}
		if podUsesDevice(pod) {
			t.Errorf("가속기가 아닌 리소스를 장치 점유로 봤다: %s", rn)
		}
	}
}
