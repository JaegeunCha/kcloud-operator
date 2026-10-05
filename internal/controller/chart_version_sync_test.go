// ============================================================
// chart_version_sync_test.go: chart 선언 버전과 배포 프리셋이 갈라지지 않는지 고정한다.
// 상세: Chart.yaml 의 appVersion 은 릴리스마다 손으로 올려야 하는데, 실제 배포는
//
//	values 의 image.tag 로 결정된다. 둘이 갈라져도 아무 데서도 실패하지 않아
//	2026-08-06 시점에 appVersion=v0.5.79 인 채 v0.5.103 이 돌고 있었다.
//	선언값을 진실로 믿고 무엇이 떠 있는지 판단하면 틀리게 된다.
//
// 범위: 이 시험은 "chart 가 스스로에 대해 일관된가" 만 본다. 클러스터에 실제로 무엇이
//
//	떠 있는지는 라이브 검증의 몫이다(여기서 클러스터를 조회하지 않는다).
//
// 생성일: 2026-08-06 | 수정일: 2026-10-05 (kubeVersion 선언 시험 추가)
// ============================================================
package controller

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// chartAppVersion 은 deploy/helm/Chart.yaml 의 appVersion 을 읽는다.
func chartAppVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../deploy/helm/Chart.yaml")
	if err != nil {
		t.Fatalf("Chart.yaml 읽기 실패: %v", err)
	}
	re := regexp.MustCompile(`(?m)^appVersion:\s*(\S+)\s*$`)
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatal("Chart.yaml 에 appVersion 이 없다")
	}
	return strings.Trim(string(m[1]), `"'`)
}

// presetImageTag 는 values 프리셋의 image.tag 를 읽는다.
func presetImageTag(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s 읽기 실패: %v", path, err)
	}
	// image: 블록 아래 tag: 만 잡는다(detector 등 다른 블록의 tag 와 섞이지 않게).
	re := regexp.MustCompile(`(?m)^image:\s*\n(?:\s+\S.*\n)*?\s+tag:\s*(\S+)\s*$`)
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("%s 에 image.tag 가 없다", path)
	}
	return strings.Trim(string(m[1]), `"'`)
}

// line128Suffix 는 K8s 1.28 라인 operator 이미지 태그의 접미사다.
const line128Suffix = "-k8s1.28"

