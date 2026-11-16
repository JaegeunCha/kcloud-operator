// ============================================================
// chart_image_path_test.go: values 의 이미지 경로가 registry 와 조립됐을 때 실재하는 Harbor 경로가 되는지 고정한다.
// 상세: _helpers.tpl 의 kcloud-operator.image 는 "<global.registry>/<repo>:<tag>" 로 조립한다. values 파일이
// 스스로 일관된가만 보고, Harbor 에 그 태그가 실재하는지는 레지스트리 조회의 몫이다(네트워크를 타지 않는다).
// 생성일: 2026-08-07 | 수정일: 2026-11-16
// ============================================================
package controller

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// valuesScalar 는 values.yaml 에서 키 하나의 스칼라 값을 읽는다. 들여쓰기 깊이는
// 보지 않으므로 파일 전체에서 유일한 키에만 쓴다.
func valuesScalar(t *testing.T, key string) string {
	t.Helper()
	raw, err := os.ReadFile("../../deploy/helm/values.yaml")
	if err != nil {
		t.Fatalf("values.yaml 읽기 실패: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `:\s*(\S+)\s*$`)
	m := re.FindAllSubmatch(raw, -1)
	if len(m) == 0 {
		t.Fatalf("values.yaml 에 %s 가 없다", key)
	}
	if len(m) > 1 {
		t.Fatalf("values.yaml 의 %s 가 %d 곳이다 — 이 헬퍼로는 못 고른다", key, len(m))
	}
	return strings.Trim(string(m[0][1]), `"'`)
}

// TestNestedFuriosaRepoPathsArePreserved 는 Furiosa 통합 이미지의 kcloud/ 접두가 지워지지 않았는지 본다.
// global.registry 가 프로젝트 경로(.../kcloud)까지 포함하므로 repo 는 레지스트리 상대경로여야 하는데, 이
// 이미지만 kcloud/ 를 또 달아 최종 경로가 .../kcloud/kcloud/... 가 된다. 오타처럼 보이지만 Harbor 에
// 올라가 있는 경로는 이쪽뿐이라 접두를 지우면 그 순간 ImagePullBackOff 다. 접두를 빼는 것은 정리가 아니라
// 회귀다 — 접두 없는 경로에 이미지를 먼저 올린 뒤에만 뺄 수 있고, 그때는 이 시험도 같이 고친다.
// RNGD 는 공개 벤더 이미지(furiosaai/furiosa-device-plugin)를 쓰므로 목록에 없다.
func TestNestedFuriosaRepoPathsArePreserved(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/helm/values.yaml")
	if err != nil {
		t.Fatalf("values.yaml 읽기 실패: %v", err)
	}
	for _, want := range []string{
		`devicePluginRepository: "kcloud/furiosa-unified-device-plugin"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("values.yaml 에서 사라졌다: %s\n"+
				"Harbor 에 실재하는 경로는 registry 의 kcloud 프로젝트 아래 kcloud/ 가 한 번 더 있는 쪽이다. "+
				"접두를 뺐다면 한 겹 경로에 이미지를 먼저 올렸는지 확인할 것", want)
		}
	}
}

// TestOtherRepoPathsAreRegistryRelative 는 나머지 repository 필드가 registry 상대경로를
// 유지하는지 본다. 위 예외를 보고 다른 필드에도 프로젝트 경로를 붙이면
// 그쪽은 Harbor 에 없는 경로가 된다.
func TestOtherRepoPathsAreRegistryRelative(t *testing.T) {
	for _, key := range []string{"kubectlRepository"} {
		got := valuesScalar(t, key)
		if strings.HasPrefix(got, "kcloud/") {
			t.Errorf("%s = %q — registry 가 이미 kcloud 프로젝트까지 가리키므로 접두가 겹친다", key, got)
		}
	}
	// 드라이버 이미지들은 repository 키가 벤더마다 반복되므로 한꺼번에 본다.
	raw, err := os.ReadFile("../../deploy/helm/values.yaml")
	if err != nil {
		t.Fatalf("values.yaml 읽기 실패: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s+repository:\s*"?(kcloud/\S+?)"?\s*$`)
	if m := re.FindSubmatch(raw); m != nil {
		t.Errorf("repository 에 kcloud/ 접두가 붙었다: %q — registry 와 겹친다", string(m[1]))
	}
}
