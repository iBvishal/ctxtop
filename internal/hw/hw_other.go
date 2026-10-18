//go:build !darwin && !linux

package hw

import "github.com/iBvishal/ctxtop/internal/snapshot"

func readMemory(*snapshot.Host) {}
