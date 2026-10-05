// ============================================================
// envtest_server_version_test.go: envtest 가 실제로 띄운 API 서버의 버전이 차트 kubeVersion 범위 안인지 Ginkgo 스펙으로 확인한다.
// 상세: 이 스펙이 보는 것은 띄운 서버가 차트 kubeVersion 범위 안인지뿐이다. 선언값
// (ENVTEST_K8S_VERSION)과 실제 바이너리가 같은지는 어느 게이트도 보지 않는다 —
// 범위 안에 있으면 선언과 실제가 어긋나도 통과한다. 범위 밖 바이너리(예: 1.33)로
// 띄우면 실패한다.
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
