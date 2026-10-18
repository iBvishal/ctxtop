// Package hw reads the machine's memory state without root.
package hw

import (
	"runtime"

	"github.com/iBvishal/ctxtop/internal/snapshot"
)

// Read returns the host's memory state. Fields the platform can't report
// stay zero; Read never fails.
func Read() snapshot.Host {
	h := snapshot.Host{OS: runtime.GOOS, Arch: runtime.GOARCH}
	readMemory(&h)
	return h
}
