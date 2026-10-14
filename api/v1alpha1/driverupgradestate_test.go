// driverupgradestate_test.go: DriverUpgradeState CRD 필드 노출 시험
// 생성일: 2026-10-14 | 수정일: 2026-10-14

package v1alpha1

import (
	"os"
	"strings"
	"testing"
)

func TestDriverUpgradeStateCRDHasRolledBackFields(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/npu.ai_driverupgradestates.yaml")
	if err != nil {
		t.Fatalf("CRD 읽기 실패: %v", err)
	}
	for _, f := range []string{"rolledBackVersion", "rolledBackGeneration"} {
		if !strings.Contains(string(raw), f) {
			t.Errorf("CRD 에 %s 필드가 없다 — make manifests 재생성 필요", f)
		}
	}
}
