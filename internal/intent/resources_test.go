// ============================================================
// resources_test.go: 벤더↔리소스명 카탈로그 테스트
// 상세: MIG 만 profile 별 리소스명을 만들고, 나머지 벤더는 flat 광고라는 사실을 고정한다.
// furiosa 는 product(rngd/warboy)로만 갈리고, product 가 없거나 못 알아보면 에러라는 것도 고정한다.
// 생성일: 2026-07-29 | 수정일: 2026-10-05 (도메인 접두 집합 시험 추가)
// ============================================================
package intent

import (
	"strings"
	"testing"

	"kcloud-operator/api/v1alpha1"
)

func TestWholeDeviceResource(t *testing.T) {
	cases := []struct {
		vendor  string
		product string
		want    string
		wantErr bool
	}{
		{"nvidia", "", "nvidia.com/gpu", false},
		{"NVIDIA", "A30", "nvidia.com/gpu", false}, // 단일 product 벤더는 product 값을 무시한다
		{"furiosa", "rngd", "furiosa.ai/rngd", false},
		{"furiosa", "warboy", "beta.furiosa.ai/npu", false},
		{"furiosa", "", "", true},      // 제품이 둘이라 비면 못 정한다
		{"furiosa", "bogus", "", true}, // 못 알아보는 product 도 마찬가지
		{"rebellions", "", "rebellions.ai/ATOM", false},
		{"tenstorrent", "", "tenstorrent.com/blackhole", false},
		{"banana", "", "", false}, // 미지 벤더는 그냥 미지원("") 이지 에러가 아니다
	}
	for _, c := range cases {
		got, err := WholeDeviceResource(c.vendor, c.product)
		if (err != nil) != c.wantErr {
			t.Fatalf("WholeDeviceResource(%q,%q) err=%v wantErr=%v", c.vendor, c.product, err, c.wantErr)
		}
		if got != c.want {
			t.Fatalf("WholeDeviceResource(%q,%q)=%q want %q", c.vendor, c.product, got, c.want)
		}
	}
}

// RNGD 는 PE 파티션을 만들어도 furiosa.ai/rngd 하나로 flat 광고한다 — 리소스명이 갈리지 않는다.
func TestPartitionResource(t *testing.T) {
	if got, err := PartitionResource("nvidia", "", "1g.6gb"); err != nil || got != "nvidia.com/mig-1g.6gb" {
		t.Fatalf("nvidia partition resource = %q, err=%v", got, err)
	}
	if got, err := PartitionResource("nvidia", "", ""); err != nil || got != "nvidia.com/gpu" {
		t.Fatalf("nvidia without profile = %q, err=%v", got, err)
	}
	if got, err := PartitionResource("furiosa", "rngd", "2core.12gb"); err != nil || got != "furiosa.ai/rngd" {
		t.Fatalf("furiosa partition resource = %q, err=%v, want flat furiosa.ai/rngd", got, err)
	}
	if _, err := PartitionResource("furiosa", "", "2core.12gb"); err == nil {
		t.Fatal("furiosa partition without product should error, not default to RNGD")
	}
}

func TestResourceForMode(t *testing.T) {
	nv := v1alpha1.AcceleratorMapping{Vendor: "nvidia", NativeProfile: "1g.6gb"}
	cases := []struct {
		mode string
		want string
	}{
		{v1alpha1.AccessModeExclusive, "nvidia.com/gpu"},
		{v1alpha1.AccessModeShared, "nvidia.com/gpu"},
		{v1alpha1.AccessModePartitioned, "nvidia.com/mig-1g.6gb"},
		{v1alpha1.AccessModePartitionedShared, "nvidia.com/mig-1g.6gb"},
	}
	for _, c := range cases {
		got, err := ResourceFor(nv, c.mode)
		if err != nil {
			t.Fatalf("ResourceFor(%s) unexpected err=%v", c.mode, err)
		}
		if got != c.want {
			t.Fatalf("ResourceFor(%s)=%q want %q", c.mode, got, c.want)
		}
	}

	warboy := v1alpha1.AcceleratorMapping{Vendor: "furiosa", Product: "warboy"}
	if got, err := ResourceFor(warboy, v1alpha1.AccessModeExclusive); err != nil || got != "beta.furiosa.ai/npu" {
		t.Fatalf("ResourceFor(furiosa/warboy)=%q err=%v", got, err)
	}
	rngd := v1alpha1.AcceleratorMapping{Vendor: "furiosa", Product: "rngd"}
	if got, err := ResourceFor(rngd, v1alpha1.AccessModeExclusive); err != nil || got != "furiosa.ai/rngd" {
		t.Fatalf("ResourceFor(furiosa/rngd)=%q err=%v", got, err)
	}
	ambiguous := v1alpha1.AcceleratorMapping{Vendor: "furiosa"}
	if _, err := ResourceFor(ambiguous, v1alpha1.AccessModeExclusive); err == nil {
		t.Fatal("ResourceFor(furiosa, no product) should error, not default to RNGD")
	}
}

