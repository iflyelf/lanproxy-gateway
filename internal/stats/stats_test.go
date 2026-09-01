package stats

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestOpenRecordConn(t *testing.T) {
	c := New(100)
	id := c.OpenConn("10.0.0.5", "1.1.1.1", 443, "proxy")
	if id == 0 {
		t.Fatal("OpenConn 应返回非零 ID")
	}
	total := c.Totals()
	if total.ActiveConns != 1 {
		t.Errorf("活动连接应为 1, 实际 %d", total.ActiveConns)
	}
	c.RecordConn(id, "10.0.0.5", "1.1.1.1", 443, 1000, 2000, "proxy", false)
	total = c.Totals()
	if total.ActiveConns != 0 {
		t.Errorf("记录后活动连接应归零, 实际 %d", total.ActiveConns)
	}
	if total.TxBytes != 1000 || total.RxBytes != 2000 {
		t.Errorf("流量统计错误: tx=%d rx=%d", total.TxBytes, total.RxBytes)
	}
	if total.TotalConns != 1 {
		t.Errorf("累计连接应为 1, 实际 %d", total.TotalConns)
	}
}

func TestRecentSingleRecord(t *testing.T) {
	// 验证不再有 CloseConn 双调用导致的重复记录
	c := New(100)
	id := c.OpenConn("10.0.0.5", "1.1.1.1", 443, "proxy")
	c.RecordConn(id, "10.0.0.5", "1.1.1.1", 443, 100, 200, "proxy", false)
	recent := c.Recent(10)
	if len(recent) != 1 {
		t.Errorf("应只有 1 条连接记录, 实际 %d", len(recent))
	}
}

func TestConcurrentSafety(t *testing.T) {
	// -race 下验证分片锁并发安全
	c := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ip := "10.0.0." + string(rune('0'+n%10))
			for j := 0; j < 100; j++ {
				id := c.OpenConn(ip, "1.1.1.1", 443, "proxy")
				c.RecordConn(id, ip, "1.1.1.1", 443, 10, 20, "proxy", false)
			}
		}(i)
	}
	// 并发读
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Totals()
				c.Devices()
				c.Recent(50)
			}
		}()
	}
	wg.Wait()
	total := c.Totals()
	if total.ActiveConns != 0 {
		t.Errorf("所有连接关闭后活动连接应为 0, 实际 %d", total.ActiveConns)
	}
	if total.TotalConns != 5000 {
		t.Errorf("累计连接应为 5000, 实际 %d", total.TotalConns)
	}
}

func TestSampling(t *testing.T) {
	c := New(100)
	// 手动注入流量并采样
	id := c.OpenConn("10.0.0.5", "1.1.1.1", 443, "proxy")
	c.RecordConn(id, "10.0.0.5", "1.1.1.1", 443, 1000, 2000, "proxy", false)

	// 首次采样(建立基线)
	c.sample()
	// 模拟时间流逝
	c.lastSample = time.Now().Add(-2 * time.Second)
	id2 := c.OpenConn("10.0.0.5", "1.1.1.1", 443, "proxy")
	c.RecordConn(id2, "10.0.0.5", "1.1.1.1", 443, 3000, 4000, "proxy", false)
	c.sample()

	samples := c.TrafficSamples()
	if len(samples) == 0 {
		t.Error("采样后应有数据点")
	}
}

func TestStartSamplingStops(t *testing.T) {
	c := New(100)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.StartSampling(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("StartSampling 未在 ctx 取消后退出")
	}
}
