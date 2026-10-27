// ============================================================
// driverinstallpolicy_trackonly_cel_test.go: trackOnly 정책의 driver.image 생략 CEL 검증을 envtest 로 고정한다.
// 상세: trackOnly=true 면 설치기 이미지 없이 생성되고, 설치 정책(trackOnly=false)의 image 누락은 거절된다.
// 생성일: 2026-10-27 | 수정일: 2026-10-27
// ============================================================
package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	npuv1alpha1 "kcloud-operator/api/v1alpha1"
)

// dipTrackOnlyNoImage 는 설치기 이미지가 없는 최소 DIP 다. trackOnly 만 케이스별로 다르다.
func dipTrackOnlyNoImage(name string, trackOnly bool) *npuv1alpha1.DriverInstallPolicy {
	return &npuv1alpha1.DriverInstallPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: npuv1alpha1.DriverInstallPolicySpec{
			Vendor: "rebellions",
			Model:  "atom",
			Driver: npuv1alpha1.DriverSpec{Mode: "job", TrackOnly: trackOnly, VersionSource: "Host"},
		},
	}
}

var _ = Describe("DriverInstallPolicy trackOnly 의 image 생략 CEL 검증", func() {
	It("trackOnly=true 면 image 없이 생성이 통과한다", func() {
		dip := dipTrackOnlyNoImage("cel-trackonly-noimage", true)
		Expect(k8sClient.Create(ctx, dip)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, dip) })
	})

	It("trackOnly=false 에 image 가 없으면 생성이 거절된다", func() {
		err := k8sClient.Create(ctx, dipTrackOnlyNoImage("cel-install-noimage", false))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("driver.image is required unless driver.trackOnly=true"))
	})
})
