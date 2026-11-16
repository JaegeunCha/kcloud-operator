// ============================================================
// chart_version_sync_test.go: chart 선언 버전과 배포 프리셋이 갈라지지 않는지 고정한다.
// 상세: Chart.yaml 의 appVersion 은 릴리스마다 손으로 올리지만 실제 배포는 values 의 image.tag 로 결정된다. 둘이
// 갈라져도 아무 데서도 실패하지 않아(appVersion=v0.5.79 인 채 v0.5.103 이 도는 상태가 관측된 적이 있다) 선언값만
// 보고 무엇이 떠 있는지 판단하면 틀린다. 이 시험은 "chart 가 스스로에 대해 일관된가" 만 보고, 클러스터에 실제로
// 무엇이 떠 있는지는 라이브 검증의 몫이다(여기서 클러스터를 조회하지 않는다).
// 생성일: 2026-08-06 | 수정일: 2026-11-16
// ============================================================
package controller

import (
	"fmt"
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

// otherLinePresetEnv 는 반대 라인(기본 라인) 프리셋 파일 경로를 받는 환경 변수다. 그
// 프리셋은 이 차트에 없으므로(kubeVersion 선언과 실재를 맞추려고 뺐다) 작업 트리에서
// 읽을 수 없다. `make other-line-preset-check` 가 기본 라인 ref 의 blob 을 임시 파일로
// 꺼내 이 변수로 넘긴다 — 그 타깃은 ref 가 없으면 건너뛰지 않고 실패로 센다.
// 변수가 비면 작업 트리 프리셋만 본다(make test 경로. CI 체크아웃에 그 ref 가 없다).
const otherLinePresetEnv = "KCLOUD_OTHER_LINE_PRESET"

// linePreset 은 이 라인의 배포 프리셋 경로다.
const linePreset = "../../deploy/helm/values-k8s1.28.yaml"

// presetMinorRe 는 프리셋 파일 이름에서 그 프리셋이 겨냥하는 K8s 마이너를 뽑는다
// (values-k8s1.34.yaml, values-k8s1.34-dra.yaml 둘 다 1.34).
var presetMinorRe = regexp.MustCompile(`^values-k8s(\d+\.\d+)(?:-[^.]+)?\.yaml$`)

// minorOf 는 "1.28" 을 128 로 바꾼다. 마이너까지만 비교하면 충분하다.
func minorOf(v string) (int, error) {
	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d.%d", &major, &minor); err != nil {
		return 0, fmt.Errorf("버전 %q 를 해석하지 못했다: %w", v, err)
	}
	return major*100 + minor, nil
}

// kubeVersionBounds 는 kubeVersion 선언 문자열에서 [floor, ceil) 경계를 뽑는다. 이 라인은
// 위에 기본 라인이 있어 상한이 있다 — 하한만 읽으면 범위 밖 프리셋 판정이 한 방향밖에 못 본다.
// testing.T 가 없는 Ginkgo 스펙(envtest_server_version_test.go)도 쓸 수 있도록 순수 함수로 둔다.
func kubeVersionBounds(decl string) (floor, ceil int, err error) {
	f := regexp.MustCompile(`>=\s*(\d+\.\d+)`).FindStringSubmatch(decl)
	if f == nil {
		return 0, 0, fmt.Errorf("kubeVersion %q 에서 >= 하한을 읽지 못했다", decl)
	}
	c := regexp.MustCompile(`<\s*(\d+\.\d+)`).FindStringSubmatch(decl)
	if c == nil {
		return 0, 0, fmt.Errorf("kubeVersion %q 에서 < 상한을 읽지 못했다 — 상한이 없으면 "+
			"기본 라인 클러스터가 이 차트를 설치한다", decl)
	}
	if floor, err = minorOf(f[1]); err != nil {
		return 0, 0, fmt.Errorf("kubeVersion 하한 %q 해석 실패: %w", f[1], err)
	}
	if ceil, err = minorOf(c[1]); err != nil {
		return 0, 0, fmt.Errorf("kubeVersion 상한 %q 해석 실패: %w", c[1], err)
	}
	return floor, ceil, nil
}

