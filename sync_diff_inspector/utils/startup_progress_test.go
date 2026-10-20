package utils

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestStartupProgressReportsBlockedOperationAndStops(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			core, recorded := observer.New(zap.InfoLevel)
			heartbeats := make(chan struct{}, 16)
			logger := zap.New(core, zap.Hooks(func(entry zapcore.Entry) error {
				if entry.Message == "初始化阶段进行中" {
					select {
					case heartbeats <- struct{}{}:
					default:
					}
				}
				return nil
			}))
			p := newStartupProgress("读取 DB2 表元数据", 10, 10*time.Millisecond, logger)
			defer p.Finish(nil)
			p.SetTotal(12)
			p.Update(3, "APP.WAITING_TABLE")
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			// No work completes while waiting. The heartbeat must still identify
			// the blocked table without incrementing the completed count.
			for {
				select {
				case <-deadline.C:
					t.Fatal("no heartbeat for the pending table")
				case <-heartbeats:
				}
				entries := recorded.FilterMessage("初始化阶段进行中").All()
				fields := entries[len(entries)-1].ContextMap()
				if fields["current"] != "APP.WAITING_TABLE" {
					continue
				}
				require.EqualValues(t, 3, fields["completed"])
				require.EqualValues(t, 12, fields["total"])
				require.Equal(t, "读取 DB2 表元数据", fields["stage"])
				require.Contains(t, fields, "elapsed")
				break
			}
			var err error
			if failed {
				err = errors.New("catalog query timed out")
			} else {
				p.Update(12, "")
			}
			p.Finish(err)
			p.Finish(err) // repeated cleanup must not panic or duplicate final logs
			entries := recorded.All()
			require.Equal(t, "初始化阶段开始", entries[0].Message)
			last := entries[len(entries)-1]
			if failed {
				require.Equal(t, "初始化阶段失败", last.Message)
				require.Equal(t, "catalog query timed out", last.ContextMap()["error"])
				require.Equal(t, "APP.WAITING_TABLE", last.ContextMap()["current"])
			} else {
				require.Equal(t, "初始化阶段完成", last.Message)
				require.EqualValues(t, 12, last.ContextMap()["completed"])
			}
			select {
			case <-p.done:
			default:
				t.Fatal("heartbeat goroutine is still running after Finish")
			}
		})
	}
}
