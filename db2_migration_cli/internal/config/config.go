// Package config reads the small db2-migrate wrapper configuration. It never
// reads or adds wrapper-only settings to a TiDB Lightning configuration.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type File struct {
	Tools struct {
		SyncDiffBinary string `toml:"sync-diff-binary"`
		TiDBToolkitDir string `toml:"tidb-toolkit-dir"`
	} `toml:"tools"`
	Path string `toml:"-"`
}

// Load returns an empty configuration when path is empty or does not exist.
// Relative tool paths are made relative to the wrapper configuration file.
func Load(path string) (File, error) {
	var cfg File
	if path == "" {
		return cfg, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return cfg, err
	}
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	} else if err != nil {
		return cfg, err
	}
	if _, err := toml.DecodeFile(abs, &cfg); err != nil {
		return cfg, err
	}
	cfg.Path = abs
	base := filepath.Dir(abs)
	if cfg.Tools.SyncDiffBinary != "" && !filepath.IsAbs(cfg.Tools.SyncDiffBinary) {
		cfg.Tools.SyncDiffBinary = filepath.Join(base, cfg.Tools.SyncDiffBinary)
	}
	if cfg.Tools.TiDBToolkitDir != "" && !filepath.IsAbs(cfg.Tools.TiDBToolkitDir) {
		cfg.Tools.TiDBToolkitDir = filepath.Join(base, cfg.Tools.TiDBToolkitDir)
	}
	return cfg, nil
}
