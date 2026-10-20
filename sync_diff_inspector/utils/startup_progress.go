package utils

import (
	"sync"
	"time"

	"github.com/pingcap/log"
	"go.uber.org/zap"
)

// StartupProgress reports the last completed item and the operation currently
// waiting. Its heartbeat continues while a database call blocks; it does not
// imply that any comparison or query has completed.
type StartupProgress struct {
	mu        sync.Mutex
	stage     string
	completed int
	total     int
	current   string
	started   time.Time
	logger    *zap.Logger
	stop      chan struct{}
	done      chan struct{}
	once      sync.Once
}

func NewStartupProgress(stage string, total int) *StartupProgress {
	return newStartupProgress(stage, total, 5*time.Second, log.L())
}

func newStartupProgress(stage string, total int, interval time.Duration, logger *zap.Logger) *StartupProgress {
	p := &StartupProgress{stage: stage, total: total, started: time.Now(), logger: logger,
		stop: make(chan struct{}), done: make(chan struct{})}
	p.logger.Info("初始化阶段开始", p.fields()...)
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.logger.Info("初始化阶段进行中", p.fields()...)
			case <-p.stop:
				return
			}
		}
	}()
	return p
}

func (p *StartupProgress) Update(completed int, current string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completed, p.current = completed, current
}

func (p *StartupProgress) SetTotal(total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = total
}

func (p *StartupProgress) fields() []zap.Field {
	p.mu.Lock()
	defer p.mu.Unlock()
	return []zap.Field{zap.String("stage", p.stage), zap.Int("completed", p.completed),
		zap.Int("total", p.total), zap.String("current", p.current), zap.Duration("elapsed", time.Since(p.started))}
}

func (p *StartupProgress) Finish(err error) {
	p.once.Do(func() {
		close(p.stop)
		<-p.done
		fields := p.fields()
		if err != nil {
			p.logger.Error("初始化阶段失败", append(fields, zap.Error(err))...)
		} else {
			p.logger.Info("初始化阶段完成", fields...)
		}
	})
}
