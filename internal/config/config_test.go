package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}
}

func TestValidateRejectsBadUpstreamType(t *testing.T) {
	c := Default()
	c.Upstream.Type = "ftp"
	if err := c.Validate(); err == nil {
		t.Error("非法 upstream.type 应被拒绝")
	}
}

func TestValidateRejectsBadCIDR(t *testing.T) {
	c := Default()
	c.TProxy.BypassCIDRs = []string{"not-a-cidr"}
	if err := c.Validate(); err == nil {
		t.Error("非法 CIDR 应被拒绝")
	}
}

func TestValidateRejectsEmptyCreds(t *testing.T) {
	c := Default()
	c.Web.Password = ""
	if err := c.Validate(); err == nil {
		t.Error("空密码应被拒绝")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	orig := Default()
	orig.Upstream.Address = "127.0.0.1:7890"
	orig.Web.Username = "tester"
	if err := orig.Save(path); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if loaded.Web.Username != "tester" {
		t.Errorf("往返后 username 不一致: %q", loaded.Web.Username)
	}
	if loaded.Upstream.Address != "127.0.0.1:7890" {
		t.Errorf("往返后 upstream 地址不一致: %q", loaded.Upstream.Address)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	c, err := Load("/nonexistent/path/gateway.yaml")
	if err != nil {
		t.Fatalf("缺失文件应返回默认配置而非错误: %v", err)
	}
	if c.Upstream.Address != "127.0.0.1:7890" {
		t.Errorf("应返回默认配置")
	}
}
