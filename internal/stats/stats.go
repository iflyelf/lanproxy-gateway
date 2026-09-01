package stats

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const numShards = 64 // 设备统计分片数

// Collector 聚合每设备(源 IP)的流量与连接统计,并保存最近的连接记录。
// 所有方法均并发安全。使用分片锁优化高并发性能。
type Collector struct {
	// 设备统计分片(按 IP hash 分桶,减少锁竞争)
	shards [numShards]deviceShard

	// 连接记录环形缓冲(独立锁)
	recentMu  sync.RWMutex
	recent    []ConnRecord
	recentCap int
	recentIdx int
	recentLen int

	// 流量时序采样(独立锁)
	sampleMu   sync.RWMutex
	samples    []TrafficSample
	sampleCap  int
	sampleIdx  int
	lastSample time.Time
	lastTx     uint64
	lastRx     uint64

	connSeq atomic.Uint64
}

type deviceShard struct {
	mu      sync.RWMutex
	devices map[string]*deviceStat
}

type deviceStat struct {
	IP          string
	TxBytes     atomic.Uint64 // 设备发出(上行)字节
	RxBytes     atomic.Uint64 // 设备接收(下行)字节
	TotalConns  atomic.Uint64
	ActiveConns atomic.Int64
	LastSeen    atomic.Int64 // unix 秒
}

// DeviceStat 是对外暴露的设备统计快照。
type DeviceStat struct {
	IP          string `json:"ip"`
	TxBytes     uint64 `json:"tx_bytes"`
	RxBytes     uint64 `json:"rx_bytes"`
	TotalConns  uint64 `json:"total_conns"`
	ActiveConns int64  `json:"active_conns"`
	LastSeen    int64  `json:"last_seen"`
}

// ConnRecord 是一条连接记录。
type ConnRecord struct {
	ID        uint64 `json:"id"`
	SrcIP     string `json:"src_ip"`
	DstIP     string `json:"dst_ip"`
	DstPort   int    `json:"dst_port"`
	StartedAt int64  `json:"started_at"`
	ClosedAt  int64  `json:"closed_at"`
	TxBytes   uint64 `json:"tx_bytes"`
	RxBytes   uint64 `json:"rx_bytes"`
	Upstream  string `json:"upstream"` // proxy / direct / failed
	Failed    bool   `json:"failed"`
}

// TrafficSample 是一个时序采样点(全局速率)。
type TrafficSample struct {
	Timestamp int64  `json:"t"`      // unix 毫秒
	TxRate    uint64 `json:"tx_rate"` // 字节/秒
	RxRate    uint64 `json:"rx_rate"`
}

// New 创建统计收集器,recentCap 为最近连接记录的保留条数。
func New(recentCap int) *Collector {
	if recentCap <= 0 {
		recentCap = 500
	}
	c := &Collector{
		recent:    make([]ConnRecord, recentCap),
		recentCap: recentCap,
		samples:   make([]TrafficSample, 300), // 5分钟@1秒
		sampleCap: 300,
	}
	for i := range c.shards {
		c.shards[i].devices = make(map[string]*deviceStat)
	}
	return c
}

// shardFor 返回 IP 对应的分片索引。
func shardFor(ip string) int {
	h := uint32(0)
	for i := 0; i < len(ip); i++ {
		h = h*31 + uint32(ip[i])
	}
	return int(h % numShards)
}

func (c *Collector) device(ip string) *deviceStat {
	shard := &c.shards[shardFor(ip)]
	shard.mu.RLock()
	d, ok := shard.devices[ip]
	shard.mu.RUnlock()
	if ok {
		return d
	}
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if d, ok = shard.devices[ip]; ok {
		return d
	}
	d = &deviceStat{IP: ip}
	shard.devices[ip] = d
	return d
}

// OpenConn 记录一条新连接的建立,返回连接 ID。
func (c *Collector) OpenConn(srcIP, dstIP string, dstPort int, upstream string) uint64 {
	d := c.device(srcIP)
	d.TotalConns.Add(1)
	d.ActiveConns.Add(1)
	d.LastSeen.Store(time.Now().Unix())
	return c.connSeq.Add(1)
}