// chartKubeVersionBounds 는 kubeVersionBounds 의 testing.T 래퍼다.
func chartKubeVersionBounds(t *testing.T) (decl string, floor, ceil int) {
	t.Helper()
	decl = chartKubeVersion(t)
	var err error
	if floor, ceil, err = kubeVersionBounds(decl); err != nil {
		t.Fatal(err)
	}
	return decl, floor, ceil
}

// TestChartAppVersionMatchesPresets 는 이 라인 프리셋이 Chart.yaml 의 appVersion 을
// 가리키는지 본다. 두 라인은 릴리스 번호가 따로 붙으므로(1.28 = v0.6.2-k8s1.28,
// 기본 라인 = v0.8.0) 접미사만 붙여 유도할 수 없다. 반대 라인 프리셋은 kubeVersion 선언과
// 실재를 맞추기 위해 이 차트에 두지 않는다 — 짝 릴리스 번호의 대조 상대는
// deploy/integration/release.yaml 의 1.31~1.34 operatorTag 이고, 그 대조는
// TestChartPairedReleaseMatchesReleaseManifest 가 한다.
func TestChartAppVersionMatchesPresets(t *testing.T) {
	app := chartAppVersion(t)
	paired := chartPairedRelease(t)

	// 이 차트는 1.26~1.30 라인이다. appVersion 에 그 접미가 없으면 라인을 혼동한 것이다.
	if !strings.HasSuffix(app, line128Suffix) {
		t.Fatalf("appVersion=%q — 이 차트는 1.26~1.30 라인이므로 %s 접미를 가져야 한다. "+
			"기본 라인은 main 브랜치가 따로 올린다", app, line128Suffix)
	}
	if strings.HasSuffix(paired, line128Suffix) {
		t.Errorf("paired-release=%q — 반대 라인은 기본 라인이므로 %s 접미를 가지면 안 된다. "+
			"이 선언이 어느 라인을 가리키는지 알 수 없으면 짝 대조가 뜻을 잃는다",
			paired, line128Suffix)
	}

	if got := presetImageTag(t, linePreset); got != app {
		t.Errorf("%s: image.tag=%q, want %q (이 브랜치가 만드는 이미지 = Chart.yaml appVersion)",
			linePreset, got, app)
	}
}

