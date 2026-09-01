// Package logger 提供带级别控制、按天切割与自动清理的日志。
//
// 特性:
//   - 级别: debug / info / warn / error
//   - 按天切割: 日志文件名带日期后缀(gateway-2026-09-02.log),跨天自动切换
//   - 自动清理: 超过保留天数的旧日志文件在切割时删除
//   - 双输出: 可同时写文件与控制台
package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level 表示日志级别。
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	}
	return "INFO"
}

// ParseLevel 解析级别字符串,未知值返回 LevelInfo。
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Options 是日志初始化参数。
type Options struct {
	// Path 是日志文件路径(如 /var/log/lanproxy-gateway/gateway.log)。
	// 留空表示不写文件,仅控制台输出。
	Path string
	// Level 是最低输出级别。
	Level Level
	// MaxAgeDays 是日志保留天数,<=0 表示不清理。
	MaxAgeDays int
	// Console 为 true 时同时输出到 stdout。
	Console bool
}

// Logger 是带切割能力的日志器。
type Logger struct {
	mu sync.Mutex

	level      Level
	maxAgeDays int
	console    bool

	// 文件相关
	dir      string // 日志目录
	baseName string // 基础名(不含日期与扩展名),如 "gateway"
	ext      string // 扩展名,如 ".log"
	file     *os.File
	curDate  string // 当前文件对应日期 2006-01-02
}

var (
	defaultLogger *Logger
	defaultOnce   sync.Once
)

// Init 初始化全局日志器。重复调用会替换全局实例。
func Init(opts Options) error {
	l, err := New(opts)
	if err != nil {
		return err
	}
	defaultLogger = l
	return nil
}

// New 创建一个日志器实例。
func New(opts Options) (*Logger, error) {
	l := &Logger{
		level:      opts.Level,
		maxAgeDays: opts.MaxAgeDays,
		console:    opts.Console,
	}
	if opts.Path != "" {
		l.dir = filepath.Dir(opts.Path)
		base := filepath.Base(opts.Path)
		l.ext = filepath.Ext(base)
		if l.ext == "" {
			l.ext = ".log"
			l.baseName = base
		} else {
			l.baseName = strings.TrimSuffix(base, l.ext)
		}
		if err := os.MkdirAll(l.dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建日志目录失败: %w", err)
		}
		if err := l.rotate(time.Now()); err != nil {
			return nil, err
		}
	}
	// 未配置文件时至少保证控制台输出。
	if opts.Path == "" {
		l.console = true
	}
	return l, nil
}

// get 返回全局日志器,未初始化则退化为仅控制台。
func get() *Logger {
	defaultOnce.Do(func() {
		if defaultLogger == nil {
			defaultLogger = &Logger{level: LevelInfo, console: true}
		}
	})
	return defaultLogger
}

// fileNameFor 返回指定日期的日志文件名。
func (l *Logger) fileNameFor(date string) string {
	return filepath.Join(l.dir, fmt.Sprintf("%s-%s%s", l.baseName, date, l.ext))
}

// rotate 切换到指定时间对应的日志文件,并清理过期文件。
// 调用者需持有锁,或在初始化阶段(尚无并发)调用。
func (l *Logger) rotate(now time.Time) error {
	date := now.Format("2006-01-02")
	if l.file != nil && l.curDate == date {
		return nil
	}
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	f, err := os.OpenFile(l.fileNameFor(date), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	l.file = f
	l.curDate = date

	// 维护一个指向当前日志的软链(gateway.log -> gateway-2026-09-02.log),便于 tail。
	link := filepath.Join(l.dir, l.baseName+l.ext)
	_ = os.Remove(link)
	_ = os.Symlink(l.fileNameFor(date), link)

	l.cleanup(now)
	return nil
}

// cleanup 删除超过保留天数的日志文件。
func (l *Logger) cleanup(now time.Time) {
	if l.maxAgeDays <= 0 || l.dir == "" {
		return
	}
	cutoff := now.AddDate(0, 0, -l.maxAgeDays)
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	prefix := l.baseName + "-"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, l.ext) {
			continue
		}
		datePart := strings.TrimSuffix(strings.TrimPrefix(name, prefix), l.ext)
		t, err := time.Parse("2006-01-02", datePart)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			_ = os.Remove(filepath.Join(l.dir, name))
		}
	}
}

// output 写入一条日志。
func (l *Logger) output(lv Level, msg string) {
	if lv < l.level {
		return
	}
	now := time.Now()
	line := fmt.Sprintf("%s [%s] %s\n", now.Format("2006-01-02 15:04:05"), lv.String(), msg)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.dir != "" {
		// 跨天则切割。
		if err := l.rotate(now); err == nil && l.file != nil {
			_, _ = io.WriteString(l.file, line)
		}
	}
	if l.console {
		_, _ = io.WriteString(os.Stdout, line)
	}
}

// CurrentFile 返回当前日志文件的完整路径。未配置文件时返回空串。
func (l *Logger) CurrentFile() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dir == "" {
		return ""
	}
	return l.fileNameFor(l.curDate)
}

// Tail 读取当前日志文件的最后 n 行。未配置文件时返回空。
func (l *Logger) Tail(n int) ([]string, error) {
	path := l.CurrentFile()
	if path == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 200
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// CurrentFile 返回全局日志器的当前文件路径。
func CurrentFile() string { return get().CurrentFile() }

// Tail 读取全局日志器当前文件的最后 n 行。
func Tail(n int) ([]string, error) { return get().Tail(n) }

// Close 关闭底层文件。
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

// 实例方法。
func (l *Logger) Debugf(format string, args ...any) { l.output(LevelDebug, fmt.Sprintf(format, args...)) }
func (l *Logger) Infof(format string, args ...any)  { l.output(LevelInfo, fmt.Sprintf(format, args...)) }
func (l *Logger) Warnf(format string, args ...any)  { l.output(LevelWarn, fmt.Sprintf(format, args...)) }
func (l *Logger) Errorf(format string, args ...any) { l.output(LevelError, fmt.Sprintf(format, args...)) }

// 包级函数,使用全局日志器。
func Debugf(format string, args ...any) { get().Debugf(format, args...) }
func Infof(format string, args ...any)  { get().Infof(format, args...) }
func Warnf(format string, args ...any)  { get().Warnf(format, args...) }
func Errorf(format string, args ...any) { get().Errorf(format, args...) }

// Close 关闭全局日志器。
func Close() error {
	if defaultLogger != nil {
		return defaultLogger.Close()
	}
	return nil
}