// chartPairedRelease 는 Chart.yaml 의 npu.ai/paired-release 주석을 읽는다. 반대 라인의
// 릴리스 번호이고, 그 라인의 프리셋이 가리켜야 할 태그다.
func chartPairedRelease(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../deploy/helm/Chart.yaml")
	if err != nil {
		t.Fatalf("Chart.yaml 읽기 실패: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s+npu\.ai/paired-release:\s*(\S+)\s*$`)
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatal("Chart.yaml annotations 에 npu.ai/paired-release 가 없다 — " +
			"반대 라인 프리셋이 어느 릴리스를 가리켜야 하는지 판정할 수 없다")
	}
	return strings.Trim(string(m[1]), `"'`)
}

// TestChartAppVersionMatchesPresets 는 두 배포 프리셋이 각각 옳은 릴리스를 가리키는지
// 본다. 두 라인은 릴리스 번호가 따로 붙으므로(1.28 = v0.6.0-k8s1.28, 1.34 = v0.7.0)
// 접미사만 붙여 유도할 수 없다. 이 브랜치가 만드는 이미지는 appVersion 이 정하고,
// 반대 라인 프리셋은 Chart.yaml 이 선언한 짝 릴리스를 그대로 가리켜야 한다.
func TestChartAppVersionMatchesPresets(t *testing.T) {
	const (
		preset128 = "../../deploy/helm/values-k8s1.28.yaml"
		preset134 = "../../deploy/helm/values-k8s1.34.yaml"
	)
	app := chartAppVersion(t)
	paired := chartPairedRelease(t)

	// 이 체크아웃이 어느 라인인지는 appVersion 접미사가 정한다.
	own, other := preset134, preset128
	if strings.HasSuffix(app, line128Suffix) {
		own, other = preset128, preset134
	}
	if !strings.HasSuffix(paired, line128Suffix) && !strings.HasSuffix(app, line128Suffix) {
		t.Errorf("appVersion=%q, paired-release=%q — 둘 다 1.28 라인이 아니다. "+
			"두 라인 중 하나는 반드시 %s 접미를 가져야 한다", app, paired, line128Suffix)
	}

	if got := presetImageTag(t, own); got != app {
		t.Errorf("%s: image.tag=%q, want %q (이 브랜치가 만드는 이미지 = Chart.yaml appVersion)",
			own, got, app)
	}
	if got := presetImageTag(t, other); got != paired {
		t.Errorf("%s: image.tag=%q, want %q (반대 라인 = Chart.yaml annotations 의 짝 릴리스)",
			other, got, paired)
	}
}

// TestDefaultValuesTagMatchesAppVersion 은 기본 values.yaml 도 이 브랜치의 릴리스를
// 가리키는지 본다. 프리셋 없이 설치하는 경로가 낡은 태그를 당겨 옛 operator 가 뜨는
// 사고가 반복돼 왔다 — 기본값이 낡은 것 자체가 결함이다.
func TestDefaultValuesTagMatchesAppVersion(t *testing.T) {
	app := chartAppVersion(t)
	if got := presetImageTag(t, "../../deploy/helm/values.yaml"); got != app {
		t.Errorf("values.yaml: image.tag=%q, want %q (Chart.yaml appVersion)", got, app)
	}
}

// imageCoordinateKey 는 이미지 좌표를 담는 키인지 본다. 이런 키가 프리셋에 있으면
// 그 이미지가 라인별로 갈린다는 뜻이다.
func imageCoordinateKey(k string) bool {
	switch k {
	case "repository", "tag", "image":
		return true
	}
	return strings.HasSuffix(k, "Image") || strings.HasSuffix(k, "Repository")
}

// findImageCoordinates 는 노드를 훑어 이미지 좌표 키의 경로를 모은다.
func findImageCoordinates(node any, path string, out *[]string) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	for k, v := range m {
		p := k
		if path != "" {
			p = path + "." + k
		}
		// 최상위 image 블록은 operator 이미지다 — 막지 않고 안으로 들어가 어느 좌표를
		// 건드리는지까지 본다(tag 는 허용, repository 는 아니다).
		if imageCoordinateKey(k) && p != "image" {
			*out = append(*out, p)
			continue
		}
		findImageCoordinates(v, p, out)
	}
}

// TestReleaseLinePresetsShareNonOperatorImages 는 두 프리셋이 operator 이외의 이미지를
// 재정의하지 않는지 본다. 라인별로 갈리는 산출물은 operator 하나뿐이라는 것이 이 설계의
// 전제다(k8s 라이브러리를 링크하는 것이 operator 뿐 — detector 는 k8s.io v0.29, tenstorrent
// device-plugin 은 kubelet API 만, 나머지는 k8s 의존 없음). 프리셋에 다른 이미지가 끼면
// 그 전제가 조용히 깨진다.
//
// 이미지 좌표만 막는다. 기능 토글(api.enabled 등)은 프리셋에 있어야 하는 것이다 —
// 그것이 없어서 차트만으로 설치하면 라이브와 다른 물건이 뜨던 것을 릴리스 게이트가
// 잡았고, 그래서 프로파일에 토글을 넣었다.
func TestReleaseLinePresetsShareNonOperatorImages(t *testing.T) {
	for _, path := range []string{
		"../../deploy/helm/values-k8s1.34.yaml",
		"../../deploy/helm/values-k8s1.28.yaml",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s 읽기 실패: %v", path, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s 파싱 실패: %v", path, err)
		}
		var found []string
		findImageCoordinates(doc, "", &found)
		for _, p := range found {
			if p == "image.tag" {
				continue // operator 이미지 — 이것만 라인별로 갈린다
			}
			t.Errorf("%s: %s 가 이미지를 재정의한다 — 라인별로 갈리는 것은 operator 뿐이어야 한다",
				path, p)
		}
	}
}

