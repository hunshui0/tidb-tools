// Copyright 2021 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseConfig(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "config")
	configPath := writeTestConfig(t, "config.toml", outputDir)
	shardingConfigPath := writeTestConfig(t, "config_sharding.toml", outputDir)

	cfg := NewConfig()
	require.Nil(t, cfg.Parse([]string{"-L", "info", "--config", configPath}))
	cfg = NewConfig()
	require.Contains(t, cfg.Parse([]string{"-L", "info"}).Error(), "argument --config is required")

	unknownFlag := []string{"--LL", "info"}
	err := cfg.Parse(unknownFlag)
	require.Contains(t, err.Error(), "LL")

	require.Nil(t, cfg.Parse([]string{"--config", configPath}))
	require.Nil(t, cfg.Init())
	require.Nil(t, cfg.Task.Init(cfg.DataSources, cfg.TableConfigs))

	require.Nil(t, cfg.Parse([]string{"--config", shardingConfigPath}))
	// we change the config from config.toml to config_sharding.toml
	// this action will raise error.
	require.Contains(t, cfg.Init().Error(), "failed to init Task: config changes breaking the checkpoint, please use another outputDir and start over again!")

	require.NoError(t, os.RemoveAll(cfg.Task.OutputDir))
	require.Nil(t, cfg.Parse([]string{"--config", shardingConfigPath}))
	// this time will be ok, because we remove the last outputDir.
	require.Nil(t, cfg.Init())
	require.Nil(t, cfg.Task.Init(cfg.DataSources, cfg.TableConfigs))

	require.True(t, cfg.CheckConfig())

	// Keep assertions independent of host-specific paths and config hashes.
	require.Contains(t, cfg.String(), filepath.ToSlash(outputDir))
	require.NotContains(t, cfg.String(), "AVeryV#ryStr0ngP@ssw0rd")
	hash, err := cfg.Task.ComputeConfigHash()
	require.NoError(t, err)
	require.Len(t, hash, 64)

	require.True(t, cfg.TableConfigs["config1"].Valid())

}

func writeTestConfig(t *testing.T, name, outputDir string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	require.NoError(t, err)
	content := strings.Replace(string(data), `output-dir = "/tmp/output/config"`, `output-dir = "`+filepath.ToSlash(outputDir)+`"`, 1)
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestError(t *testing.T) {
	tableConfig := &TableConfig{}
	require.False(t, tableConfig.Valid())
	tableConfig.TargetTables = []string{"123", "234"}
	require.True(t, tableConfig.Valid())

	cfg := NewConfig()
	// Parse
	err := cfg.Parse([]string{"--config", "no_exist.toml"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no_exist.toml")

	// CheckConfig
	cfg.CheckThreadCount = 0
	require.False(t, cfg.CheckConfig())
	cfg.CheckThreadCount = 1
	require.True(t, cfg.CheckConfig())

	// Init
	cfg.DataSources = make(map[string]*DataSource)
	cfg.DataSources["123"] = &DataSource{
		RouteRules: []string{"111"},
	}
	err = cfg.Init()
	require.Contains(t, err.Error(), "not found source routes for rule 111, please correct the config")
}

func TestNoSecretLeak(t *testing.T) {
	source := &DataSource{
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "postgres",
		Password: "AVeryV#ryStr0ngP@ssw0rd",
		SqlMode:  "MYSQL",
		Snapshot: "2022/10/24",
	}
	cfg := &Config{}
	cfg.DataSources = map[string]*DataSource{"pg-1": source}
	require.NotContains(t, cfg.String(), "AVeryV#ryStr0ngP@ssw0rd", "%s", cfg.String())
	sourceJSON := []byte(`
		{
			"host": "127.0.0.1",
			"port": 5432,
			"user": "postgres",
			"password": "meow~~~"
		}
	`)
	s := DataSource{}
	json.Unmarshal(sourceJSON, &s)
	require.Equal(t, string(s.Password), "meow~~~")
}
