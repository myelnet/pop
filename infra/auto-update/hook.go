package main

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "io"
    "io/ioutil"
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "net/http"
    "os"
    "os/exec"
    "strings"
    "time"
)

var oss = flag.String("os", "linux", "OS used")
var arch = flag.String("arch", "amd64", "architecture used")
var popPath = flag.String("pop-path", "/usr/local/bin/pop", "path to pop install")
var startCmd = flag.String("cmd", "./start-cmd.sh", "cmd to run when starting pop")

type ReleaseDetails struct {
  AssetsURL string `json:"assets_url"`
}

type ReleaseUpdate struct {
  Action string
  Release ReleaseDetails
}

type Asset struct {
  URL string `json:"browser_download_url"`
}

func VerifySignature(payload string, requestSignature string) bool {
  // make sure GITHUB_WEBHOOK_SECRET matches that of your github webhook
  h := hmac.New(sha256.New, []byte(os.Getenv("GITHUB_WEBHOOK_SECRET")))
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


func updatePOP(w http.ResponseWriter, r *http.Request){

    // return the string response containing the request body
    var f ReleaseUpdate

    fmt.Println("==> (", time.Now().UTC(),") ❔ Release event.")

    reqBody, _ := ioutil.ReadAll(r.Body)

    verification := VerifySignature(string(reqBody), r.Header.Get("X-Hub-Signature-256"))
    if verification {
      fmt.Println(string(reqBody))
      // return the string response containing the request body
      json.Unmarshal(reqBody, &f)

      // verify that a new release created
      if (f.Action == "published") || (f.Action == "created"){
        fmt.Println("==> (", time.Now().UTC(),") 🚀 New release was created.")
        var assets []Asset
        // get the URL to download the new release assets

        r, _ := http.Get(f.Release.AssetsURL)
        respBody, _ := ioutil.ReadAll(r.Body)
        json.Unmarshal(respBody, &assets)

        // fetch the asset that matches the system's OS and architecture
        for _, a := range assets {
          if strings.Contains(a.URL, "pop-" + *arch + "-" + *oss){
              fmt.Println("==> (", time.Now().UTC(),") 🔎 Found a relevant asset.")

              stopPop := exec.Command(*popPath, "off")
              err := stopPop.Run()
              if err != nil {
                  fmt.Println("==> (", time.Now().UTC(),") 💤 Pop was not running.")
              }

              // Launch a goroutine to download file
              err = DownloadFile(*popPath, a.URL)
              if err != nil {
            		panic(err)
            	}
              fmt.Println("==> (", time.Now().UTC(),") ⬇️  Downloaded new asset.")

              startPop := exec.Command(*startCmd)
              err = startPop.Run()
              if err != nil {
                  log.Fatal(err)
              }
              fmt.Println("==> (", time.Now().UTC(),") 🎉 Started pop.")

            }
        }
      } else {
        fmt.Println("==> (", time.Now().UTC(),") ❌ Not a new release.")
      }
    } else {
      fmt.Println("==> (", time.Now().UTC(),") 🗝  Signatures did not match ! ")
    }
}

func handleRequests() {
    http.HandleFunc("/", updatePOP)
    log.Fatal(http.ListenAndServe(":4567", nil))
}

func main() {
  fmt.Printf(`


PPPPPPPPPPPPPPPPP        OOOOOOOOO     PPPPPPPPPPPPPPPPP                    HHHHHHHHH     HHHHHHHHH                                  kkkkkkkk
P::::::::::::::::P     OO:::::::::OO   P::::::::::::::::P                   H:::::::H     H:::::::H                                  k::::::k
P::::::PPPPPP:::::P  OO:::::::::::::OO P::::::PPPPPP:::::P                  H:::::::H     H:::::::H                                  k::::::k
PP:::::P     P:::::PO:::::::OOO:::::::OPP:::::P     P:::::P                 HH::::::H     H::::::HH                                  k::::::k
P::::P     P:::::PO::::::O   O::::::O  P::::P     P:::::P                   H:::::H     H:::::H     ooooooooooo      ooooooooooo    k:::::k    kkkkkkk
P::::P     P:::::PO:::::O     O:::::O  P::::P     P:::::P                   H:::::H     H:::::H   oo:::::::::::oo  oo:::::::::::oo  k:::::k   k:::::k
P::::PPPPPP:::::P O:::::O     O:::::O  P::::PPPPPP:::::P                    H::::::HHHHH::::::H  o:::::::::::::::oo:::::::::::::::o k:::::k  k:::::k
P:::::::::::::PP  O:::::O     O:::::O  P:::::::::::::PP   ---------------   H:::::::::::::::::H  o:::::ooooo:::::oo:::::ooooo:::::o k:::::k k:::::k
P::::PPPPPPPPP    O:::::O     O:::::O  P::::PPPPPPPPP     -:::::::::::::-   H:::::::::::::::::H  o::::o     o::::oo::::o     o::::o k::::::k:::::k
P::::P            O:::::O     O:::::O  P::::P             ---------------   H::::::HHHHH::::::H  o::::o     o::::oo::::o     o::::o k:::::::::::k
P::::P            O:::::O     O:::::O  P::::P                               H:::::H     H:::::H  o::::o     o::::oo::::o     o::::o k:::::::::::k
P::::P            O::::::O   O::::::O  P::::P                               H:::::H     H:::::H  o::::o     o::::oo::::o     o::::o k::::::k:::::k
PP::::::PP          O:::::::OOO:::::::OPP::::::PP                           HH::::::H     H::::::HHo:::::ooooo:::::oo:::::ooooo:::::ok::::::k k:::::k
P::::::::P           OO:::::::::::::OO P::::::::P                           H:::::::H     H:::::::Ho:::::::::::::::oo:::::::::::::::ok::::::k  k:::::k
P::::::::P             OO:::::::::OO   P::::::::P                           H:::::::H     H:::::::H oo:::::::::::oo  oo:::::::::::oo k::::::k   k:::::k
PPPPPPPPPP               OOOOOOOOO     PPPPPPPPPP                           HHHHHHHHH     HHHHHHHHH   ooooooooooo      ooooooooooo   kkkkkkkk    kkkkkkk


-------------------------------------------
Auto-update your pop using github webhooks.
-------------------------------------------
      `)

    flag.Parse()

    fmt.Println("\n==> 🍏 OS: " + *oss)
    fmt.Println("==> 🖥️  Architecture: "+ *arch)
    fmt.Println("==> 🌎 Path: " + *popPath)
    fmt.Println("==> 💡 CMD: " + *startCmd)

    handleRequests()
}