// chartKubeVersion 은 Chart.yaml 의 kubeVersion 선언을 읽는다. 선언이 없으면 치명
// 실패다 — 범위 밖 K8s 에 설치되는 것을 막는 유일한 수단이 사라진 것이다.
func chartKubeVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../deploy/helm/Chart.yaml")
	if err != nil {
		t.Fatalf("Chart.yaml 읽기 실패: %v", err)
	}
	// 값 안에 공백이 있다(">=1.26.0-0 <1.31.0-0") — \S+ 로 잡으면 하한만 읽힌다.
	m := regexp.MustCompile(`(?m)^kubeVersion:\s*(.+?)\s*$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("Chart.yaml 에 kubeVersion 이 없다 — 범위 밖 K8s 설치를 막을 수단이 없다")
	}
	return strings.Trim(string(m[1]), `"'`)
}

// TestChartKubeVersionMatchesLineRange 는 kubeVersion 선언의 경계가 이 라인의 범위와
// 같은지 본다. description·README·release.yaml 이 1.26~1.30 으로 적어 둔 라인이므로
// 선언도 그 경계여야 한다 — 어긋나면 문서가 된다는 범위와 helm 이 강제하는 범위가 갈린다.
func TestChartKubeVersionMatchesLineRange(t *testing.T) {
	if got, want := chartKubeVersion(t), ">=1.26.0-0 <1.31.0-0"; got != want {
		t.Errorf("kubeVersion=%q, want %q (1.26~1.30 라인)", got, want)
	}
}

// TestChartKubeVersionRejectsOtherLine 은 kubeVersion 선언이 라인 경계를 실제로 막는지
// helm 으로 확인한다. 선언을 적어 두고 helm 이 그것을 보는지 확인하지 않으면, 범위 밖
// 클러스터에 그대로 설치되고 선언은 주석만 한 값이 된다.
//
// 주의: os/exec 로 helm 을 부르므로 손으로 돌릴 때는 -count=1 을 붙일 것(캐시가 차트
// 변경을 보지 못한다). make test 는 -coverprofile 이 캐시를 끈다.
func TestChartKubeVersionRejectsOtherLine(t *testing.T) {
	bin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm 이 PATH 에 없다 — 범위 시험 건너뜀")
	}
	for _, c := range []struct {
		kube   string
		reject bool
	}{
		{"1.26.0", false},  // 선언 하한
		{"1.28.15", false}, // 실측한 버전
		{"1.30.5", false},  // 선언 상한 직전
		{"1.31.0", true},   // 기본 라인 하한 — 이 차트가 거절해야 한다
		{"1.34.1", true},   // 기본 라인 최신 — 이 차트가 거절해야 한다
	} {
		out, err := exec.Command(bin, "template", "t", "../../deploy/helm",
			"--kube-version", c.kube).CombinedOutput()
		switch {
		case c.reject && err == nil:
			t.Errorf("--kube-version %s 가 통과했다 — kubeVersion 선언이 경계를 막지 못한다",
				c.kube)
		case c.reject && !strings.Contains(string(out), "kubeVersion"):
			t.Errorf("--kube-version %s 가 거절된 사유가 kubeVersion 이 아니다:\n%s", c.kube, out)
		case !c.reject && err != nil:
			t.Errorf("--kube-version %s 가 거절됐다 — 범위 안인데 설치가 막힌다: %v\n%s",
				c.kube, err, out)
		}
	}
}

// TestChartLintPasses 는 kubeVersion 을 선언한 차트가 helm lint 를 통과하는지 본다.
// lint 는 kubeVersion 을 강제하지 않으므로 선언 형식이 깨져도 render 에서만 드러난다 —
// 두 경로를 함께 고정한다.
func TestChartLintPasses(t *testing.T) {
	bin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm 이 PATH 에 없다 — lint 시험 건너뜀")
	}
	if out, err := exec.Command(bin, "lint", "../../deploy/helm",
		"--kube-version", chartLineKubeVersion).CombinedOutput(); err != nil {
		t.Errorf("helm lint 실패: %v\n%s", err, out)
	}
}
