// ============================================================
// preflight_job.go: NVIDIA 드라이버 설치 전 점검 Job 실행·판정(PreFlight 단계)
// 상세: cordon 전에 설치기 preflight.sh 를 Job 으로 돌려 hold 된 호스트 패키지가 설치를 막는지 본다.
//       종료 코드 3(hold 충돌)만 막고, 그 밖의 실패는 판정 불가로 보고 기존 경로로 넘긴다.
// 생성일: 2026-10-27 | 수정일: 2026-10-27
// ============================================================

package upgrade

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "kcloud-operator/api/v1alpha1"
	"kcloud-operator/internal/driverjob"
	"kcloud-operator/internal/naming"
)

// preflightVerdict 는 설치 전 점검 결과다.
type preflightVerdict int

const (
	// preflightRunning 은 점검 Job 을 만들었거나 아직 도는 중이다 — PreFlight 에 머문다.
	preflightRunning preflightVerdict = iota
	// preflightPassed 는 통과했거나 판정하지 못했다 — 기존 경로(Cordoning)로 간다.
	preflightPassed
	// preflightBlocked 는 hold 된 호스트 패키지가 설치를 막는다 — cordon 없이 Failed.
	preflightBlocked
)

// preflightApplicable 은 이 정책에 설치 전 점검 Job 을 띄울지다. preflight.sh 는 NVIDIA 설치기의
// apt 경로에만 있으므로 다른 벤더·설치 방식·추적 전용 정책은 점검하지 않는다.
func preflightApplicable(policy *v1alpha1.DriverInstallPolicy) bool {
	return isJobMode(policy) && !policy.Spec.Driver.TrackOnly &&
		strings.EqualFold(policy.Spec.Vendor, "nvidia") &&
		policy.Spec.Driver.Installer == v1alpha1.DriverInstallerAPT
}

// runDriverPreflight 는 설치 전 점검 Job 을 만들거나 결과를 읽는다. 결과를 읽은 뒤에는 Job 을
// 지운다 — 남겨 두면 다음 사이클이 같은 이미지·버전의 지난 결과를 다시 읽는다. 반환 문자열은
// preflightBlocked 일 때의 사유다.
func (m *UpgradeStateMachine) runDriverPreflight(
	ctx context.Context,
	state *v1alpha1.DriverUpgradeState,
	policy *v1alpha1.DriverInstallPolicy,
) (preflightVerdict, string, error) {
	image, version := installTargetOf(state, policy)
	name := naming.PreflightJobName(state.Spec.Vendor, state.Spec.Model, state.Spec.NodeName)
	var job batchv1.Job
	err := m.Get(ctx, types.NamespacedName{Name: name, Namespace: driverjob.Namespace}, &job)
	if apierrors.IsNotFound(err) {
		desired := driverjob.RenderPreflightJob(policy, state.Spec.NodeName, image, version)
		if err := m.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return preflightRunning, "", fmt.Errorf("설치 전 점검 Job 생성 실패: %w", err)
		}
		m.Recorder.Eventf(state, corev1.EventTypeNormal, "DriverPreflightStarted",
			"노드 %s 설치 전 점검 Job 생성: %s (image=%s, version=%s)", state.Spec.NodeName, name, image, version)
		return preflightRunning, "", nil
	}
	if err != nil {
		return preflightRunning, "", fmt.Errorf("설치 전 점검 Job 조회 실패: %w", err)
	}
	if !jobTargetsInstall(&job, image, version) {
		if err := m.deleteInstallJob(ctx, &job); client.IgnoreNotFound(err) != nil {
			return preflightRunning, "", fmt.Errorf("지난 설치 전 점검 Job 삭제 실패: %w", err)
		}
		return preflightRunning, "", nil
	}
	if !jobComplete(&job) && !jobFailed(&job) {
		return preflightRunning, "", nil
	}
	term, err := m.jobTermination(ctx, &job, driverjob.PreflightContainerName)
	if err != nil {
		return preflightRunning, "", fmt.Errorf("설치 전 점검 파드 조회 실패: %w", err)
	}
	if err := m.deleteInstallJob(ctx, &job); client.IgnoreNotFound(err) != nil {
		return preflightRunning, "", fmt.Errorf("설치 전 점검 Job 삭제 실패: %w", err)
	}
	if jobComplete(&job) {
		m.Recorder.Eventf(state, corev1.EventTypeNormal, "DriverPreflightPassed",
			"노드 %s 설치 전 점검 통과 (version=%s)", state.Spec.NodeName, version)
		return preflightPassed, "", nil
	}
	if term.found && term.exitCode == driverjob.ExitCodeHostPackageConflict {
		cause := term.message
		if cause == "" {
			cause = holdConflictDefaultCause
		}
		return preflightBlocked, cause, nil
	}
	if !term.found {
		m.Recorder.Eventf(state, corev1.EventTypeWarning, "DriverPreflightInconclusive",
			"노드 %s 설치 전 점검이 판정하지 못함: 파드 종료 기록 없음(Job 실패 사유: %s) — 설치를 진행한다",
			state.Spec.NodeName, jobFailedReason(&job))
		return preflightPassed, "", nil
	}
	m.Recorder.Eventf(state, corev1.EventTypeWarning, "DriverPreflightInconclusive",
		"노드 %s 설치 전 점검이 판정하지 못함(exit=%d %s): %s — 설치를 진행한다",
		state.Spec.NodeName, term.exitCode, term.reason, term.message)
	return preflightPassed, "", nil
}

// jobFailedReason 은 Job 의 Failed 조건이 적은 사유(reason)와, 있으면 메시지다. 없으면 "기록 없음" 이다.
func jobFailedReason(job *batchv1.Job) string {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue && c.Reason != "" {
			if c.Message == "" {
				return c.Reason
			}
			return c.Reason + ": " + c.Message
		}
	}
	return "기록 없음"
}

// preflightBlockedMessage 는 설치 전 점검이 설치를 막았을 때의 상태 메시지다. Failed 는 정책을
// 다시 읽지 않으므로 재시도 방법을 끝에 둔다.
func preflightBlockedMessage(state *v1alpha1.DriverUpgradeState, cause string) string {
	return fmt.Sprintf("설치 전 점검 실패(노드 cordon 하지 않음): %s — 조치 뒤 kubectl delete dus %s 로 다시 시도한다",
		cause, state.Name)
}
