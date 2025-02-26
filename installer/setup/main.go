package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tcnksm/go-input"
)

var config struct {
	rack       int
	proxy      string
	httpClient *http.Client
	clusters   []Cluster
	cluster    *Cluster
}

var ui = input.DefaultUI()

func sanity() error {
	if os.Getuid() != 0 {
		return errors.New("run as root")
	}

	myself, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(myself), "cluster.json"))
	if err != nil {
		return fmt.Errorf("could not load cluster.json: %w", err)
	}
	err = json.Unmarshal(data, &config.clusters)
	if err != nil {
		return fmt.Errorf("invalid cluster.json: %w", err)
	}

	return nil
}

func configure() error {
	config.proxy = os.Getenv("http_proxy")
	if config.proxy == "" {
		validate := func(s string) error {
			s = strings.TrimSpace(s)
			if _, err := url.Parse(s); err != nil {
				return err
			}
			config.proxy = s
			return nil
		}

		_, err := ui.Ask("proxy URL", &input.Options{
			Required:     true,
			Loop:         true,
			Mask:         false,
			ValidateFunc: validate,
		})
		if err != nil {
			return err
		}
	}

	u, err := url.Parse(config.proxy)
	if err != nil {
		return fmt.Errorf("invalid URL %s: %w", config.proxy, err)
	}

	// Most of the following values are copied from http.DefaultTransport to workaround a proxy issue.
	// See: https://github.com/golang/go/issues/25793
	tr := &http.Transport{
		Proxy: http.ProxyURL(u),
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	config.httpClient = &http.Client{
		Transport: tr,
	}

	var i int
	if len(config.clusters) > 1 {
		fmt.Println("Choose the cluster of this server:")
		for i := range config.clusters {
			fmt.Printf("%d) %s\n", i, config.clusters[i].Name)
		}

		validate := func(s string) error {
			ans, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return err
			}
			if ans < 0 {
				return errors.New("wrong value")
			}
			if ans >= len(config.clusters) {
				return errors.New("wrong value")
			}
			i = ans
			return nil
		}

		_, err := ui.Ask(fmt.Sprintf("Input a number [0-%d]", len(config.clusters)-1), &input.Options{
			Required:     true,
			Loop:         true,
			Mask:         false,
			ValidateFunc: validate,
		})
		if err != nil {
			return err
		}
	}
	config.cluster = &config.clusters[i]

	return nil
}

func checkConnectivity() error {
	fmt.Fprintln(os.Stderr, "Checking Internet connectivity...")
	for i := 0; i < 30; i++ {
		resp, err := config.httpClient.Get("http://www.cybozu.com")
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil
	}

	fmt.Fprintln(os.Stderr, "\nFailed to connect to the Internet.")
	_, err := ui.Ask("Press Enter to continue", &input.Options{})
	return err
}

func main() {
	if err := subMain(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

}

func subMain() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: %s LRN", os.Args[0])
	}
	lrn, err := strconv.Atoi(os.Args[1])
	if err != nil {
		return fmt.Errorf("invalid LRN: %w", err)
	}
	if lrn < 0 {
		return fmt.Errorf("invalid LRN: %d", lrn)
	}

	if err := sanity(); err != nil {
		return err
	}

	if err := configure(); err != nil {
		return err
	}

	hostname := fmt.Sprintf("%s-boot-%d", config.cluster.Name, lrn)
	if err := runCmd("hostnamectl", "set-hostname", hostname); err != nil {
		return err
	}
	if err := runCmd("sed", "-i", fmt.Sprintf("s/ boot/ %s/", hostname), "/etc/hosts"); err != nil {
		return err
	}

	if err := purgePackages(); err != nil {
		return err
	}

	if err := dumpNecoFiles(lrn); err != nil {
		return err
	}

	if err := setupNetwork(lrn); err != nil {
		return err
	}

	if err := checkConnectivity(); err != nil {
		return err
	}

	if err := runCmd("sed", "-i", "s/archive.ubuntu.com/linux.yz.yamagata-u.ac.jp/g", "/etc/apt/sources.list"); err != nil {
		return err
	}

	if err := setupDocker(); err != nil {
		return err
	}

	if err := runCmd("adduser", "cybozu", "docker"); err != nil {
		return err
	}

	if err := installPackages(installList...); err != nil {
		return err
	}
	if err := installChromium(); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "Upgrading packages...")
	// Upgrading cloud-init triggers a cloud-init run again. The second run changes hostname unexpectedly and causes
	// the serf startup failure. Hold the cloud-init version here to avoid it.
	if err := runCmd("apt-mark", "hold", "cloud-init"); err != nil {
		return err
	}
	if err := runCmd("apt-get", "-y", "dist-upgrade"); err != nil {
		return err
	}

	if err := runCmd("apt-get", "clean"); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "\nDone!")
	return nil
}