// TestChartPairedReleaseMatchesReleaseManifest 는 Chart.yaml 의 짝 릴리스 선언이
// deploy/integration/release.yaml 의 기본 라인 프로파일과 같은 태그를 가리키는지 본다.
// 반대 라인 프리셋이 이 차트에 없으므로 그 선언은 이 매니페스트와 대조한다 — 대조 상대가
// 없으면 선언이 묵어도 아무 데서도 걸리지 않는다(한 릴리스 묵은 채 남은 사례가 있다).
func TestChartPairedReleaseMatchesReleaseManifest(t *testing.T) {
	const relPath = "../../deploy/integration/release.yaml"
	raw, err := os.ReadFile(relPath)
	if err != nil {
		t.Fatalf("%s 읽기 실패: %v", relPath, err)
	}
	var rel struct {
		KubernetesProfiles []struct {
			Range       string `json:"range"`
			OperatorTag string `json:"operatorTag"`
		} `json:"kubernetesProfiles"`
	}
	if err := yaml.Unmarshal(raw, &rel); err != nil {
		t.Fatalf("%s 파싱 실패: %v", relPath, err)
	}
	if len(rel.KubernetesProfiles) == 0 {
		t.Fatalf("%s: 프로파일이 0개다 — 대조가 비었으면 통과가 아니다", relPath)
	}

	paired := chartPairedRelease(t)
	app := chartAppVersion(t)
	var checked int
	for _, prof := range rel.KubernetesProfiles {
		// 이 라인 프로파일은 appVersion 을, 기본 라인 프로파일은 짝 선언을 가리켜야 한다.
		want := paired
		if prof.OperatorTag == app || strings.HasSuffix(prof.OperatorTag, line128Suffix) {
			want = app
		}
		checked++
		if prof.OperatorTag != want {
			t.Errorf("%s: range=%q operatorTag=%q, want %q", relPath, prof.Range,
				prof.OperatorTag, want)
		}
	}
	if checked < 2 {
		t.Errorf("%s: 프로파일이 %d개다 — 두 라인 모두 적혀 있어야 대조가 성립한다",
			relPath, checked)
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

// findImageCoordinates 는 노드를 훑어 이미지 좌표 키의 경로를 모은다. 이미지 좌표만 모으고 기능
// 토글(api.enabled 등)은 보지 않는다 — 토글은 프리셋에 있어야 하는 것이라, 빠지면 차트만으로
// 설치했을 때 라이브와 다른 구성이 뜬다.
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
// 재정의하지 않는지 본다. 라인별로 갈리는 산출물은 operator 하나뿐이라는 것이 이 설계의 전제다(k8s
// 라이브러리를 링크하는 것이 operator 뿐 — detector 는 k8s.io v0.29, tenstorrent device-plugin 은
// kubelet API 만, 나머지는 k8s 의존 없음). 프리셋에 다른 이미지가 끼면 그 전제가 조용히 깨진다. 이미지
// 좌표만 막는다(findImageCoordinates). 기본 라인 브랜치에만 있는 반대 라인 프리셋은 otherLinePresetEnv
// 로 받는다 — `make other-line-preset-check` 가 그 ref 의 blob 을 임시 파일로 꺼내 이 시험에 넘긴다.
func TestReleaseLinePresetsShareNonOperatorImages(t *testing.T) {
	paths := []string{linePreset}
	if p := os.Getenv(otherLinePresetEnv); p != "" {
		paths = append(paths, p)
	}
	for _, path := range paths {
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

// readChartKubeVersion 은 deploy/helm/Chart.yaml 의 kubeVersion 선언을 읽는다. testing.T 가
// 없는 Ginkgo 스펙(envtest_server_version_test.go)도 쓸 수 있도록 순수 함수로 둔다.
func readChartKubeVersion() (string, error) {
	raw, err := os.ReadFile("../../deploy/helm/Chart.yaml")
	if err != nil {
		return "", fmt.Errorf("Chart.yaml 읽기 실패: %w", err)
	}
	// 값 안에 공백이 있다(">=1.26.0-0 <1.31.0-0") — \S+ 로 잡으면 하한만 읽힌다.
	m := regexp.MustCompile(`(?m)^kubeVersion:\s*(.+?)\s*$`).FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("Chart.yaml 에 kubeVersion 이 없다 — 범위 밖 K8s 설치를 막을 수단이 없다")
	}
	return strings.Trim(string(m[1]), `"'`), nil
}

// chartKubeVersion 은 readChartKubeVersion 의 testing.T 래퍼다. 선언이 없으면 치명
// 실패다 — 범위 밖 K8s 에 설치되는 것을 막는 유일한 수단이 사라진 것이다.
func chartKubeVersion(t *testing.T) string {
	t.Helper()
	v, err := readChartKubeVersion()
	if err != nil {
		t.Fatal(err)
	}
	return v
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
	bin := helmBinary(t)
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
	bin := helmBinary(t)
	if out, err := exec.Command(bin, "lint", "../../deploy/helm",
		"--kube-version", chartLineKubeVersion).CombinedOutput(); err != nil {
		t.Errorf("helm lint 실패: %v\n%s", err, out)
	}
}

// TestChartCarriesNoOutOfRangePreset 은 이 차트가 kubeVersion 범위 밖 라인의 프리셋을
// 싣지 않는지 본다. 싣고 있으면 선언과 실재가 어긋난다 — kubeVersion 은 클러스터 버전만
// 보므로, 1.30 클러스터에서 기본 라인 프리셋을 -f 로 주면 선언은 통과하고 범위 밖 라인의
// operator 가 뜬다(실제로 일어난 경로다). 이름 하나를 집지 않고 deploy/helm 의
// values-k8s*.yaml 전부를 본다 — 파일 이름만 막으면 values-k8s1.32.yaml 처럼 다른 이름으로
// 같은 어긋남이 다시 들어온다.
func TestChartCarriesNoOutOfRangePreset(t *testing.T) {
	decl, floor, ceil := chartKubeVersionBounds(t)
	entries, err := os.ReadDir("../../deploy/helm")
	if err != nil {
		t.Fatalf("deploy/helm 읽기 실패: %v", err)
	}
	var checked int
	for _, e := range entries {
		m := presetMinorRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		checked++
		got, err := minorOf(m[1])
		if err != nil {
			t.Errorf("%s: 이름의 K8s 버전 %q 를 해석하지 못했다: %v", e.Name(), m[1], err)
			continue
		}
		if got < floor || got >= ceil {
			t.Errorf("%s 는 K8s %s 라인 프리셋인데 이 차트의 kubeVersion 은 %q 다 — "+
				"범위 밖 라인의 프리셋은 그 라인 브랜치에만 둔다(1.31+ 는 기본 라인)",
				e.Name(), m[1], decl)
		}
	}
	// 글롭이 어긋나면 0회 순회로 통과한다 — 표본이 있었는지 먼저 단언한다.
	if checked == 0 {
		t.Fatal("deploy/helm 에 values-k8s*.yaml 프리셋이 없어 한 번도 대조하지 않았다")
	}
}

// envtestK8sVersion 은 Makefile 의 `ENVTEST_K8S_VERSION ?= <major.minor>` 를 읽는다. 값이
// $(shell …) 같은 식이면 실패한다 — 그 식은 클라이언트 라이브러리 버전(k8s.io/api v0.33)을
// 따라가 이 라인의 지원 범위(kubeVersion 선언) 밖 API 서버로 시험을 돌린다.
func envtestK8sVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("Makefile 읽기 실패: %v", err)
	}
	// 선언이 둘 이상이면 make 는 마지막 즉시 대입을 쓰므로 하나만 허용한다.
	decls := regexp.MustCompile(`(?m)^ENVTEST_K8S_VERSION\s*[:?+]?=\s*(.+)$`).FindAllStringSubmatch(string(raw), -1)
	if len(decls) != 1 {
		t.Fatalf("Makefile 의 ENVTEST_K8S_VERSION 선언이 %d개다 — 하나여야 뒤의 선언이 앞의 고정을 가리지 않는다", len(decls))
	}
	m := decls[0]
	v := strings.TrimSpace(m[1])
	if !regexp.MustCompile(`^\d+\.\d+$`).MatchString(v) {
		t.Fatalf("ENVTEST_K8S_VERSION=%q — 숫자 버전을 명시해야 한다(식으로 두면 지원 범위 밖 서버로 시험한다)", v)
	}
	return v
}

// 증명: envtest 가 띄우는 API 서버 버전이 차트 kubeVersion 범위 안이다.
// 깨는 뮤테이션: ENVTEST_K8S_VERSION 을 1.33 으로 바꾸거나 식으로 되돌리면 실패한다.
func TestEnvtestServerVersionWithinChartRange(t *testing.T) {
	decl, floor, ceil := chartKubeVersionBounds(t)
	v := envtestK8sVersion(t)
	got, err := minorOf(v)
	if err != nil {
		t.Fatalf("ENVTEST_K8S_VERSION %q 해석 실패: %v", v, err)
	}
	if got < floor || got >= ceil {
		t.Fatalf("envtest API 서버 %s 가 차트 kubeVersion %q 범위 밖이다 — 지원 범위 안 서버로 시험해야 한다", v, decl)
	}
}