func TestVendorForResource(t *testing.T) {
	for name, want := range map[string]string{
		"nvidia.com/gpu":        "nvidia",
		"nvidia.com/mig-1g.6gb": "nvidia",
		"furiosa.ai/rngd":       "furiosa",
		"beta.furiosa.ai/npu":   "furiosa",
		"rebellions.ai/ATOM":    "rebellions",
		"cpu":                   "",
		"memory":                "",
		"nvidia.com/gpu-shared": "",
	} {
		if got := VendorForResource(name); got != want {
			t.Fatalf("VendorForResource(%q)=%q want %q", name, got, want)
		}
	}
}

// TestAcceleratorResourceDomainsCoversEveryVendor 는 도메인 접두 집합이 카탈로그의 모든
// 리소스명을 덮는지 본다. 이 집합이 한 벤더를 빠뜨리면 그 벤더 장치를 쥔 Pod 가 uninstall
// 게이트(internal/uninstall/gate.go)와 드라이버 업그레이드 drain
// (internal/upgrade/state_machine.go)의 판정에서 조용히 빠진다 — rebellions.ai/ 가
// drain 쪽 목록에 없어 실제로 그랬다.
func TestAcceleratorResourceDomainsCoversEveryVendor(t *testing.T) {
	domains := AcceleratorResourceDomains()
	checked := 0
	for vendor, products := range productResource {
		for product, res := range products {
			checked++
			covered := false
			for _, d := range domains {
				if strings.Contains(res, d) {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("%s/%s 의 자원명 %q 가 도메인 집합 %v 의 어느 항목과도 겹치지 않는다",
					vendor, product, res, domains)
			}
		}
	}
	// 카탈로그 순회가 0회면 위 단언은 한 번도 실행되지 않고 통과한다.
	if checked == 0 {
		t.Fatal("productResource 가 비어 한 번도 대조하지 않았다")
	}
	// 가속기가 아닌 리소스명이 집합에 섞이면 판정이 과하게 잡는다.
	for _, rn := range []string{"cpu", "memory", "ephemeral-storage"} {
		for _, d := range domains {
			if strings.Contains(rn, d) {
				t.Errorf("가속기가 아닌 %q 가 도메인 %q 에 걸린다", rn, d)
			}
		}
	}
}

// TestAcceleratorResourceDomainsIsSortedAndDeduped 는 돌려주는 집합이 정렬·중복 제거
// 상태인지 본다. map 순회에서 유도하므로 정렬을 빼면 호출마다 순서가 달라져, 이 집합을
// 비교하거나 로그로 남기는 쪽이 호출마다 다른 값을 본다.
func TestAcceleratorResourceDomainsIsSortedAndDeduped(t *testing.T) {
	domains := AcceleratorResourceDomains()
	if len(domains) == 0 {
		t.Fatal("도메인 집합이 비었다")
	}
	for i := 1; i < len(domains); i++ {
		if domains[i-1] >= domains[i] {
			t.Errorf("정렬·중복 제거가 깨졌다: %v", domains)
		}
	}
	for _, d := range domains {
		if !strings.HasSuffix(d, "/") {
			t.Errorf("도메인 접두 %q 가 \"/\" 로 끝나지 않는다 — 부분 일치가 다른 벤더까지 집는다", d)
		}
	}
}
