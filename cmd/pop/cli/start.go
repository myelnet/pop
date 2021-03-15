package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/myelnet/pop/node"
	"github.com/peterbourgon/ff/v2"
	"github.com/peterbourgon/ff/v2/ffcli"
	"github.com/rs/zerolog/log"
)

var startArgs struct {
	temp         bool
	bootstrap    string
	filEndpoint  string
	filToken     string
	filTokenType string
	privKeyPath  string
	regions      string
}

var startCmd = &ffcli.Command{
	Name:      "start",
	ShortHelp: "Starts an IPFS daemon",
	LongHelp: strings.TrimSpace(`

The 'pop start' command starts an IPFS daemon service.

`),
	Exec: runStart,
	FlagSet: (func() *flag.FlagSet {
		fs := flag.NewFlagSet("start", flag.ExitOnError)
		fs.BoolVar(&startArgs.temp, "temp-repo", false, "create a temporary repo for debugging")
		fs.StringVar(&startArgs.bootstrap, "bootstrap", "", "bootstrap peer to discover others")
		fs.StringVar(&startArgs.filEndpoint, "fil-endpoint", "", "endpoint to reach a filecoin api")
		fs.StringVar(&startArgs.filToken, "fil-token", "", "token to authorize filecoin api access")
		fs.StringVar(&startArgs.filTokenType, "fil-token-type", "Bearer", "auth token type")
		fs.StringVar(&startArgs.privKeyPath, "privkey", "", "path to private key to use by default")
		fs.StringVar(&startArgs.regions, "regions", "Global", "provider regions separated by commas")

		return fs
	})(),
	Options: (func() []ff.Option {
		path, err := repoFullPath(getRepoPath())
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

-------------------------------------------------------------
Manage your Myel CDN point of presence from the command line.
-------------------------------------------------------------
`)
	var err error
	path, err := repoFullPath(getRepoPath())
	if err != nil {
		return err
	}

	exists, err := repoExists(path)
	if err != nil {
		return err
	}

	if !exists || startArgs.temp {
		path, err = os.MkdirTemp("", ".pop")
		if err != nil {
			return err
		}
		defer os.RemoveAll(path)
		fmt.Printf("==> Created temp repo (To create a persistent repo run `pop init`)\n")
	}

	ctx, cancel := context.WithCancel(ctx)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	signal.Ignore(syscall.SIGPIPE)
	go func() {
		select {
		case s := <-interrupt:
			fmt.Printf("Shutting down, reason: %s\n", s.String())
			cancel()
		case <-ctx.Done():
		}
	}()

	var filToken string
	if startArgs.filToken != "" {
		// Basic auth requires base64 encoding and Infura api provides unencoded strings
		if startArgs.filTokenType == "Basic" {
			filToken = base64.StdEncoding.EncodeToString([]byte(startArgs.filToken))
		} else {
			filToken = startArgs.filToken
		}
		filToken = fmt.Sprintf("%s %s", startArgs.filTokenType, filToken)
	}

	var privKey string
	if startArgs.privKeyPath != "" {
		fdata, err := os.ReadFile(startArgs.privKeyPath)
		if err != nil {
			log.Error().Err(err).Msg("failed to read private key")
		} else {
			privKey = strings.TrimSpace(string(fdata))
		}
	}
	var bAddrs []string
	if startArgs.bootstrap != "" {
		bAddrs = append(bAddrs, startArgs.bootstrap)
	}

	opts := node.Options{
		RepoPath:       path,
		BootstrapPeers: bAddrs,
		FilEndpoint:    startArgs.filEndpoint,
		FilToken:       filToken,
		PrivKey:        privKey,
		Regions:        strings.Split(startArgs.regions, ","),
	}

	err = node.Run(ctx, opts)
	if err != nil && err != context.Canceled {
		log.Error().Err(err).Msg("node.Run")
		return err
	}
	return nil
}

// Repo path is akin to IPFS: ~/.pop by default or changed via $POP_PATH
func getRepoPath() string {
	if path, ok := os.LookupEnv("POP_PATH"); ok {
		return path
	}
	return ".pop"
}

// construct full path and check if a repo was initialized with a datastore
func repoFullPath(path string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, path), nil
}

func repoExists(path string) (bool, error) {
	_, err := os.Stat(filepath.Join(path, "datastore"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
