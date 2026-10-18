//go:build !darwin

package hw

import "github.com/iBvishal/ctxtop/internal/snapshot"

func readGPU(*snapshot.Host) {}
