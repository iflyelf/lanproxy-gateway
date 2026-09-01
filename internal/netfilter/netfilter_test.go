package netfilter

import "testing"

func TestRender(t *testing.T) {
	m := New(Options{
		ListenPort:  12345,
		FwMark:      1,
		LANIface:    "eth0",
		BypassCIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"},
	})
	out, err := m.render()
	if err != nil {
		t.Fatalf("render 出错: %v", err)
	}
	for _, want := range []string{
		"table inet lanproxy_gw",
		"iifname != \"eth0\" return",
		"tproxy ip to 127.0.0.1:12345",
		"meta mark set 1",
		"10.0.0.0/8, 192.168.0.0/16",
		"type filter hook prerouting priority mangle",
	} {
		if !contains(out, want) {
			t.Errorf("渲染结果缺少 %q\n完整内容:\n%s", want, out)
		}
	}
}

func TestRenderNoIface(t *testing.T) {
	m := New(Options{ListenPort: 1, FwMark: 1})
	out, err := m.render()
	if err != nil {
		t.Fatalf("render 出错: %v", err)
	}
	if contains(out, "iifname") {
		t.Errorf("未指定网卡时不应包含 iifname 过滤:\n%s", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
