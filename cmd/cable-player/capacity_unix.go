//go:build !windows

package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"syscall"
)

func terminationSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }

type filesystemCapacity struct{ path string }

func (c filesystemCapacity) AvailableBytes(context.Context) (int64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(c.path, &stats); err != nil {
		return 0, fmt.Errorf("inspect player cache capacity: %w", err)
	}
	blocks, size := uint64(stats.Bavail), uint64(stats.Bsize)
	if size != 0 && blocks > math.MaxInt64/size {
		return math.MaxInt64, nil
	}
	return int64(blocks * size), nil
}
