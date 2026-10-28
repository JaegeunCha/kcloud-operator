// ============================================================
// preflight_job_test.go: PreFlight 설치 전 점검 Job 실행·판정 시험
// 상세: fake client 로 점검 Job 생성·대기·통과·hold 충돌·판정 불가(옛 이미지)·낡은 Job 재생성을 본다.
//       hold 충돌이면 노드를 cordon 하지 않고 다른 사이클의 노드 라벨도 지우지 않은 채 Failed 로 가는지 단정한다.
// 생성일: 2026-10-27 | 수정일: 2026-10-28
// ============================================================

package upgrade

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "kcloud-operator/api/v1alpha1"
	"kcloud-operator/internal/driverjob"
	"kcloud-operator/internal/naming"
)

const holdConflictMsg = "hold 된 패키지(libx11-6)가 드라이버 설치를 막는다 — libx11-xcb1 : Depends: libx11-6 " +
	"(= 2:1.7.5-1ubuntu0.4) but 2:1.7.5-1ubuntu0.3 is to be installed"

// makeJobPod 는 job 소유 파드 하나다. lastState 면 직전 종료 기록(OnFailure 재시작 전)에 둔다.
func makeJobPod(jobName, container string, exitCode int32, reason, message string, lastState bool) *corev1.Pod {
	term := &corev1.ContainerStateTerminated{ExitCode: exitCode, Reason: reason, Message: message,
		FinishedAt: metav1.Now()}
	cs := corev1.ContainerStatus{Name: container}
	if lastState {
		cs.LastTerminationState.Terminated = term
		cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}
	} else {
		cs.State.Terminated = term
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: jobName + "-p", Namespace: driverjob.Namespace,
			Labels: map[string]string{batchv1.JobNameLabel: jobName}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

// markJob 은 Job status 를 완료 또는 실패로 둔다.
func markJob(j *batchv1.Job, complete, failed bool) *batchv1.Job {
	if complete {
		j.Status.Succeeded = 1
		j.Status.Conditions = append(j.Status.Conditions,
			batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue})
	}
	if failed {
		j.Status.Failed = 1
		j.Status.Conditions = append(j.Status.Conditions,
			batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue})
	}
	return j
}

func preflightKey() types.NamespacedName {
	return types.NamespacedName{Name: naming.PreflightJobName(jVendor, jModel, jNode), Namespace: driverjob.Namespace}
}

func preflightGone(t *testing.T, sm *UpgradeStateMachine) bool {
	t.Helper()
	err := sm.Get(context.Background(), preflightKey(), &batchv1.Job{})
	return apierrors.IsNotFound(err)
}

