package dctest

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/cybozu-go/log"
	"github.com/cybozu-go/neco"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func testJoinRemove() {
	var rootToken string
	It("should get root token", func() {
		stdout, _, err := execAt(boot0, "neco", "vault", "show-root-token")
		Expect(err).ShouldNot(HaveOccurred())
		rootToken = string(bytes.TrimSpace(stdout))
		Expect(rootToken).NotTo(BeEmpty())
	})

	It("copies root CA certificate from existing server", func() {
		stdout, _, err := execAt(boot0, "cat", neco.ServerCAFile)
		Expect(err).ShouldNot(HaveOccurred())
		err = execAtWithInput(boot3, stdout, "sudo", "tee", neco.ServerCAFile)
		Expect(err).ShouldNot(HaveOccurred())
		_, _, err = execAt(boot3, "sudo", "update-ca-certificates")
		Expect(err).ShouldNot(HaveOccurred())
	})

	It("should add a new boot server", func() {
		stdout, stderr, err := execAt(
			boot3, "sudo", "env", "VAULT_TOKEN="+rootToken, "neco", "join", "0", "1", "2")
		if err != nil {
			log.Error("neco join failed", map[string]interface{}{
				"host":   "boot-3",
				"stdout": string(stdout),
				"stderr": string(stderr),
			})
			Expect(err).ShouldNot(HaveOccurred())
		}
		execSafeAt(boot3, "test", "-f", neco.NecoConfFile)
		execSafeAt(boot3, "test", "-f", neco.NecoCertFile)
		execSafeAt(boot3, "test", "-f", neco.NecoKeyFile)

		execSafeAt(boot3, "test", "-f", neco.EtcdBackupCertFile)
		execSafeAt(boot3, "test", "-f", neco.EtcdBackupKeyFile)
		execSafeAt(boot3, "test", "-f", neco.TimerFile("etcd-backup"))
		execSafeAt(boot3, "test", "-f", neco.ServiceFile("etcd-backup"))

		execSafeAt(boot3, "systemctl", "-q", "is-active", "neco-updater.service")
		execSafeAt(boot3, "systemctl", "-q", "is-active", "neco-worker.service")
	})

	It("should install programs", func() {
		By("Waiting for request to complete")
		waitRequestComplete()

		By("Waiting for etcd to be restarted on boot-0")
		time.Sleep(time.Second * 7)

		By("Checking etcd installation")
		_, _, err := execAt(boot3, "systemctl", "-q", "is-active", neco.EtcdService+".service")
		Expect(err).ShouldNot(HaveOccurred())
		_, _, err = execAt(boot3, "test", "-f", "/usr/local/bin/etcdctl")
		Expect(err).ShouldNot(HaveOccurred())
		By("Checking vault installation")
		_, _, err = execAt(boot3, "systemctl", "-q", "is-active", neco.VaultService+".service")
		Expect(err).ShouldNot(HaveOccurred())
	})

	It("should add boot-3 to etcd cluster", func() {
		stdout, _, err := execAt(boot0, "env", "ETCDCTL_API=3", "etcdctl", "-w", "json",
			"--cert=/etc/neco/etcd.crt", "--key=/etc/neco/etcd.key", "member", "list")
		Expect(err).ShouldNot(HaveOccurred())
		var mlr struct {
			Members []struct {
				Name string `json:"name"`
			} `json:"members"`
		}

		err = json.Unmarshal(stdout, &mlr)
		Expect(err).ShouldNot(HaveOccurred())

		names := make([]string, len(mlr.Members))
		for i, m := range mlr.Members {
			names[i] = m.Name
		}
		Expect(names).Should(ContainElement("boot-3"))
	})

	It("should remove boot-3", func() {
		By("Running neco leave 3")
		execSafeAt(boot0, "sudo", "env", "VAULT_TOKEN="+rootToken, "neco", "leave", "3")

		By("Waiting boot-3 gets removed from etcd")
		Eventually(func() error {
			stdout, _, err := execAt(boot0, "env", "ETCDCTL_API=3", "etcdctl", "-w", "json",
				"--cert=/etc/neco/etcd.crt", "--key=/etc/neco/etcd.key", "member", "list")
			if err != nil {
				return err
			}

			var mlr struct {
				Members []struct {
					Name string `json:"name"`
				} `json:"members"`
			}
			err = json.Unmarshal(stdout, &mlr)
			if err != nil {
				return err
			}

			for _, m := range mlr.Members {
				if m.Name == "boot-3" {
					return errors.New("boot-3 is not removed from etcd")
				}
			}
			return nil
		}).Should(Succeed())
	})
}