// RecordConn 记录一条连接的结束及流量,仅在连接关闭时调用一次。
// 替代原 CloseConn 的双调用问题。
func (c *Collector) RecordConn(connID uint64, srcIP, dstIP string, dstPort int, tx, rx uint64, upstream string, failed bool) {
	d := c.device(srcIP)
	d.ActiveConns.Add(-1)
	d.TxBytes.Add(tx)
	d.RxBytes.Add(rx)

	rec := ConnRecord{
		ID:        connID,
		SrcIP:     srcIP,
		DstIP:     dstIP,
		DstPort:   dstPort,
		StartedAt: time.Now().Unix() - 1, // 近似,无需保存 start 时间
		ClosedAt:  time.Now().Unix(),
		TxBytes:   tx,
		RxBytes:   rx,
		Upstream:  upstream,
		Failed:    failed,
	}

	c.recentMu.Lock()
	c.recent[c.recentIdx] = rec
	c.recentIdx = (c.recentIdx + 1) % c.recentCap
	if c.recentLen < c.recentCap {
		c.recentLen++
	}
	c.recentMu.Unlock()
}

// Devices 返回所有设备的统计快照,按总流量(上行+下行)降序排列。
func (c *Collector) Devices() []DeviceStat {
	out := make([]DeviceStat, 0, 64)
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.RLock()
		for _, d := range shard.devices {
			out = append(out, DeviceStat{
				IP:          d.IP,
				TxBytes:     d.TxBytes.Load(),
				RxBytes:     d.RxBytes.Load(),
				TotalConns:  d.TotalConns.Load(),
				ActiveConns: d.ActiveConns.Load(),
				LastSeen:    d.LastSeen.Load(),
			})
		}
		shard.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool {
		return (out[i].TxBytes + out[i].RxBytes) > (out[j].TxBytes + out[j].RxBytes)
	})
	return out
}

// Recent 返回最近 N 条连接记录,从新到旧。
func (c *Collector) Recent(limit int) []ConnRecord {
	c.recentMu.RLock()
	defer c.recentMu.RUnlock()
	if limit > c.recentLen {
		limit = c.recentLen
	}
	out := make([]ConnRecord, 0, limit)
	// 从最新开始往回读。
	for i := 0; i < limit; i++ {
		idx := (c.recentIdx - 1 - i + c.recentCap) % c.recentCap
		out = append(out, c.recent[idx])
	}
	return out
}

// Totals 返回全局汇总。
type Totals struct {
	TxBytes     uint64 `json:"tx_bytes"`
	RxBytes     uint64 `json:"rx_bytes"`
	ActiveConns int64  `json:"active_conns"`
	TotalConns  uint64 `json:"total_conns"`
	DeviceCount int    `json:"device_count"`
}

// Totals 返回所有设备的汇总统计。
func (c *Collector) Totals() Totals {
	var t Totals
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.RLock()
		t.DeviceCount += len(shard.devices)
		for _, d := range shard.devices {
			t.TxBytes += d.TxBytes.Load()
			t.RxBytes += d.RxBytes.Load()
			t.ActiveConns += d.ActiveConns.Load()
			t.TotalConns += d.TotalConns.Load()
		}
		shard.mu.RUnlock()
	}
	return t
}

// TrafficSamples 返回最近的流量时序采样(最多 300 点)。
func (c *Collector) TrafficSamples() []TrafficSample {
	c.sampleMu.RLock()
	defer c.sampleMu.RUnlock()
	// 返回全部有效采样点(环形读取)
	out := make([]TrafficSample, 0, c.sampleCap)
	if c.sampleIdx == 0 && c.samples[0].Timestamp == 0 {
		return out // 无数据
	}
	// 从最老到最新顺序读取
	for i := 0; i < c.sampleCap; i++ {
		idx := (c.sampleIdx + i) % c.sampleCap
		if c.samples[idx].Timestamp == 0 {
			continue // 未填满
		}
		out = append(out, c.samples[idx])
	}
	return out
}

// StartSampling 启动后台采样协程(每秒记录全局速率)。
func (c *Collector) StartSampling(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sample()
		}
	}
}

func (c *Collector) sample() {
	now := time.Now()
	t := c.Totals()
	
	c.sampleMu.Lock()
	defer c.sampleMu.Unlock()

	if c.lastSample.IsZero() {
		// 首次采样,无速率
		c.lastSample = now
		c.lastTx = t.TxBytes
		c.lastRx = t.RxBytes
		return
	}

	elapsed := now.Sub(c.lastSample).Seconds()
	if elapsed < 0.5 {
		return // 避免时钟回拨或过快调用
	}

	txRate := uint64(float64(t.TxBytes-c.lastTx) / elapsed)
	rxRate := uint64(float64(t.RxBytes-c.lastRx) / elapsed)

	c.samples[c.sampleIdx] = TrafficSample{
		Timestamp: now.UnixMilli(),
		TxRate:    txRate,
		RxRate:    rxRate,
	}
	c.sampleIdx = (c.sampleIdx + 1) % c.sampleCap

	c.lastSample = now
	c.lastTx = t.TxBytes
	c.lastRx = t.RxBytes
}
