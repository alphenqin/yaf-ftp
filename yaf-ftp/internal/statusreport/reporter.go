package statusreport

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yaf-ftp/flow2ftp/internal/config"
)

// Reporter 负责聚合流量统计并定期上报
type Reporter struct {
	cfg       config.StatusReportConfig
	client    *http.Client
	startTime time.Time

	totalPkts  atomic.Int64
	totalBytes atomic.Int64

	mu            sync.Mutex
	lastPkts      int64
	lastBytes     int64
	lastTimestamp time.Time
	uuid          string
}

// NewReporter 创建 Reporter（未启用时返回 nil, nil）
func NewReporter(cfg config.StatusReportConfig) (*Reporter, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	uuid := strings.TrimSpace(cfg.UUID)
	if uuid == "" {
		h, _ := os.Hostname()
		uuid = h
	}

	return &Reporter{
		cfg:           cfg,
		client:        &http.Client{Timeout: 10 * time.Second},
		startTime:     time.Now(),
		lastTimestamp: time.Now(),
		uuid:          uuid,
	}, nil
}

// Add 累加一次包/字节统计
func (r *Reporter) Add(pkts, bytes int64) {
	if r == nil {
		return
	}
	r.totalPkts.Add(pkts)
	r.totalBytes.Add(bytes)
}

// Run 启动周期上报
func (r *Reporter) Run(ctxDone <-chan struct{}) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(time.Duration(r.cfg.IntervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctxDone:
			return
		case <-ticker.C:
			r.reportOnce()
		}
	}
}

func (r *Reporter) reportOnce() {
	now := time.Now()
	totalPkts := r.totalPkts.Load()
	totalBytes := r.totalBytes.Load()

	r.mu.Lock()
	deltaPkts := totalPkts - r.lastPkts
	deltaBytes := totalBytes - r.lastBytes
	elapsedWindow := now.Sub(r.lastTimestamp).Seconds()
	if elapsedWindow <= 0 {
		elapsedWindow = 1
	}
	r.lastPkts = totalPkts
	r.lastBytes = totalBytes
	r.lastTimestamp = now
	r.mu.Unlock()

	runSecs := now.Sub(r.startTime).Seconds()
	if runSecs <= 0 {
		runSecs = 1
	}

	payload := map[string]interface{}{
		"curRcvPkts":    deltaPkts,
		"curRcvBytes":   deltaBytes,
		"curPkts":       deltaPkts,
		"curBytes":      deltaBytes,
		"curAvgRcvPps":  float64(deltaPkts) / elapsedWindow,
		"curAvgRcvBps":  float64(deltaBytes) / elapsedWindow,
		"curAvgPps":     float64(deltaPkts) / elapsedWindow,
		"curAvgBps":     float64(deltaBytes) / elapsedWindow,
		"uuid":          r.uuid,
		"runSecs":       int64(runSecs),
		"totalRcvPkts":  totalPkts,
		"totalRcvBytes": totalBytes,
		"totalPkts":     totalPkts,
		"totalBytes":    totalBytes,
		"totalAvgRcvPps": func() float64 {
			return float64(totalPkts) / runSecs
		}(),
		"totalAvgRcvBps": func() float64 {
			return float64(totalBytes) / runSecs
		}(),
		"totalAvgPps": func() float64 {
			return float64(totalPkts) / runSecs
		}(),
		"totalAvgBps": func() float64 {
			return float64(totalBytes) / runSecs
		}(),
	}

	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[ERROR] 状态上报序列化失败: %v", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, r.cfg.URL, bytes.NewReader(b))
	if err != nil {
		log.Printf("[ERROR] 状态上报请求创建失败: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		log.Printf("[ERROR] 状态上报失败: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("[WARN] 状态上报返回非 2xx: %s", resp.Status)
	}
}
