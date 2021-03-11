package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterbourgon/ff/v2/ffcli"
	"github.com/rs/zerolog/log"
)

var initCmd = &ffcli.Command{
	Name:      "init",
	ShortHelp: "Creates a new IPFS repo with a config file",
	LongHelp: strings.TrimSpace(`

The 'hop init' command generates a new empty repo at path ~/.hop with default configurations. This is required 
before starting the node for the first time. To edit configs open ~/.hop/HopConfig.json.

`),
	Exec: runInit,
}

// HopConfig is the json config object we generate with the init command
type HopConfig struct {
	FilEndpoint   string `json:"fil-endpoint"`
	FilToken      string `json:"fil-token"`
	FilTokenType  string `json:"fil-token-type"`
	BootstrapAddr string `json:"bootstrap"`
}

func runInit(ctx context.Context, args []string) error {
	path, err := repoFullPath(getRepoPath())
	if err != nil {
		return err
	}

	exists, err := repoExists(path)
	if err != nil {
		return err
	}
	if !exists {
		// Make our root repo dir and datastore dir
		err = os.MkdirAll(filepath.Join(path, "datastore"), 0755)
		if err != nil {
			return err
		}
		// default configs
		// Regions aren't set in a static config object as we aim to make them
		// more dynamic in the future
		config := HopConfig{
			FilEndpoint:   os.Getenv("FIL_ENDPOINT"),
			FilToken:      os.Getenv("FIL_TOKEN"),
			BootstrapAddr: "/ip4/3.14.73.230/tcp/4001/ipfs/12D3KooWQtnktGLsDc3fgHW4vrsCVR15oC1Vn6Wy6Moi65pL6q2a",
			FilTokenType:  "Bearer",
		}
		buf := new(bytes.Buffer)
		e := json.NewEncoder(buf)
		e.SetIndent("", "    ")
		if err := e.Encode(config); err != nil {
			return err
		}
		c, err := os.Create(filepath.Join(path, "HopConfig.json"))
		if err != nil {
			return err
		}
		_, err = c.Write(buf.Bytes())
		if err != nil {
			return err
		}
		if err := c.Close(); err != nil {
			return err
		}
		log.Info().Str("path", path).Msg("initialized new IPFS repo")
		return nil
	}
	return fmt.Errorf("a hop repo already exists")
}