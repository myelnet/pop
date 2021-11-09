package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHeaderParse(t *testing.T) {
	data := parseTimingHeader("dial;dur=0.800000011920929, db;dur=53")
	require.Equal(t, map[string]map[string]string{
		"dial": {"dur": "0.800000011920929"},
		"db":   {"dur": "53"},
	}, data)

	// ignore malformed entries
	data = parseTimingHeader("dial;dur=0.800000011920929, db;dur")
	require.Equal(t, map[string]map[string]string{
		"dial": {"dur": "0.800000011920929"},
	}, data)

}
