package node

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const PopExecutablePath = "/usr/local/bin/pop"

type ReleaseDetails struct {
	AssetsURL string `json:"assets_url"`
}

type ReleaseUpdate struct {
	Action  string
	Release ReleaseDetails
}

type Asset struct {
	URL string `json:"browser_download_url"`
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

func upgradeHandler(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// return the string response containing the request body
		var f ReleaseUpdate

		fmt.Println("==> (", time.Now().UTC(), ") ❔ Release event.")

		reqBody, err := ioutil.ReadAll(r.Body)
		if err != nil {
			log.Error().Err(err).Msg("could not read request body")
			return
		}

		ok := VerifySignature(string(reqBody), r.Header.Get("X-Hub-Signature-256"), secret)
		if !ok {
			fmt.Println("==> (", time.Now().UTC(), ") 🗝  Signatures did not match ! ")
			return
		}
		// return the string response containing the request body
		json.Unmarshal(reqBody, &f)

		// verify that a new release created
		if f.Action != "published" && f.Action != "created" {
			fmt.Println("==> (", time.Now().UTC(), ") ❌ Not a new release.")
			return
		}
		fmt.Println("==> (", time.Now().UTC(), ") 🚀 New release was created.")
		var assets []Asset

		// get the URL to download the new release assets
		res, err := http.Get(f.Release.AssetsURL)
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
		json.Unmarshal(respBody, &assets)

		// fetch the asset that matches the system's OS and architecture
		for _, a := range assets {
			if strings.Contains(a.URL, "pop-"+runtime.GOARCH+"-"+runtime.GOOS) {
				fmt.Println("==> (", time.Now().UTC(), ") 🔎 Found a relevant asset.")

				// launch a goroutine to download revelevant release file
				err = DownloadFile(PopExecutablePath, a.URL)
				if err != nil {
					log.Error().Err(err).Msg("could not download release")
					return
				}
				fmt.Println("==> (", time.Now().UTC(), ") ⬇️  Downloaded new asset.")

				// TODO: restart POP
			}
		}
	})
}
