package hw

import (
	"strings"
	"testing"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

func TestParseMeminfo(t *testing.T) {
	const in = `MemTotal:       16000000 kB
MemFree:         2000000 kB
MemAvailable:    6000000 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
`
	var h snapshot.Host
	parseMeminfo(strings.NewReader(in), &h)
	if h.MemTotal != 16000000*1024 || h.MemUsed != 10000000*1024 {
		t.Errorf("mem = %d used of %d", h.MemUsed, h.MemTotal)
	}
	if h.SwapTotal != 2000000*1024 || h.SwapUsed != 500000*1024 {
		t.Errorf("swap = %d used of %d", h.SwapUsed, h.SwapTotal)
	}
}
