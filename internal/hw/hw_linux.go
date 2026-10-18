package hw

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

func readMemory(h *snapshot.Host) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	parseMeminfo(f, h)
}

// parseMeminfo fills h from /proc/meminfo. Used memory is total minus
// MemAvailable, as free(1) counts it.
func parseMeminfo(r io.Reader, h *snapshot.Host) {
	kb := map[string]uint64{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			kb[name] = v
		}
	}
	h.MemTotal = kb["MemTotal"] * 1024
	if avail, ok := kb["MemAvailable"]; ok && avail <= kb["MemTotal"] {
		h.MemUsed = (kb["MemTotal"] - avail) * 1024
	}
	h.SwapTotal = kb["SwapTotal"] * 1024
	if kb["SwapFree"] <= kb["SwapTotal"] {
		h.SwapUsed = (kb["SwapTotal"] - kb["SwapFree"]) * 1024
	}
}
