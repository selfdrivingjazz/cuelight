//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"os"

	"golang.org/x/sys/windows"
)

func terminationSignals() []os.Signal { return []os.Signal{os.Interrupt} }

type filesystemCapacity struct{ path string }

func (c filesystemCapacity) AvailableBytes(context.Context) (int64, error) {
	path, err := windows.UTF16PtrFromString(c.path)
	if err != nil {
		return 0, fmt.Errorf("encode player cache path: %w", err)
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, nil, nil); err != nil {
		return 0, fmt.Errorf("inspect player cache capacity: %w", err)
	}
	if available > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(available), nil
}
