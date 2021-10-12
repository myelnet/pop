package node

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"github.com/rs/zerolog/log"
)

const (
	ReleaseURL      = "https://api.github.com/repos/myelnet/pop/releases"
	ReleaseWorkflow = "tagged-release"
	ReleaseFile     = "./latestrelease.json"
)

var (
	popArgs []string
	popEnvs []string
	popPath string
)

type ReleaseDetails struct {
	AssetsURL string `json:"assets_url"`
	ID        int    `json:"id"`
}

type WorkflowDetails struct {
	Name string `json:"name"`
	ID   int    `json:"id"`
}

type ReleaseUpdate struct {
	Action   string `json:"action"`
	Workflow WorkflowDetails
}

type Asset struct {
	URL string `json:"browser_download_url"`
}

func init() {
	popArgs = os.Args
	popEnvs = os.Environ()
	popPath, _ = os.Executable()
}

func GetLatestRelease() (ReleaseDetails, error) {
	var release ReleaseDetails
	content, err := ioutil.ReadFile(ReleaseFile)
	if err != nil {
		return release, err
	}
	// get the release details
	json.Unmarshal(content, &release)

	return release, nil
}

func SaveLatestRelease(release ReleaseDetails) error {
	// convert release to JSON
	releaseJson, err := json.Marshal(release)
	if err != nil {
		return err
	}
	// write to file
	err = ioutil.WriteFile(ReleaseFile, releaseJson, 0644)
	if err != nil {
		return err
	}
	return nil
}

func VerifySignature(payload string, requestSignature string, secret string) bool {
	// make sure GITHUB_WEBHOOK_SECRET matches that of your github webhook
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))

	// Get result and encode as hexadecimal string
	signature := "sha256=" + hex.EncodeToString(h.Sum(nil))

	// Replace with constant time compare
	return (signature == requestSignature)
}

func DownloadFile(filepath string, url string) error {
	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	return err
}

// RestartByExec calls `syscall.Exec()` to restart app
func RestartByExec() {
	// look for pop binary
	binary, err := exec.LookPath(popPath)
	if err != nil {
		log.Error().Err(err).Msg("could not find binary")
		return
	}
	// restart with current args and env variables
	execErr := syscall.Exec(binary, popArgs, popEnvs)
	if execErr != nil {
		log.Error().Err(err).Msg("could not restart pop")
	}
}

func upgradeHandler(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return the string response containing the request body
		var f ReleaseUpdate

		log.Info().Msg("❔ Release event.")

		reqBody, err := ioutil.ReadAll(r.Body)
		if err != nil {
			log.Error().Err(err).Msg("could not read request body")
			return
		}

		ok := VerifySignature(string(reqBody), r.Header.Get("X-Hub-Signature-256"), secret)
		if !ok {
			log.Info().Msg("🗝  Signatures did not match !")
			return
		}
		// return the string response containing the request body
		json.Unmarshal(reqBody, &f)
		// verify that a new release created
		if f.Workflow.Name != ReleaseWorkflow || f.Action != "completed" {
			log.Info().Msg("❌ Not a new release.")
			return
		}

		log.Info().Msg("🚀 New workflow was created.")

		var release ReleaseDetails
		// get the latest release assets
		res, err := http.Get(ReleaseURL + "/latest")
		if err != nil {
			log.Error().Err(err).Msg("could not get release URL")
			return
		}

		// parse response
		respBody, err := ioutil.ReadAll(res.Body)
		if err != nil {
			log.Error().Err(err).Msg("could not read response body")
			return
		}
		// get the releases asset URLs
		json.Unmarshal(respBody, &release)

		latestRelease, err := GetLatestRelease()
		if err != nil {
			log.Info().Msg("💾 Could not load latest release record, using defaults.")
		}

		if latestRelease.ID == release.ID {
			log.Info().Msg("👾 Workflow didn't update main release.")
			return
		}

		var assets []Asset
		// get the URL to download the new release assets
		res, err = http.Get(release.AssetsURL)
		if err != nil {
			log.Error().Err(err).Msg("could not get release URL")
			return
		}

		// parse response
		respBody, err = ioutil.ReadAll(res.Body)
		if err != nil {
			log.Error().Err(err).Msg("could not read response body")
			return
		}
		json.Unmarshal(respBody, &assets)

		// fetch the asset that matches the system's OS and architecture
		for _, a := range assets {
			if strings.Contains(a.URL, "pop-"+runtime.GOARCH+"-"+runtime.GOOS) {
				log.Info().Msg("🔎 Found a relevant asset.")
				// remove any old temp files
				cmd := exec.Command("rm", "-f", popPath+"_temp")
				err = cmd.Run()
				if err != nil {
					log.Error().Err(err).Msg("could not remove old temp file")
				}

				// launch a goroutine to download release file to temp file
				err = DownloadFile(popPath+"_temp", a.URL)
				if err != nil {
					log.Error().Err(err).Msg("could not download release")
					return
				}
				log.Info().Msg("⬇️  Downloaded new asset.")

				// swap out temp file for current executable
				installCmd := exec.Command("install", "-C", popPath+"_temp", popPath)
				err = installCmd.Run()
				if err != nil {
					log.Error().Err(err).Msg("could not install new release")
				}
				log.Info().Msg("⬇️  Installed new asset.")

				SaveLatestRelease(release)

				RestartByExec()

			}
		}
	})
}
