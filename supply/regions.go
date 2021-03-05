package supply

import (
	"math"

	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
)

// Regions: This is very experimental and needs more iterations to be stable

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

// Region represents a CDN subnetwork
type Region struct {
	// The official region name should be unique to avoid clashing with other regions
	Name string
	// Code is a compressed identifier for the region
	Code RegionCode
	// PPB is the minimum price per byte in FIL defined for this region. This does not account for
	// any dynamic pricing mechanisms
	PPB abi.TokenAmount
}

var (
	global = Region{
		Name: "Global",
		Code: GlobalRegion,
		PPB:  big.Zero(),
	}
	asia = Region{
		Name: "Asia",
		Code: AsiaRegion,
		PPB:  abi.NewTokenAmount(1),
	}
	africa = Region{
		Name: "Africa",
		Code: AfricaRegion,
		PPB:  abi.NewTokenAmount(1),
	}
	southAmerica = Region{
		Name: "SouthAmerica",
		Code: SouthAmericaRegion,
		PPB:  abi.NewTokenAmount(1),
	}
	northAmerica = Region{
		Name: "NorthAmerica",
		Code: NorthAmericaRegion,
		PPB:  abi.NewTokenAmount(1),
	}
	europe = Region{
		Name: "Europe",
		Code: EuropeRegion,
		PPB:  abi.NewTokenAmount(1),
	}
	oceania = Region{
		Name: "Oceania",
		Code: OceaniaRegion,
		PPB:  abi.NewTokenAmount(1),
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
