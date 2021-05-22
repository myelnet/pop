package deal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntervals(t *testing.T) {

	testCases := []struct {
		currentInterval  uint64
		paymentInterval  uint64
		intervalIncrease uint64
		expLowerBound    uint64
		expNextInterval  uint64
	}{{
		currentInterval:  0,
		paymentInterval:  10,
		intervalIncrease: 5,
		expLowerBound:    0,
		expNextInterval:  10,
	}, {
		currentInterval:  10,
		paymentInterval:  10,
		intervalIncrease: 5,
		expLowerBound:    0,
		expNextInterval:  25, // 10 + (10 + 5)
	},
	}

	for _, tc := range testCases {
		t.Run("", func(t *testing.T) {
			params := Params{
				PaymentInterval:         tc.paymentInterval,
				PaymentIntervalIncrease: tc.intervalIncrease,
			}
			lowerBound := params.IntervalLowerBound(tc.currentInterval)
			nextInterval := params.NextInterval(tc.currentInterval)

			require.Equal(t, tc.expLowerBound, lowerBound)
			require.Equal(t, tc.expNextInterval, nextInterval)
		})
	}
}
