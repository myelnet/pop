package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/docker/go-units"
	"github.com/myelnet/pop/internal/utils"
	"github.com/myelnet/pop/metrics"
	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v3"
	"github.com/peterbourgon/ff/v3/ffcli"
	"github.com/rs/zerolog/log"
)

// PopConfig is the json config object we generate with the init command
type PopConfig struct {
	temp          bool
	privKeyPath   string
	regions       string
	replInterval  time.Duration
	upgradeSecret string
	certmagic     bool
	// Exported fields can be set by survey.Ask
	Bootstrap     string `json:"bootstrap"`
	Capacity      string `json:"capacity"`
	MaxPPB        int    `json:"maxppb"`
	FilEndpoint   string `json:"fil-endpoint"`
	FilToken      string `json:"fil-token"`
	FilTokenType  string `json:"fil-token-type"`
	Domains       string `json:"domains"`
	IndexEndpoint string `json:"index-endpoint"`
}

var startArgs PopConfig

var startCmd = &ffcli.Command{
	Name:      "start",
	ShortHelp: "Starts a POP daemon",
	LongHelp: strings.TrimSpace(`

The 'pop start' command starts a pop daemon service.

`),
	Exec: runStart,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("start", flag.ExitOnError)
		fs.BoolVar(&startArgs.temp, "temp-repo", false, "create a temporary repo for debugging")
		fs.StringVar(&startArgs.Bootstrap, "bootstrap", "", "bootstrap peer to discover others (add multiple addresses separated by commas)")
		fs.StringVar(&startArgs.FilEndpoint, "fil-endpoint", "https://infura.myel.cloud", "endpoint to reach a filecoin api")
		fs.StringVar(&startArgs.FilToken, "fil-token", "", "token to authorize filecoin api access")
		fs.StringVar(&startArgs.FilTokenType, "fil-token-type", "Bearer", "auth token type")
		fs.StringVar(&startArgs.privKeyPath, "privkey", "", "path to private key to use by default")
		fs.StringVar(&startArgs.regions, "regions", "", "provider regions separated by commas")
		fs.StringVar(&startArgs.Capacity, "capacity", "100GB", "storage space allocated for the node")
		fs.DurationVar(&startArgs.replInterval, "replinterval", 0, "at which interval to check for new content from peers. 0 means the feature is deactivated")
		fs.StringVar(&startArgs.Domains, "domains", "", "comma separated list of domain names this pop can support")
		fs.IntVar(&startArgs.MaxPPB, "maxppb", 5, "max price per byte")
		fs.StringVar(&startArgs.IndexEndpoint, "index-endpoint", "", "endpoint of a hosted index service")
		fs.StringVar(&startArgs.upgradeSecret, "upgrade-secret", "", "secret used to verify upgrade message signatures, if provided the server will listen for github webhook request and automatically upgrade the pop")
		fs.BoolVar(&startArgs.certmagic, "certmagic", false, "run certmagic to get TLS certificates")

		return fs
	})(),
	Options: (func() []ff.Option {
		path, err := utils.FullPath(utils.RepoPath())
		if err != nil {
			path = ""
		}
		return []ff.Option{
			ff.WithConfigFile(filepath.Join(path, "PopConfig.json")),
			ff.WithConfigFileParser(ff.JSONParser),
			ff.WithAllowMissingConfigFile(true),
		}
	})(),
}

