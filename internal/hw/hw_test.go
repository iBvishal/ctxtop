package hw

import (
	"runtime"
	"testing"
)

func TestRead(t *testing.T) {
	h := Read()
	if h.OS != runtime.GOOS || h.Arch != runtime.GOARCH {
		t.Errorf("os/arch = %s/%s", h.OS, h.Arch)
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return
	}
	if h.MemTotal == 0 {
		t.Fatal("mem_total is zero")
	}
	if h.MemUsed == 0 || h.MemUsed > h.MemTotal {
		t.Errorf("mem_used = %d of %d", h.MemUsed, h.MemTotal)
	}
	if h.SwapUsed > h.SwapTotal {
		t.Errorf("swap_used = %d of %d", h.SwapUsed, h.SwapTotal)
	}
	if runtime.GOOS == "darwin" && h.Pressure == "" {
		t.Error("darwin reported no memory pressure level")
	}
}
