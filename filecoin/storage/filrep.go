package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const (
	baseURL      = "https://api.filrep.io/api/v1"
	defaultLimit = 10
)

// FilRep is an http client for the filrep.io miner reputation api
type FilRep struct {
	httpClient *http.Client
}

// NewFilRep creates a new instance of the FilRep client with a default http client
func NewFilRep() *FilRep {
	c := &FilRep{
		httpClient: http.DefaultClient,
	}
	return c
}

// FindMiners executes a request for the main /miners endpoint of the filrep api
func (fr *FilRep) FindMiners(ctx context.Context, mp MinerParams) (result MinersResult, err error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return MinersResult{}, err
	}
	base.Path += "/miners"

	base.RawQuery = mp.URLEncode()

	req, err := http.NewRequestWithContext(ctx, "GET", base.String(), nil)
	if err != nil {
		return MinersResult{}, err
	}
	res, err := fr.httpClient.Do(req)
	if err != nil {
		return MinersResult{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return MinersResult{}, fmt.Errorf("api error code: %d", res.StatusCode)
	}

	err = json.NewDecoder(res.Body).Decode(&result)
	if err != nil {
		return MinersResult{}, err
	}

	return result, nil
}
