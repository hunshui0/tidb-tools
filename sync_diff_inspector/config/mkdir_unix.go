//go:build !windows

package config

import (
	"os"
	"syscall"

	"github.com/pingcap/errors"
)

func mkdirAll(base string) error {
	mask := syscall.Umask(0)
	err := os.MkdirAll(base, LocalDirPerm)
	syscall.Umask(mask)
	return errors.Trace(err)
}