func TestPreflightApplicable(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v1alpha1.DriverInstallPolicy)
		want   bool
	}{
		{"nvidia job apt", func(*v1alpha1.DriverInstallPolicy) {}, true},
		{"daemonset", func(p *v1alpha1.DriverInstallPolicy) { p.Spec.Driver.Mode = "" }, false},
		{"ngc", func(p *v1alpha1.DriverInstallPolicy) { p.Spec.Driver.Installer = v1alpha1.DriverInstallerNGC }, false},
		{"installer empty", func(p *v1alpha1.DriverInstallPolicy) { p.Spec.Driver.Installer = "" }, false},
		{"other vendor", func(p *v1alpha1.DriverInstallPolicy) { p.Spec.Vendor = "tenstorrent" }, false},
		{"trackOnly", func(p *v1alpha1.DriverInstallPolicy) { p.Spec.Driver.TrackOnly = true }, false},
	}
	for _, c := range cases {
		p := makeAptJobDIP(verNew)
		c.mutate(p)
		if got := preflightApplicable(p); got != c.want {
			t.Errorf("%s: preflightApplicable=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestHandlePreFlight_NotApplicable_GoesToCordoning(t *testing.T) {
	dus := makeJobDUS(v1alpha1.UpgradeStatePreFlight, "", verNew)
	dip := makeJobDIP(verNew, imgNew, true, true) // ngc — 점검 대상 아님
	sm := newUpgradeSMWithRecorder(dus, dip, readyNode(true))
	if _, _, err := sm.handlePreFlight(context.Background(), dus, dip); err != nil {
		t.Fatalf("handlePreFlight: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStateCordoning || !preflightGone(t, sm) {
		t.Errorf("state=%s preflightGone=%v, want Cordoning·true", dus.Status.State, preflightGone(t, sm))
	}
}

func TestHandlePreFlight_CreatesPreflightJob(t *testing.T) {
	dus := makeJobDUS(v1alpha1.UpgradeStatePreFlight, "", verNew)
	dip := makeAptJobDIP(verNew)
	sm := newUpgradeSMWithRecorder(dus, dip, readyNode(true))
	requeue, after, err := sm.handlePreFlight(context.Background(), dus, dip)
	if err != nil || !requeue || after != 10*time.Second || dus.Status.State != v1alpha1.UpgradeStatePreFlight {
		t.Fatalf("err=%v requeue=%v after=%s state=%s", err, requeue, after, dus.Status.State)
	}
	var job batchv1.Job
	if err := sm.Get(context.Background(), preflightKey(), &job); err != nil {
		t.Fatalf("점검 Job 없음: %v", err)
	}
	c := job.Spec.Template.Spec.Containers[0]
	if c.Name != driverjob.PreflightContainerName ||
		!reflect.DeepEqual(c.Command, []string{"/usr/local/bin/preflight.sh"}) {
		t.Errorf("container=%s command=%v", c.Name, c.Command)
	}
	if !hasEvent(drainEvents(sm), "DriverPreflightStarted") {
		t.Error("DriverPreflightStarted 이벤트 없음")
	}
}

func TestHandlePreFlight_PreflightRunning_Waits(t *testing.T) {
	dus := makeJobDUS(v1alpha1.UpgradeStatePreFlight, "", verNew)
	dip := makeAptJobDIP(verNew)
	running := driverjob.RenderPreflightJob(dip, jNode, imgNew, verNew)
	sm := newUpgradeSMWithRecorder(dus, dip, readyNode(true), running)
	if _, _, err := sm.handlePreFlight(context.Background(), dus, dip); err != nil {
		t.Fatalf("handlePreFlight: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStatePreFlight || preflightGone(t, sm) {
		t.Errorf("state=%s gone=%v, want PreFlight·false", dus.Status.State, preflightGone(t, sm))
	}
}

func TestHandlePreFlight_PreflightPassed_Cordoning(t *testing.T) {
	dus := makeJobDUS(v1alpha1.UpgradeStatePreFlight, "", verNew)
	dip := makeAptJobDIP(verNew)
	done := markJob(driverjob.RenderPreflightJob(dip, jNode, imgNew, verNew), true, false)
	sm := newUpgradeSMWithRecorder(dus, dip, readyNode(true), done)
	if _, _, err := sm.handlePreFlight(context.Background(), dus, dip); err != nil {
		t.Fatalf("handlePreFlight: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStateCordoning {
		t.Errorf("state=%s, want Cordoning", dus.Status.State)
	}
	if !preflightGone(t, sm) {
		t.Error("결과를 읽은 점검 Job 이 남았다 — 다음 사이클이 지난 결과를 다시 읽는다")
	}
	if !hasEvent(drainEvents(sm), "DriverPreflightPassed") {
		t.Error("DriverPreflightPassed 이벤트 없음")
	}
}

func TestHandlePreFlight_HoldConflict_FailedWithoutCordon(t *testing.T) {
	dus := makeJobDUS(v1alpha1.UpgradeStatePreFlight, "", verNew)
	dip := makeAptJobDIP(verNew)
	failed := markJob(driverjob.RenderPreflightJob(dip, jNode, imgNew, verNew), false, true)
	pod := makeJobPod(failed.Name, driverjob.PreflightContainerName, 3, "Error", holdConflictMsg, false)
	// 같은 노드에서 다른 사이클이 단 업그레이드 라벨이다. 점검 차단은 cordon 전이라 이 사이클의 라벨이 아니다.
	busy := readyNode(true)
	busy.Labels = map[string]string{
		driverUpgradingLabelKey:         labelValueTrue,
		driverUpgradingBlockingLabelKey: labelValueTrue,
	}
	sm := newUpgradeSMWithRecorder(dus, dip, busy, failed, pod)
	if _, _, err := sm.handlePreFlight(context.Background(), dus, dip); err != nil {
		t.Fatalf("handlePreFlight: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStateFailed {
		t.Fatalf("state=%s, want Failed", dus.Status.State)
	}
	if !strings.HasPrefix(dus.Status.Message, "설치 전 점검 실패(노드 cordon 하지 않음): "+holdConflictMsg) ||
		!strings.Contains(dus.Status.Message, "kubectl delete dus "+jDUSNm) {
		t.Errorf("message=%q", dus.Status.Message)
	}
	var node corev1.Node
	if err := sm.Get(context.Background(), types.NamespacedName{Name: jNode}, &node); err != nil ||
		node.Spec.Unschedulable {
		t.Errorf("노드가 cordon 됐다(err=%v)", err)
	}
	for _, key := range []string{driverUpgradingLabelKey, driverUpgradingBlockingLabelKey} {
		if node.Labels[key] != labelValueTrue {
			t.Errorf("점검 차단이 다른 사이클의 라벨 %s 를 지웠다(labels=%v)", key, node.Labels)
		}
	}
	install := types.NamespacedName{Name: naming.InstallJobName(jVendor, jModel, jNode), Namespace: driverjob.Namespace}
	if err := sm.Get(context.Background(), install, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Errorf("install Job 이 생겼다(err=%v)", err)
	}
	if !preflightGone(t, sm) || !hasEvent(drainEvents(sm), "DriverPreflightBlocked") {
		t.Error("점검 Job 미삭제 또는 DriverPreflightBlocked 이벤트 없음")
	}
}

func TestHandlePreFlight_OldImageStartError_Proceeds(t *testing.T) {
	dus := makeJobDUS(v1alpha1.UpgradeStatePreFlight, "", verNew)
	dip := makeAptJobDIP(verNew)
	failed := markJob(driverjob.RenderPreflightJob(dip, jNode, imgNew, verNew), false, true)
	pod := makeJobPod(failed.Name, driverjob.PreflightContainerName, 128, "StartError",
		`exec: "/usr/local/bin/preflight.sh": stat /usr/local/bin/preflight.sh: no such file or directory`, false)
	sm := newUpgradeSMWithRecorder(dus, dip, readyNode(true), failed, pod)
	if _, _, err := sm.handlePreFlight(context.Background(), dus, dip); err != nil {
		t.Fatalf("handlePreFlight: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStateCordoning {
		t.Errorf("state=%s, want Cordoning — 옛 설치기 이미지가 설치를 막았다", dus.Status.State)
	}
	if !hasEvent(drainEvents(sm), "DriverPreflightInconclusive") {
		t.Error("DriverPreflightInconclusive 이벤트 없음")
	}
}

func TestHandlePreFlight_FailedWithoutPod_Proceeds(t *testing.T) {
	dus := makeJobDUS(v1alpha1.UpgradeStatePreFlight, "", verNew)
	dip := makeAptJobDIP(verNew)
	failed := markJob(driverjob.RenderPreflightJob(dip, jNode, imgNew, verNew), false, true)
	// 파드가 정리돼 종료 기록이 없다 — Job 의 Failed 조건 사유가 유일한 단서다.
	failed.Status.Conditions[0].Reason = "DeadlineExceeded"
	failed.Status.Conditions[0].Message = "Job was active longer than specified deadline"
	sm := newUpgradeSMWithRecorder(dus, dip, readyNode(true), failed)
	if _, _, err := sm.handlePreFlight(context.Background(), dus, dip); err != nil {
		t.Fatalf("handlePreFlight: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStateCordoning {
		t.Errorf("state=%s, want Cordoning", dus.Status.State)
	}
	var inconclusive string
	for _, e := range drainEvents(sm) {
		if strings.Contains(e, "DriverPreflightInconclusive") {
			inconclusive = e
		}
	}
	if !strings.Contains(inconclusive, "DeadlineExceeded") || !strings.Contains(inconclusive, "specified deadline") ||
		strings.Contains(inconclusive, "exit=") {
		t.Errorf("판정 불가 이벤트=%q, want Job 실패 사유 포함·exit= 없음(종료 코드 0 은 통과처럼 읽힌다)", inconclusive)
	}
}

func TestJobFailedReason(t *testing.T) {
	const none = "기록 없음"
	failedJob := func(status corev1.ConditionStatus, reason, msg string) *batchv1.Job {
		return &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobFailed, Status: status, Reason: reason, Message: msg}}}}
	}
	cases := []struct {
		name string
		job  *batchv1.Job
		want string
	}{
		{"reason+message", failedJob(corev1.ConditionTrue, "DeadlineExceeded", "over"), "DeadlineExceeded: over"},
		{"reason only", failedJob(corev1.ConditionTrue, "BackoffLimitExceeded", ""), "BackoffLimitExceeded"},
		{"reason empty", failedJob(corev1.ConditionTrue, "", "over"), none},
		{"condition false", failedJob(corev1.ConditionFalse, "DeadlineExceeded", "over"), none},
		{"no condition", &batchv1.Job{}, none},
	}
	for _, c := range cases {
		if got := jobFailedReason(c.job); got != c.want {
			t.Errorf("%s: jobFailedReason=%q, want %q", c.name, got, c.want)
		}
	}
}

func TestHandlePreFlight_StalePreflightJob_Recreated(t *testing.T) {
	dus := makeJobDUS(v1alpha1.UpgradeStatePreFlight, "", verNew)
	dip := makeAptJobDIP(verNew)
	stale := markJob(driverjob.RenderPreflightJob(dip, jNode, imgNew, verOld), true, false)
	sm := newUpgradeSMWithRecorder(dus, dip, readyNode(true), stale)
	if _, _, err := sm.handlePreFlight(context.Background(), dus, dip); err != nil {
		t.Fatalf("1차 handlePreFlight: %v", err)
	}
	if dus.Status.State != v1alpha1.UpgradeStatePreFlight || !preflightGone(t, sm) {
		t.Fatalf("state=%s gone=%v — 다른 버전의 지난 결과를 썼다", dus.Status.State, preflightGone(t, sm))
	}
	if _, _, err := sm.handlePreFlight(context.Background(), dus, dip); err != nil {
		t.Fatalf("2차 handlePreFlight: %v", err)
	}
	var job batchv1.Job
	if err := sm.Get(context.Background(), preflightKey(), &job); err != nil || jobDriverVersion(&job) != verNew {
		t.Errorf("새 점검 Job: err=%v version=%q", err, jobDriverVersion(&job))
	}
}

func TestJobTermination_PicksLatest(t *testing.T) {
	job := makeInstallJob(false, false)
	older := metav1.NewTime(time.Now().Add(-2 * time.Minute))
	newer := metav1.NewTime(time.Now().Add(-1 * time.Minute))
	pod := makeJobPod(job.Name, driverjob.InstallContainerName, 3, "Error", "old", true)
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt = older
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		ExitCode: 1, Reason: "Error", Message: "new", FinishedAt: newer}}
	sm := newUpgradeSMWithRecorder(job, pod)
	got, err := sm.jobTermination(context.Background(), job, driverjob.InstallContainerName)
	if err != nil || !got.found || got.exitCode != 1 || got.message != "new" {
		t.Errorf("got=%+v err=%v, want 최근 종료(exit 1, new)", got, err)
	}
	none, err := newUpgradeSMWithRecorder(job).jobTermination(context.Background(), job, driverjob.InstallContainerName)
	if err != nil || none.found {
		t.Errorf("파드 없음인데 found=%v err=%v", none.found, err)
	}
}

// 종료 메시지는 여러 줄일 수 있다(점검 Job 의 로그 꼬리 등). 상태 메시지·이벤트에 한 줄로 실리도록 공백을 접는다.
func TestJobTermination_FoldsMultilineMessage(t *testing.T) {
	job := makeInstallJob(false, false)
	pod := makeJobPod(job.Name, driverjob.InstallContainerName, 1, "Error", " 첫 줄\n\t둘째  줄\r\n셋째\n", false)
	got, err := newUpgradeSMWithRecorder(job, pod).jobTermination(context.Background(), job, driverjob.InstallContainerName)
	if err != nil || got.message != "첫 줄 둘째 줄 셋째" {
		t.Errorf("message=%q err=%v, want 한 줄로 접힌 %q", got.message, err, "첫 줄 둘째 줄 셋째")
	}
}

// 같은 이름으로 다시 만든 Job 의 종료 기록에는 옛 Job 의 파드가 섞이지 않는다. 지워지는 중인 옛 파드는
// 같은 job-name 라벨을 달고 있어도 다른 UID 의 Job 이 소유한다.
func TestJobTermination_SkipsPodsOfRecreatedJob(t *testing.T) {
	const container, oldMsg, newMsg = driverjob.InstallContainerName, "옛 Job 의 파드", "새 Job 의 파드"
	ctx := context.Background()
	jobKind := batchv1.SchemeGroupVersion.WithKind("Job")
	job := makeInstallJob(false, false)
	job.UID = "uid-new"
	oldJob := job.DeepCopy()
	oldJob.UID = "uid-old"
	recent := metav1.NewTime(time.Now())
	earlier := metav1.NewTime(time.Now().Add(-time.Minute))

	// 옛 파드는 종료 기록이 더 최근이고 종료 코드가 3 이다. 고르면 새 Job 의 hold 충돌로 오판한다.
	oldPod := makeJobPod(job.Name, container, driverjob.ExitCodeHostPackageConflict, "Error", oldMsg, true)
	oldPod.Name += "-old"
	oldPod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(oldJob, jobKind)}
	oldPod.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt = recent
	newPod := makeJobPod(job.Name, container, 1, "Error", newMsg, true)
	newPod.Name += "-new"
	newPod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(job, jobKind)}
	newPod.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt = earlier

	got, err := newUpgradeSMWithRecorder(oldPod, newPod).jobTermination(ctx, job, container)
	if err != nil || !got.found || got.exitCode != 1 || got.message != newMsg {
		t.Errorf("got=%+v err=%v, want 새 Job 소유 파드의 종료(exit 1, %s)", got, err, newMsg)
	}
	onlyOld, err := newUpgradeSMWithRecorder(oldPod).jobTermination(ctx, job, container)
	if err != nil || onlyOld.found {
		t.Errorf("옛 Job 의 파드뿐인데 found=%v exit=%d err=%v, want found=false", onlyOld.found, onlyOld.exitCode, err)
	}
}
