// ============================================================
// chart_rebellions_generation_test.go: rebellions.devicePluginGeneration 차트 전달 시험(1.28 라인)
// 상세: 1.28 라인 차트에는 values.schema.json 이 없어 허용값 검사는 CRD enum 이 맡는다. 여기서는 값이
//       정책에 그대로 실리는지, 빈 값이면 싣지 않는지만 본다.
// 생성일: 2026-10-24 | 수정일: 2026-10-24
// ============================================================

package controller

import (
	"strings"
	"testing"
)

func TestChartRebellionsDevicePluginGenerationPassThrough(t *testing.T) {
	out := helmTemplate(t, "--set", "rebellions.devicePluginGeneration=v0.4")
	if !strings.Contains(out, `devicePluginGeneration: "v0.4"`) {
		t.Error("정책에 devicePluginGeneration 이 실리지 않았다")
	}
	if strings.Contains(helmTemplate(t), "devicePluginGeneration:") {
		t.Error("빈 값인데 정책에 devicePluginGeneration 이 실렸다")
	}
}
