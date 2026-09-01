package stats

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Collector 聚合每设备(源 IP)的流量与连接统计,并保存最近的连接记录。
// 所有方法均并发安全。
type Collector struct {
	mu      sync.RWMutex
	devices map[string]*deviceStat

	// recent 是环形缓冲的最近连接记录。
	recent    []ConnRecord
	recentCap int
	recentIdx int
	recentLen int

	connSeq atomic.Uint64
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
	Upstream  string `json:"upstream"` // proxy 或 direct
	Failed    bool   `json:"failed"`
}

// New 创建统计收集器,recentCap 为最近连接记录的保留条数。
func New(recentCap int) *Collector {
	if recentCap <= 0 {
		recentCap = 500
	}
	return &Collector{
		devices:   make(map[string]*deviceStat),
		recent:    make([]ConnRecord, recentCap),
		recentCap: recentCap,
	}
}

func (c *Collector) device(ip string) *deviceStat {
	c.mu.RLock()
	d, ok := c.devices[ip]
	c.mu.RUnlock()
	if ok {
		return d
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok = c.devices[ip]; ok {
		return d
	}
	d = &deviceStat{IP: ip}
	c.devices[ip] = d
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

// CloseConn 记录一条连接的结束及其字节数。
func (c *Collector) CloseConn(id uint64, srcIP, dstIP string, dstPort int, tx, rx uint64, upstream string, failed bool) {
	d := c.device(srcIP)
	d.ActiveConns.Add(-1)
	d.TxBytes.Add(tx)
	d.RxBytes.Add(rx)
	d.LastSeen.Store(time.Now().Unix())

	rec := ConnRecord{
		ID:        id,
		SrcIP:     srcIP,
		DstIP:     dstIP,
		DstPort:   dstPort,
		StartedAt: 0,
		ClosedAt:  time.Now().Unix(),
		TxBytes:   tx,
		RxBytes:   rx,
		Upstream:  upstream,
		Failed:    failed,
	}
	c.mu.Lock()
	c.recent[c.recentIdx] = rec
	c.recentIdx = (c.recentIdx + 1) % c.recentCap
	if c.recentLen < c.recentCap {
		c.recentLen++
	}
	c.mu.Unlock()
}

// Devices 返回所有设备的统计快照,按上行+下行字节降序。
func (c *Collector) Devices() []DeviceStat {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]DeviceStat, 0, len(c.devices))
	for _, d := range c.devices {
		out = append(out, DeviceStat{
			IP:          d.IP,
			TxBytes:     d.TxBytes.Load(),
			RxBytes:     d.RxBytes.Load(),
			TotalConns:  d.TotalConns.Load(),
			ActiveConns: d.ActiveConns.Load(),
			LastSeen:    d.LastSeen.Load(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].TxBytes+out[i].RxBytes > out[j].TxBytes+out[j].RxBytes
	})
	return out
}

// DeviceByIP 返回指定 IP 的统计,不存在返回零值与 false。
func (c *Collector) DeviceByIP(ip string) (DeviceStat, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.devices[ip]
	if !ok {
		return DeviceStat{}, false
	}
	return DeviceStat{
		IP:          d.IP,
		TxBytes:     d.TxBytes.Load(),
		RxBytes:     d.RxBytes.Load(),
		TotalConns:  d.TotalConns.Load(),
		ActiveConns: d.ActiveConns.Load(),
		LastSeen:    d.LastSeen.Load(),
	}, true
}

// Recent 返回最近的连接记录,最新的在前。
func (c *Collector) Recent(limit int) []ConnRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if limit <= 0 || limit > c.recentLen {
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
	c.mu.RLock()
	defer c.mu.RUnlock()
	var t Totals
	t.DeviceCount = len(c.devices)
	for _, d := range c.devices {
		t.TxBytes += d.TxBytes.Load()
		t.RxBytes += d.RxBytes.Load()
		t.ActiveConns += d.ActiveConns.Load()
		t.TotalConns += d.TotalConns.Load()
	}
	return t
}
