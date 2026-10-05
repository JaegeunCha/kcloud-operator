// ============================================================
// envtest_server_version_test.go: envtest 가 실제로 띄운 API 서버의 버전을 차트
//
//	kubeVersion 범위와 대조한다.
//
// 상세: TestEnvtestServerVersionWithinChartRange(chart_version_sync_test.go)는 Makefile
//
//	의 ENVTEST_K8S_VERSION 문자열만 본다 — TEST_ASSET_KUBE_APISERVER 등으로 그 선언과
//	다른 바이너리를 띄우면 그 어긋남을 잡지 못한다. 이 스펙은 BeforeSuite 가 이미 띄운
//	API 서버에 discovery 로 직접 물어 실제 버전을 대조한다.
//
// 생성일: 2026-10-06
// ============================================================
package controller

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/discovery"
)

var _ = Describe("envtest API 서버 버전", func() {
	It("차트 kubeVersion 범위 안에 있다", func() {
		decl, err := readChartKubeVersion()
		Expect(err).NotTo(HaveOccurred())
		floor, ceil, err := kubeVersionBounds(decl)
		Expect(err).NotTo(HaveOccurred())

		dc, err := discovery.NewDiscoveryClientForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		sv, err := dc.ServerVersion()
		Expect(err).NotTo(HaveOccurred())

		// Minor 에 "28+" 처럼 skew 표식이 붙을 수 있다 — minorOf 는 숫자만 받는다.
		major := strings.TrimSuffix(sv.Major, "+")
		minor := strings.TrimSuffix(sv.Minor, "+")
		got, err := minorOf(major + "." + minor)
		Expect(err).NotTo(HaveOccurred())

		Expect(got).To(BeNumerically(">=", floor),
			"envtest API 서버 %s.%s 가 차트 kubeVersion %q 하한 밖이다", sv.Major, sv.Minor, decl)
		Expect(got).To(BeNumerically("<", ceil),
			"envtest API 서버 %s.%s 가 차트 kubeVersion %q 상한 밖이다", sv.Major, sv.Minor, decl)
	})
})
