package metrics

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"modelrouter/internal/config"
)

const maxInfluxExporterErrorLength = 512

var defaultInfluxRetryDelays = []time.Duration{100 * time.Millisecond, 500 * time.Millisecond}

type InfluxBatchWriter interface {
	WriteBatch(context.Context, config.InfluxDBConfig, []string) error
}

type InfluxDBExporterStatus struct {
	Enabled            bool   `json:"enabled"`
	PendingPoints      int64  `json:"pending_points"`
	WrittenPoints      int64  `json:"written_points"`
	WrittenBatches     int64  `json:"written_batches"`
	DroppedPoints      int64  `json:"dropped_points"`
	EncodingFailures   int64  `json:"encoding_failures"`
	FailedBatches      int64  `json:"failed_batches"`
	Retries            int64  `json:"retries"`
	LastSuccessUnixSec int64  `json:"last_success_unix_sec,omitempty"`
	LastErrorUnixSec   int64  `json:"last_error_unix_sec,omitempty"`
	LastError          string `json:"last_error,omitempty"`
}

type InfluxDBExporter struct {
	writer        InfluxBatchWriter
	now           func() time.Time
	flushInterval func(config.InfluxDBConfig) time.Duration
	retryDelays   []time.Duration

	mu        sync.Mutex
	config    config.InfluxDBConfig
	queue     []queuedInfluxPoint
	status    InfluxDBExporterStatus
	closed    bool
	wake      chan struct{}
	done      chan struct{}
	finished  chan struct{}
	closeOnce sync.Once
	runCtx    context.Context
	cancel    context.CancelFunc
}

type queuedInfluxPoint struct {
	config config.InfluxDBConfig
	line   string
}

type influxExporterOptions struct {
	now           func() time.Time
	flushInterval func(config.InfluxDBConfig) time.Duration
	retryDelays   []time.Duration
}

func NewInfluxDBExporter(cfg config.InfluxDBConfig, writer InfluxBatchWriter) *InfluxDBExporter {
	return newInfluxDBExporter(cfg, writer, influxExporterOptions{})
}

func newInfluxDBExporter(cfg config.InfluxDBConfig, writer InfluxBatchWriter, options influxExporterOptions) *InfluxDBExporter {
	if writer == nil {
		writer = NewInfluxDBWriter(nil)
	}
	if options.now == nil {
		options.now = time.Now
	}
	if options.flushInterval == nil {
		options.flushInterval = func(cfg config.InfluxDBConfig) time.Duration {
			return cfg.FlushInterval()
		}
	}
	if options.retryDelays == nil {
		options.retryDelays = defaultInfluxRetryDelays
	}
	runCtx, cancel := context.WithCancel(context.Background())
	exporter := &InfluxDBExporter{
		writer:        writer,
		now:           options.now,
		flushInterval: options.flushInterval,
		retryDelays:   append([]time.Duration(nil), options.retryDelays...),
		config:        cloneInfluxDBConfig(cfg),
		queue:         make([]queuedInfluxPoint, 0),
		wake:          make(chan struct{}, 1),
		done:          make(chan struct{}),
		finished:      make(chan struct{}),
		runCtx:        runCtx,
		cancel:        cancel,
	}
	exporter.status.Enabled = cfg.Enabled
	go exporter.run()
	return exporter
}

// Reconfigure 更新后续数据点使用的配置，已经入队的数据点仍写入原目标。
func (e *InfluxDBExporter) Reconfigure(cfg config.InfluxDBConfig) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.config = cloneInfluxDBConfig(cfg)
	e.status.Enabled = cfg.Enabled
	e.mu.Unlock()
}

// Record 编码并尝试入队一个请求指标；队列已满时立即丢弃，绝不阻塞代理请求。
func (e *InfluxDBExporter) Record(ev Event) {
	if e == nil {
		return
	}
	e.mu.Lock()
	cfg := cloneInfluxDBConfig(e.config)
	closed := e.closed
	e.mu.Unlock()
	if closed || !cfg.Enabled {
		return
	}

	line, err := EncodeInfluxLine(ev, cfg.Tags, e.now())
	if err != nil {
		e.recordEncodingFailure(err)
		return
	}
	cfg.Tags = nil

	e.mu.Lock()
	if e.closed {
		e.status.DroppedPoints++
		e.mu.Unlock()
		return
	}
	if e.status.PendingPoints >= int64(cfg.EffectiveQueueSize()) {
		e.status.DroppedPoints++
		e.mu.Unlock()
		return
	}
	e.queue = append(e.queue, queuedInfluxPoint{config: cfg, line: line})
	e.status.PendingPoints++
	e.mu.Unlock()
	e.signal()
}

func (e *InfluxDBExporter) Status() InfluxDBExporterStatus {
	if e == nil {
		return InfluxDBExporterStatus{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

// Close 停止接收新数据，并在调用方上下文有效期内刷新剩余数据点。
func (e *InfluxDBExporter) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.mu.Unlock()
		close(e.done)
		e.signal()
	})
	select {
	case <-e.finished:
		return nil
	case <-ctx.Done():
		e.cancel()
		return ctx.Err()
	}
}

