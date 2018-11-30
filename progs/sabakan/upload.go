package sabakan

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cybozu-go/log"
	"github.com/cybozu-go/neco"
	"github.com/cybozu-go/sabakan"
	"github.com/cybozu-go/sabakan/client"
)

const (
	endpoint = "http://127.0.0.1:10080"
	imageOS  = "coreos"
)

const retryCount = 5

// UploadContents upload contents to sabakan
func UploadContents(ctx context.Context, sabakanHTTP *http.Client, proxyHTTP *http.Client, proxyURL string, version string) error {
	client, err := client.NewClient(endpoint, sabakanHTTP)
	if err != nil {
		return err
	}

	err = uploadOSImages(ctx, client, proxyHTTP)
	if err != nil {
		return err
	}

	err = uploadAssets(ctx, client, proxyURL)
	if err != nil {
		return err
	}

	err = uploadIgnitions(ctx, client, version)
	if err != nil {
		return err
	}

	return nil
}

// uploadOSImages uploads CoreOS images
func uploadOSImages(ctx context.Context, c *client.Client, p *http.Client) error {
	var index sabakan.ImageIndex
	err := neco.RetryWithSleep(ctx, retryCount, 10*time.Second,
		func(ctx context.Context) error {
			var err error
			index, err = c.ImagesIndex(ctx, imageOS)
			return err
		},
		func(err error) {
			log.Warn("sabakan: failed to get index of CoreOS images", map[string]interface{}{
				log.FnError: err,
			})
		},
	)
	if err != nil {
		return err
	}

	version := neco.CurrentArtifacts.CoreOS.Version
	if len(index) != 0 && index[len(index)-1].ID == version {
		return nil
	}

	kernelURL, initrdURL := neco.CurrentArtifacts.CoreOS.URLs()

	kernelFile, err := ioutil.TempFile("", "kernel")
	if err != nil {
		return err
	}
	defer func() {
		kernelFile.Close()
		os.Remove(kernelFile.Name())
	}()
	initrdFile, err := ioutil.TempFile("", "initrd")
	if err != nil {
		return err
	}
	defer func() {
		initrdFile.Close()
		os.Remove(initrdFile.Name())
	}()

	var kernelSize int64
	err = neco.RetryWithSleep(ctx, retryCount, 10*time.Second,
		func(ctx context.Context) error {
			err := kernelFile.Truncate(0)
			if err != nil {
				return err
			}
			_, err = kernelFile.Seek(0, 0)
			if err != nil {
				return err
			}
			kernelSize, err = downloadFile(ctx, p, kernelURL, kernelFile)
			return err
		},
		func(err error) {
			log.Warn("sabakan: failed to fetch Container Linux kernel", map[string]interface{}{
				log.FnError: err,
				"url":       kernelURL,
			})
		},
	)
	if err != nil {
		return err
	}
	_, err = kernelFile.Seek(0, 0)
	if err != nil {
		return err
	}

	var initrdSize int64
	err = neco.RetryWithSleep(ctx, retryCount, 10*time.Second,
		func(ctx context.Context) error {
			err := initrdFile.Truncate(0)
			if err != nil {
				return err
			}
			_, err = initrdFile.Seek(0, 0)
			if err != nil {
				return err
			}
			initrdSize, err = downloadFile(ctx, p, initrdURL, initrdFile)
			return err
		},
		func(err error) {
			log.Warn("sabakan: failed to fetch Container Linux initrd", map[string]interface{}{
				log.FnError: err,
				"url":       initrdURL,
			})
		},
	)

	if err != nil {
		return err
	}

	_, err = initrdFile.Seek(0, 0)
	if err != nil {
		return err
	}

	err = neco.RetryWithSleep(ctx, retryCount, 10*time.Second,
		func(ctx context.Context) error {
			return c.ImagesUpload(ctx, imageOS, version, kernelFile, kernelSize, initrdFile, initrdSize)
		},
		func(err error) {
			log.Warn("sabakan: failed to upload Container Linux", map[string]interface{}{
				log.FnError: err,
			})
		},
	)
	return err
}

