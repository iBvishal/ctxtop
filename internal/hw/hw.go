// Package hw reads the machine's memory state without root.
package hw

import (
	"runtime"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// Read returns the host's memory, GPU and power state. Fields the platform
// can't report stay empty; Read never fails. Power is measured since the
// previous Read, so the first one has none.
func Read() snapshot.Host {
	h := snapshot.Host{OS: runtime.GOOS, Arch: runtime.GOARCH}
	readMemory(&h)
	readGPU(&h)
	return h
}
