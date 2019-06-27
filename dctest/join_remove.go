package dctest

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/cybozu-go/log"
	"github.com/cybozu-go/neco"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// TestJoinRemove test boot server join/remove scenario
func TestJoinRemove() {
	It("copies root CA certificate from existing server", func() {
		stdout, _, err := execAt(boot0, "cat", neco.ServerCAFile)
		Expect(err).ShouldNot(HaveOccurred())
		_, _, err = execAtWithInput(boot3, stdout, "sudo", "tee", neco.ServerCAFile)
		Expect(err).ShouldNot(HaveOccurred())
		_, _, err = execAt(boot3, "sudo", "update-ca-certificates")
		Expect(err).ShouldNot(HaveOccurred())
	})

	It("should add a new boot server", func() {
		// for upgrading test; install the same version of Neco as upgraded boot servers.
		remoteFilename := filepath.Join("/tmp", filepath.Base(debFile))
		stdout, stderr, err := execAt(boot3, "sudo", "dpkg", "-i", remoteFilename)
		Expect(err).NotTo(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)

		token := getVaultToken()
		stdout, stderr, err = execAt(
			boot3, "sudo", "env", "VAULT_TOKEN="+token, "neco", "join", "0", "1", "2")
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

		execSafeAt(boot3, "test", "-f", "/lib/systemd/system/neco-updater.service")
		execSafeAt(boot3, "test", "-f", "/lib/systemd/system/neco-worker.service")
		execSafeAt(boot3, "test", "-f", "/lib/systemd/system/node-exporter.service")
		execSafeAt(boot3, "test", "-f", "/lib/systemd/system/sabakan-state-setter.service")
		execSafeAt(boot3, "test", "-f", "/lib/systemd/system/sabakan-state-setter.timer")

		execSafeAt(boot3, "systemctl", "-q", "is-active", "neco-updater.service")
		execSafeAt(boot3, "systemctl", "-q", "is-active", "neco-worker.service")
		execSafeAt(boot3, "systemctl", "-q", "is-active", "node-exporter.service")
		execSafeAt(boot3, "systemctl", "-q", "is-active", "sabakan-state-setter.timer")
		execSafeAt(boot3, "systemctl", "-q", "is-active", "etcd-backup.timer")
	})

	It("should install programs", func() {
		By("Waiting for request to complete")
		waitRequestComplete("members: [0 1 2 3]")

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

	It("should setup sabakan on boot-3", func() {
		stdout, stderr, err := execAt(
			boot3, "sudo", "env", "VAULT_TOKEN="+getVaultToken(), "neco", "init-local", "sabakan")
		if err != nil {
			log.Error("neco init-local sabakan", map[string]interface{}{
				"host":   boot3,
				"stdout": string(stdout),
				"stderr": string(stderr),
			})
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("should setup boot-3 hardware", func() {
		Eventually(func() error {
			stdout, stderr, err := execAt(boot3, "sudo", "neco", "bmc", "setup-hw")
			if err != nil {
				return fmt.Errorf("neco bmc setup-hw failed; host: %s, err: %s, stdout: %s, stderr: %s", boot3, err, stdout, stderr)
			}
			return nil
		}).Should(Succeed())
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

	It("should set state of boot-3 to healthy", func() {
		By("Checking boot-3 machine state")
		Eventually(func() error {
			serial := fmt.Sprintf("%x", sha1.Sum([]byte("boot-3")))
			stdout := execSafeAt(boot0, "sabactl", "machines", "get-state", serial)
			state := string(bytes.TrimSpace(stdout))
			if state != "healthy" {
				return errors.New("boot-3 machine state is not healthy: " + state)
			}
			return nil
		}).Should(Succeed())
	})

	It("should be healthy even if boot-3 has been rebooted", func() {
		// This test scenario is added from this bug issue https://github.com/cybozu-go/neco/issues/333
		By("rebooting boot-3")
		birdSystemdConf := `
[Unit]
Description=bird
Wants=network-online.target
After=network.target network-online.target

[Service]
ExecStartPre=/bin/sleep 30
Slice=machine.slice
CPUSchedulingPolicy=rr
CPUSchedulingPriority=50
Type=simple
KillMode=mixed
Restart=on-failure
RestartForceExitStatus=SIGPIPE
OOMScoreAdjust=-1000
ExecStart=/usr/bin/rkt run \
  --volume run,kind=empty,readOnly=false \
  --volume etc,kind=host,source=/etc/bird,readOnly=true \
  --net=host \
  quay.io/cybozu/bird:2.0 \
    --readonly-rootfs=true \
    --caps-retain=CAP_NET_ADMIN,CAP_NET_BIND_SERVICE,CAP_NET_RAW \
    --name bird \
    --mount volume=run,target=/run/bird \
    --mount volume=etc,target=/etc/bird \
  quay.io/cybozu/ubuntu-debug:18.04 \
    --readonly-rootfs=true \
    --name ubuntu-debug

[Install]
WantedBy=multi-user.target`
		stdout, stderr, err := execAtWithInput(boot3, []byte(birdSystemdConf), "sudo", "tee", "/etc/systemd/system/bird.service")
		Expect(err).NotTo(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)

		serial := fmt.Sprintf("%x", sha1.Sum([]byte("boot-3")))
		_ = execSafeAt(boot0, "neco", "ipmipower", "restart", serial)
		Eventually(func() error {
			stdout, stderr, err := execAt(boot0, "sabactl", "machines", "get-state", serial)
			if err != nil {
				return fmt.Errorf("stdout=%s, stderr=%s, err=%v", stdout, stderr, err)
			}
			state := string(bytes.TrimSpace(stdout))
			if state != "healthy" {
				return errors.New("boot-3 machine state is not healthy: " + state)
			}
			// debug
			var results []serfMemberContainer

			err = prepareSSHClients(boot3)
			if err != nil {
				return err
			}

			Expect(err).NotTo(HaveOccurred())
			for _, boot := range []string{boot0, boot3} {
				stdout, stderr, err = execAt(boot, "serf", "members", "-format", "json")
				if err != nil {
					return fmt.Errorf("stdout=%s, stderr=%s, err=%v", stdout, stderr, err)
				}

				result := serfMemberContainer{}
				err = json.Unmarshal(stdout, &result)
				if err != nil {
					return err
				}
				results = append(results, result)
			}
			if len(results[0].Members) != len(results[1].Members) {
				return fmt.Errorf(`number of members is different between boot servers. len(results[0].Members) is %d, len(results[1].Members) is %d`,
					len(results[0].Members), len(results[1].Members))
			}
			return nil
		}).Should(Succeed())
	})

	It("should remove boot-3", func() {
		By("Running neco leave 3")
		token := getVaultToken()
		execSafeAt(boot0, "sudo", "env", "VAULT_TOKEN="+token, "neco", "leave", "3")

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

		// need to wait for boot-1/2 to restart etcd, or the system would become
		// unstable during tests.
		time.Sleep(3 * time.Minute)
	})

	It("should set state of boot-3 to unreachable", func() {
		By("Stopping boot-3")
		// In DCtest on CircleCI, ginkgo is executed in the operation pod, so you cannot use pmctl in this context.
		// This error is ignored deliberately, because this SSH session is closed by remote host when it is succeeded.
		stdout, stderr, _ := execAt(boot3, "sudo", "shutdown", "now")
		log.Info("boot-3 is stopped", map[string]interface{}{
			"stdout": string(stdout),
			"stderr": string(stderr),
		})

		By("Checking boot-3 machine state")
		Eventually(func() error {
			serial := fmt.Sprintf("%x", sha1.Sum([]byte("boot-3")))
			stdout := execSafeAt(boot0, "sabactl", "machines", "get-state", serial)
			state := string(bytes.TrimSpace(stdout))
			if state != "unreachable" {
				return errors.New("boot-3 machine state is not unreachable: " + state)
			}
			return nil
		}).Should(Succeed())
	})
}
