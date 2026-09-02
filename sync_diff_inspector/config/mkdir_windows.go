//go:build windows

package config

import (
	"os"

	"github.com/pingcap/errors"
)

func mkdirAll(base string) error {
	return errors.Trace(os.MkdirAll(base, LocalDirPerm))
}
