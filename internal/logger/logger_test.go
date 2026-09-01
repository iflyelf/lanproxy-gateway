package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"debug": LevelDebug, "info": LevelInfo, "warn": LevelWarn,
		"warning": LevelWarn, "error": LevelError, "": LevelInfo, "xxx": LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q)=%v, 期望 %v", in, got, want)
		}
	}
}

func TestLevelFilter(t *testing.T) {
	dir := t.TempDir()
	l, err := New(Options{Path: filepath.Join(dir, "test.log"), Level: LevelWarn, Console: false})
	if err != nil {
		t.Fatal(err)
	}
	l.Debugf("debug msg")
	l.Infof("info msg")
	l.Warnf("warn msg")
	l.Errorf("error msg")
	l.Close()

	date := time.Now().Format("2006-01-02")
	data, _ := os.ReadFile(filepath.Join(dir, "test-"+date+".log"))
	content := string(data)
	if strings.Contains(content, "debug msg") || strings.Contains(content, "info msg") {
		t.Error("低于 warn 级别的日志不应写入")
	}
	if !strings.Contains(content, "warn msg") || !strings.Contains(content, "error msg") {
		t.Error("warn/error 级别的日志应写入")
	}
}

func TestFileNaming(t *testing.T) {
	dir := t.TempDir()
	l, err := New(Options{Path: filepath.Join(dir, "gateway.log"), Level: LevelInfo, Console: false})
	if err != nil {
		t.Fatal(err)
	}
	l.Infof("hello")
	l.Close()

	date := time.Now().Format("2006-01-02")
	want := filepath.Join(dir, "gateway-"+date+".log")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("期望日志文件 %s 存在: %v", want, err)
	}
	// 软链应指向当天文件
	link := filepath.Join(dir, "gateway.log")
	if target, err := os.Readlink(link); err != nil || !strings.HasSuffix(target, date+".log") {
		t.Errorf("软链应指向当天日志, target=%q err=%v", target, err)
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	// 造一个 10 天前的旧日志
	oldDate := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	oldFile := filepath.Join(dir, "gateway-"+oldDate+".log")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 初始化(保留 7 天),应触发清理
	l, err := New(Options{Path: filepath.Join(dir, "gateway.log"), Level: LevelInfo, MaxAgeDays: 7, Console: false})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("10 天前的日志应被清理,但仍存在")
	}
}

func TestNoCleanupWhenZero(t *testing.T) {
	dir := t.TempDir()
	oldDate := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	oldFile := filepath.Join(dir, "gateway-"+oldDate+".log")
	os.WriteFile(oldFile, []byte("old"), 0o644)

	l, err := New(Options{Path: filepath.Join(dir, "gateway.log"), Level: LevelInfo, MaxAgeDays: 0, Console: false})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if _, err := os.Stat(oldFile); err != nil {
		t.Errorf("max_age_days=0 时不应清理旧日志")
	}
}

func TestConsoleOnly(t *testing.T) {
	// 无 Path 时应仅控制台,不报错
	l, err := New(Options{Level: LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	l.Infof("console only")
	if err := l.Close(); err != nil {
		t.Errorf("关闭无文件日志器不应报错: %v", err)
	}
}