func runStart(ctx context.Context, args []string) error {
	fmt.Printf(`
. 　　   .  　 *  ✵ 　 　　 ✦
　 　　　　　
 ·  ✦  　 　　.  *  　　　　　　
    　.  ·  ·
  . ·   *  * ·  .
 ·　　 ·  ✧     　　 ·

ppppp   ppppppppp      ooooooooooo   ppppp   ppppppppp
p::::ppp:::::::::p   oo:::::::::::oo p::::ppp:::::::::p
p:::::::::::::::::p o:::::::::::::::op:::::::::::::::::p
pp::::::ppppp::::::po:::::ooooo:::::opp::::::ppppp::::::p
 p:::::p     p:::::po::::o     o::::o p:::::p     p:::::p
 p:::::p     p:::::po::::o     o::::o p:::::p     p:::::p
 p:::::p     p:::::po::::o     o::::o p:::::p     p:::::p
 p:::::p    p::::::po::::o     o::::o p:::::p    p::::::p
 p:::::ppppp:::::::po:::::ooooo:::::o p:::::ppppp:::::::p
 p::::::::::::::::p o:::::::::::::::o p::::::::::::::::p
 p::::::::::::::pp   oo:::::::::::oo  p::::::::::::::pp
 p::::::pppppppp       ooooooooooo    p::::::pppppppp
 p:::::p                              p:::::p
 p:::::p                              p:::::p
p:::::::p                            p:::::::p
p:::::::p                            p:::::::p
p:::::::p                            p:::::::p
ppppppppp                            ppppppppp

-----------------------------------------------------------
Manage your Myel point of presence from the command line.
-----------------------------------------------------------
`)

	path, err := setupRepo()
	if err != nil {
		return err
	}
	if startArgs.temp {
		defer os.RemoveAll(path)
	}

	privKey := setupWallet()

	regions := setupRegions()

	ctx, cancel := context.WithCancel(ctx)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	signal.Ignore(syscall.SIGPIPE)
	go func() {
		select {
		case s := <-interrupt:
			fmt.Printf("\nShutting down, reason: %s\n", s.String())
			cancel()
		case <-ctx.Done():
		}
	}()

	filToken := utils.FormatToken(startArgs.FilToken, startArgs.FilTokenType)

	var bAddrs []string
	if startArgs.Bootstrap != "" {
		startArgs.Bootstrap = strings.ReplaceAll(startArgs.Bootstrap, " ", "")
		mapDuplicates := make(map[string]struct{})
		bootstrapAddr := strings.Split(startArgs.Bootstrap, ",")

		// we ignore empty addresses & duplicates, then fill bAddrs with the clean address
		for _, addr := range bootstrapAddr {
			if addr == "" {
				continue
			}

			_, exists := mapDuplicates[addr]
			if exists {
				continue
			}

			mapDuplicates[addr] = struct{}{}
			bAddrs = append(bAddrs, addr)
		}
	}

	var capacity uint64
	if size, err := units.FromHumanSize(startArgs.Capacity); err == nil {
		capacity = uint64(size)
	} else {
		fmt.Println("failed to parse capacity")
	}

	var domains []string
	if startArgs.Domains != "" {
		domains = strings.Split(startArgs.Domains, ",")
	}

	opts := node.Options{
		RepoPath:       path,
		Metrics:        metrics.GetInfluxParams(),
		BootstrapPeers: bAddrs,
		FilEndpoint:    startArgs.FilEndpoint,
		FilToken:       filToken,
		PrivKey:        privKey,
		MaxPPB:         int64(startArgs.MaxPPB),
		Regions:        regions,
		Capacity:       capacity,
		ReplInterval:   startArgs.replInterval,
		Domains:        domains,
		Certmagic:      startArgs.certmagic,
		RemoteIndexURL: startArgs.IndexEndpoint,
		UpgradeSecret:  startArgs.upgradeSecret,
		CancelFunc:     cancel,
	}

	err = node.Run(ctx, opts)
	if err != nil && err != context.Canceled {
		log.Error().Err(err).Msg("node.Run")
		return err
	}

	return nil
}

// setupRepo will persist our initial configurations so we can remember them when we need to restart the node
// it will create a temporary repo if the flag is passed or a new repo if none exist yet.
func setupRepo() (string, error) {
	var err error
	path, err := utils.RepoPath()
	if err != nil {
		return path, err
	}

	exists, err := utils.RepoExists(path)
	if err != nil {
		return path, err
	}

	if startArgs.temp {
		path, err = os.MkdirTemp("", ".pop")
		if err != nil {
			return path, err
		}
		fmt.Printf("==> Created temporary repo\n")
		return path, nil
	}

	if exists {
		return path, nil
	}

	// Make our root repo dir and datastore dir
	err = os.MkdirAll(filepath.Join(path, "datastore"), 0755)
	if err != nil {
		return path, err
	}
	// default configs
	// Regions aren't set in a static config object as we aim to make them
	// more dynamic in the future
	buf := new(bytes.Buffer)
	e := json.NewEncoder(buf)
	e.SetIndent("", "    ")
	if err := e.Encode(startArgs); err != nil {
		return path, err
	}
	c, err := os.Create(filepath.Join(path, "PopConfig.json"))
	if err != nil {
		return path, err
	}
	_, err = c.Write(buf.Bytes())
	if err != nil {
		return path, err
	}
	if err := c.Close(); err != nil {
		return path, err
	}
	fmt.Printf("==> Initialized pop repo in %s\n", path)

	return path, nil
}

// setupWallet prompts user to import a key or generate a new one
func setupWallet() string {
	var privKey string
	if startArgs.privKeyPath != "" {
		fdata, err := os.ReadFile(startArgs.privKeyPath)
		if err != nil {
			log.Error().Err(err).Msg("failed to read private key")
		} else {
			privKey = strings.TrimSpace(string(fdata))
		}
	}

	return privKey
}

// setupRegions formats the regions to join from cli flag or user prompt
func setupRegions() []string {
	var regions []string
	if startArgs.regions != "" {
		regions = strings.Split(startArgs.regions, ",")
	}
	return regions
}
