package exchange

import (
	"math"
	"path"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
)

// Regions: This is very experimental and needs more iterations to be stable
// currently we only offer 2 bootstrap nodes for Europe and NorthAmerica more will follow

// RegionCode defines a subnetwork code
type RegionCode uint64

const (
	// GlobalRegion region is a free global network for anyone to try the network
	GlobalRegion RegionCode = iota
	// AsiaRegion is a specific region to connect caches in the Asian area
	AsiaRegion
	// AfricaRegion is a specific region in the African geographic area
	AfricaRegion
	// SouthAmericaRegion is a specific region
	SouthAmericaRegion
	// NorthAmericaRegion is a specific region to connect caches in the North American area
	NorthAmericaRegion
	// EuropeRegion is a specific region to connect caches in the European area
	EuropeRegion
	// OceaniaRegion is a specific region
	OceaniaRegion
	// CustomRegion is a user defined region
	CustomRegion = math.MaxUint64
)

// Region represents a CDN subnetwork.
type Region struct {
	// The official region name should be unique to avoid clashing with other regions.
	Name string
	// Code is a compressed identifier for the region.
	Code RegionCode
	// PPB is the minimum price per byte in FIL defined for this region. This does not account for
	// any dynamic pricing mechanisms.
	PPB abi.TokenAmount
	// Bootstrap is a list of peers that can be dialed to find other peers in that region
	Bootstrap []string
}

var (
	asia = Region{
		Name:      "Asia",
		Code:      AsiaRegion,
		PPB:       abi.NewTokenAmount(1),
		Bootstrap: []string{},
	}
	africa = Region{
		Name:      "Africa",
		Code:      AfricaRegion,
		PPB:       abi.NewTokenAmount(1),
		Bootstrap: []string{},
	}
	southAmerica = Region{
		Name:      "SouthAmerica",
		Code:      SouthAmericaRegion,
		PPB:       abi.NewTokenAmount(1),
		Bootstrap: []string{},
	}
	northAmerica = Region{
		Name: "NorthAmerica",
		Code: NorthAmericaRegion,
		PPB:  abi.NewTokenAmount(1),
		Bootstrap: []string{
			"/dns4/ohio.myel.zone/tcp/41504/p2p/12D3KooWStJfAywQmfaVFQDQYr9riDnEFG3VJ3qDGcTidvc4nQtc",
		},
	}
	europe = Region{
		Name: "Europe",
		Code: EuropeRegion,
		PPB:  abi.NewTokenAmount(1),
		Bootstrap: []string{
			"/dns4/frankfurt.myel.zone/tcp/41504/p2p/12D3KooWCdyxtPXQnNH4qB25SHhRiDkUpDY8ZYw1tKHPteSJjV4U",
		},
	}
	oceania = Region{
		Name:      "Oceania",
		Code:      OceaniaRegion,
		PPB:       abi.NewTokenAmount(1),
		Bootstrap: []string{},
	}
	global = Region{
		Name:      "Global",
		Code:      GlobalRegion,
		PPB:       big.Zero(),
		Bootstrap: []string{},
	}
)

// Regions is a list of preset regions
var Regions = map[string]Region{
	"Global":       global,
	"Asia":         asia,
	"Africa":       africa,
	"SouthAmerica": southAmerica,
	"NorthAmerica": northAmerica,
	"Europe":       europe,
	"Oceania":      oceania,
}

// ParseRegions converts region names to region structs
func ParseRegions(list []string) []Region {
	var regions []Region
	for _, rstring := range list {
		if r := Regions[rstring]; r.Name != "" {
			regions = append(regions, r)
			continue
		}
		// We also support custom regions if users want their own provider subnet
		regions = append(regions, Region{
			Name: rstring,
			Code: CustomRegion,
		})
	}
	return regions
}

// RegionFromTopic formats a topic string into a Region struct
func RegionFromTopic(topic string) Region {
	_, name := path.Split(topic)
	r, ok := Regions[name]
	if !ok {
		return Region{
			Name: name,
			Code: CustomRegion,
			PPB:  big.Zero(), // TODO: handle pricing for custom region
		}
	}
	return r
}