func (e *InfluxDBExporter) run() {
	defer close(e.finished)
	defer e.cancel()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	var batch []queuedInfluxPoint
	var batchConfig config.InfluxDBConfig
	var timerC <-chan time.Time
	shuttingDown := false
	doneC := e.done

	for {
		if e.runCtx.Err() != nil {
			e.dropPending(batch)
			return
		}

		point, ok := e.popPoint()
		if ok {
			if len(batch) > 0 && !sameInfluxBatchConfig(batchConfig, point.config) {
				e.writeBatch(batchConfig, batch)
				batch = nil
				timerC = stopInfluxTimer(timer, timerC)
			}
			if len(batch) == 0 {
				batchConfig = point.config
				timerC = resetInfluxTimer(timer, e.flushInterval(batchConfig))
			}
			batch = append(batch, point)
			if len(batch) >= batchConfig.EffectiveBatchSize() {
				e.writeBatch(batchConfig, batch)
				batch = nil
				timerC = stopInfluxTimer(timer, timerC)
			}
			continue
		}

		if shuttingDown {
			if len(batch) > 0 {
				e.writeBatch(batchConfig, batch)
			}
			return
		}

		select {
		case <-e.runCtx.Done():
			e.dropPending(batch)
			return
		case <-doneC:
			shuttingDown = true
			doneC = nil
		case <-e.wake:
		case <-timerC:
			timerC = nil
			if len(batch) > 0 {
				e.writeBatch(batchConfig, batch)
				batch = nil
			}
		}
	}
}

func (e *InfluxDBExporter) writeBatch(cfg config.InfluxDBConfig, batch []queuedInfluxPoint) {
	lines := make([]string, len(batch))
	for i := range batch {
		lines[i] = batch[i].line
	}
	var err error
	for attempt := 0; ; attempt++ {
		err = e.writer.WriteBatch(e.runCtx, cfg, lines)
		if err == nil {
			e.recordWriteSuccess(len(lines))
			return
		}
		if attempt >= len(e.retryDelays) || !influxErrorRetryable(e.runCtx, err) {
			e.recordWriteFailure(len(lines), err)
			return
		}
		e.recordRetry()
		if !waitInfluxRetry(e.runCtx, e.retryDelays[attempt]) {
			e.recordWriteFailure(len(lines), e.runCtx.Err())
			return
		}
	}
}

func (e *InfluxDBExporter) popPoint() (queuedInfluxPoint, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.queue) == 0 {
		return queuedInfluxPoint{}, false
	}
	point := e.queue[0]
	e.queue[0] = queuedInfluxPoint{}
	e.queue = e.queue[1:]
	return point, true
}

func (e *InfluxDBExporter) dropPending(batch []queuedInfluxPoint) {
	e.mu.Lock()
	dropped := int64(len(batch) + len(e.queue))
	e.queue = nil
	e.status.PendingPoints -= dropped
	e.status.DroppedPoints += dropped
	e.mu.Unlock()
}

func (e *InfluxDBExporter) recordEncodingFailure(err error) {
	e.mu.Lock()
	e.status.EncodingFailures++
	e.setLastErrorLocked(err)
	e.mu.Unlock()
}

func (e *InfluxDBExporter) recordWriteSuccess(points int) {
	e.mu.Lock()
	e.status.PendingPoints -= int64(points)
	e.status.WrittenPoints += int64(points)
	e.status.WrittenBatches++
	e.status.LastSuccessUnixSec = e.now().Unix()
	e.status.LastError = ""
	e.mu.Unlock()
}

func (e *InfluxDBExporter) recordWriteFailure(points int, err error) {
	e.mu.Lock()
	e.status.PendingPoints -= int64(points)
	e.status.DroppedPoints += int64(points)
	e.status.FailedBatches++
	e.setLastErrorLocked(err)
	e.mu.Unlock()
}

func (e *InfluxDBExporter) recordRetry() {
	e.mu.Lock()
	e.status.Retries++
	e.mu.Unlock()
}

func (e *InfluxDBExporter) setLastErrorLocked(err error) {
	if err == nil {
		return
	}
	message := err.Error()
	if len(message) > maxInfluxExporterErrorLength {
		message = message[:maxInfluxExporterErrorLength]
	}
	e.status.LastError = message
	e.status.LastErrorUnixSec = e.now().Unix()
}

func (e *InfluxDBExporter) signal() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func cloneInfluxDBConfig(cfg config.InfluxDBConfig) config.InfluxDBConfig {
	cfg.Tags = maps.Clone(cfg.Tags)
	return cfg
}

func sameInfluxBatchConfig(a, b config.InfluxDBConfig) bool {
	return a.APIVersion == b.APIVersion &&
		a.URL == b.URL &&
		a.Org == b.Org &&
		a.Bucket == b.Bucket &&
		a.Database == b.Database &&
		a.Token == b.Token &&
		a.BatchSize == b.BatchSize &&
		a.FlushIntervalSeconds == b.FlushIntervalSeconds &&
		a.TimeoutSeconds == b.TimeoutSeconds
}

func influxErrorRetryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if writeErr, ok := errors.AsType[*InfluxDBWriteError](err); ok {
		return writeErr.Retryable()
	}
	return true
}

func waitInfluxRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func resetInfluxTimer(timer *time.Timer, interval time.Duration) <-chan time.Time {
	if interval <= 0 {
		interval = time.Second
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(interval)
	return timer.C
}

func stopInfluxTimer(timer *time.Timer, timerC <-chan time.Time) <-chan time.Time {
	if timerC == nil {
		return nil
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	return nil
}