// uploadAssets uploads assets
func uploadAssets(ctx context.Context, c *client.Client, proxyURL string) error {
	// Upload bird and chorny
	var fetches []neco.ContainerImage
	fetches = append(fetches, neco.SystemContainers...)

	for _, name := range []string{"omsa", "serf"} {
		img, err := neco.CurrentArtifacts.FindContainerImage(name)
		if err != nil {
			return err
		}
		fetches = append(fetches, img)
	}

	for _, img := range fetches {
		err := uploadImageAssets(ctx, img, c, proxyURL)
		if err != nil {
			return err
		}
	}

	// Upload sabakan with version name
	img, err := neco.CurrentArtifacts.FindContainerImage("sabakan")
	if err != nil {
		return err
	}
	err = neco.RetryWithSleep(ctx, retryCount, 10*time.Second,
		func(ctx context.Context) error {
			_, err := c.AssetsUpload(ctx, neco.CryptsetupAssetName(img.Tag), neco.SabakanCryptsetupPath, nil)
			return err

		},
		func(err error) {
			log.Warn("sabakan: failed to upload asset", map[string]interface{}{
				log.FnError: err,
				"name":      neco.CryptsetupAssetName(img.Tag),
				"source":    neco.SabakanCryptsetupPath,
			})
		},
	)
	return err
}

func uploadImageAssets(ctx context.Context, img neco.ContainerImage, c *client.Client, proxyURL string) error {
	env := neco.HTTPProxyEnv(proxyURL)
	err := neco.FetchContainer(ctx, img.FullName(), env)
	if err != nil {
		return err
	}
	f, err := ioutil.TempFile("", "")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = neco.ExportContainer(ctx, img.FullName(), f.Name())
	if err != nil {
		return err
	}
	err = neco.RetryWithSleep(ctx, retryCount, 10*time.Second,
		func(ctx context.Context) error {
			_, err := c.AssetsUpload(ctx, neco.ImageAssetName(img), f.Name(), nil)
			return err

		},
		func(err error) {
			log.Warn("sabakan: failed to upload asset", map[string]interface{}{
				log.FnError: err,
				"name":      neco.ImageAssetName(img),
				"source":    f.Name(),
			})
		},
	)
	return err
}

// uploadIgnitions updates ignitions from local file
func uploadIgnitions(ctx context.Context, c *client.Client, id string) error {
	roles, err := getInstalledRoles()
	if err != nil {
		return err
	}

	for _, role := range roles {
		path := ignitionPath(role)

		newer := new(bytes.Buffer)
		err := client.AssembleIgnitionTemplate(path, newer)
		if err != nil {
			return err
		}

		need, err := needIgnitionUpdate(ctx, c, role, id, newer.String())
		if err != nil {
			return err
		}
		if !need {
			continue
		}
		err = c.IgnitionsSet(ctx, role, id, newer, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func needIgnitionUpdate(ctx context.Context, c *client.Client, role, id string, newer string) (bool, error) {
	index, err := c.IgnitionsGet(ctx, role)
	if client.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	latest := index[len(index)-1].ID
	if latest == id {
		return false, nil
	}

	current := new(bytes.Buffer)
	err = c.IgnitionsCat(ctx, role, latest, current)
	if err != nil {
		return false, err
	}
	return current.String() != newer, nil
}

func getInstalledRoles() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(neco.IgnitionDirectory, "*", "site.yml"))
	if err != nil {
		return nil, err
	}
	for i, path := range paths {
		paths[i] = filepath.Base(filepath.Dir(path))
	}
	return paths, nil
}

func ignitionPath(role string) string {
	return filepath.Join(neco.IgnitionDirectory, role, "site.yml")
}

func downloadFile(ctx context.Context, p *http.Client, url string, w io.Writer) (int64, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}
	req = req.WithContext(ctx)
	resp, err := p.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.ContentLength <= 0 {
		return 0, errors.New("unknown content-length")
	}
	return io.Copy(w, resp.Body)
}
