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

package checkpoints

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pingcap/tidb-tools/sync_diff_inspector/chunk"
	"github.com/pingcap/tidb/pkg/parser/model"
	"github.com/stretchr/testify/require"
)

func TestSaveChunkWindowsSafePathAndOverwrite(t *testing.T) {
	cp := new(Checkpoint)
	cp.Init()
	target := filepath.Join(t.TempDir(), "nested", "checkpoint.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	node := &Node{State: SuccessState, ChunkRange: &chunk.Range{Index: &chunk.ChunkID{TableIndex: 0, ChunkIndex: 0, ChunkCnt: 1}, IsFirst: true, IsLast: true}}
	_, err := cp.SaveChunk(context.Background(), target, node, nil)
	require.NoError(t, err)
	_, err = cp.SaveChunk(context.Background(), target, node, nil)
	require.NoError(t, err)
	loaded, _, err := cp.LoadChunk(target)
	require.NoError(t, err)
	require.Equal(t, node.GetID().Compare(loaded.GetID()), 0)
	entries, err := os.ReadDir(filepath.Dir(target))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, filepath.Base(target), entries[0].Name())
}

func TestLoadChunkCorrupt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "checkpoint.json")
	require.NoError(t, os.WriteFile(file, []byte("not-json"), 0o600))
	cp := new(Checkpoint)
	_, _, err := cp.LoadChunk(file)
	require.Error(t, err)
}

func TestSaveChunk(t *testing.T) {
	checker := new(Checkpoint)
	checker.Init()
	ctx := context.Background()
	cur := checker.GetChunkSnapshot()
	id, err := checker.SaveChunk(ctx, "TestSaveChunk", cur, nil)
	require.NoError(t, err)
	require.Nil(t, id)
	wg := &sync.WaitGroup{}
	rounds := 100
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func(i int) {
			node := &Node{
				ChunkRange: &chunk.Range{
					Index: &chunk.ChunkID{
						TableIndex:       0,
						BucketIndexLeft:  i / 10,
						BucketIndexRight: i / 10,
						ChunkIndex:       i % 10,
						ChunkCnt:         10,
					},
					Bounds: []*chunk.Bound{
						{
							HasLower: i != 0,
							Lower:    strconv.Itoa(i + 1000),
							Upper:    strconv.Itoa(i + 1000 + 1),
							HasUpper: i != rounds,
						},
					},
					IndexColumnNames: []model.CIStr{model.NewCIStr("col1"), model.NewCIStr("col2")},
				},

				State: SuccessState,
			}
			if rand.Intn(4) == 0 {
				time.Sleep(time.Duration(rand.Intn(3)) * time.Second)
			}
			checker.Insert(node)
			wg.Done()
		}(i)
	}
	wg.Wait()
	defer os.Remove("TestSaveChunk")

	cur = checker.GetChunkSnapshot()
	require.NotNil(t, cur)
	id, err = checker.SaveChunk(ctx, "TestSaveChunk", cur, nil)
	require.NoError(t, err)
	require.Equal(t, id.Compare(&chunk.ChunkID{TableIndex: 0, BucketIndexLeft: 9, BucketIndexRight: 9, ChunkIndex: 9}), 0)
}

func TestLoadChunk(t *testing.T) {
	checker := new(Checkpoint)
	checker.Init()
	ctx := context.Background()
	rounds := 100
	wg := &sync.WaitGroup{}
	testColNames := []model.CIStr{model.NewCIStr("col1"), model.NewCIStr("col2")}
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func(i int) {
			node := &Node{
				ChunkRange: &chunk.Range{
					Bounds: []*chunk.Bound{
						{
							HasLower: i != 0,
							Lower:    strconv.Itoa(i + 1000),
							Upper:    strconv.Itoa(i + 1000 + 1),
							HasUpper: i != rounds,
						},
					},
					Index: &chunk.ChunkID{
						TableIndex:       0,
						BucketIndexLeft:  i / 10,
						BucketIndexRight: i / 10,
						ChunkIndex:       i % 10,
						ChunkCnt:         10,
					},
					IndexColumnNames: testColNames,
				},
			}
			checker.Insert(node)
			wg.Done()
		}(i)
	}
	wg.Wait()
	defer os.Remove("TestLoadChunk")
	cur := checker.GetChunkSnapshot()
	id, err := checker.SaveChunk(ctx, "TestLoadChunk", cur, nil)
	require.NoError(t, err)
	node, _, err := checker.LoadChunk("TestLoadChunk")
	require.NoError(t, err)
	require.Equal(t, node.GetID().Compare(id), 0)
	require.Equal(t, node.ChunkRange.IndexColumnNames, testColNames)
}
