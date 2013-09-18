package hm

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/cloudfoundry/hm9000/testhelpers/etcdrunner"

	"testing"
)

var etcdRunner *etcdrunner.ETCDClusterRunner

func TestHM9000(t *testing.T) {
	RegisterFailHandler(Fail)

	etcdRunner = etcdrunner.NewETCDClusterRunner("etcd", 5001, 1)
	etcdRunner.Start()

	RunSpecs(t, "HM9000 CLI Suite")

	etcdRunner.Stop()
}

var _ = BeforeEach(func() {
	etcdRunner.Reset()
})