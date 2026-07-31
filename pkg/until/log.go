package until

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"github.com/sirupsen/logrus"
)

var Log *logrus.Logger
var logPaths = []string{".", "logs", "app"}
var LogPath = filepath.Join(logPaths...)

func init() {
	Log = initLogger()
}

func initLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetFormatter(&Log4jFormatter{})
	logger.SetReportCaller(true) // 显示调用函数
	logger.SetLevel(logrus.InfoLevel)

	// 配置每日日志切割
	println(LogPath)
	os.MkdirAll(LogPath, os.ModePerm) // 自动创建目录

	var options []rotatelogs.Option
	options = append(options, rotatelogs.WithRotationTime(24*time.Hour))  // 每日切割
	options = append(options, rotatelogs.WithMaxAge(7*24*time.Hour))      // 保留7天
	options = append(options, rotatelogs.WithRotationSize(100*1024*1024)) // 100MB切割

	// 创建切割日志写入器

	rotateLogger, err := rotatelogs.New(
		filepath.Join(LogPath, "app_%Y%m%d.log"), // 每日切割文件格式
		options...,
	)
	if err != nil {
		fmt.Printf("failed to create rotate logs: %v\n", err)
		// 如果创建失败，回退到标准输出
		logger.Out = os.Stdout
	} else {
		// 同时输出到控制台和日志文件
		logger.SetOutput(io.MultiWriter(os.Stdout, rotateLogger))
	}

	// 添加goroutine ID的hook
	logger.AddHook(&GoroutineIDHook{})

	return logger
}

// GoroutineIDHook 用于添加goroutine ID
type GoroutineIDHook struct{}

func (hook *GoroutineIDHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (hook *GoroutineIDHook) Fire(entry *logrus.Entry) error {
	entry.Data["goroutine"] = fmt.Sprintf("%d", getGoroutineID())
	return nil
}

func getGoroutineID() uint64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	// Extract the 6504 out of "goroutine 6504 ["
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	b = b[:bytes.IndexByte(b, ' ')]
	n, _ := strconv.ParseUint(string(b), 10, 64)
	return n
}

// Log4jFormatter 模仿Java log4j的格式
type Log4jFormatter struct{}

func (f *Log4jFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	timestamp := entry.Time.Format("2006-01-02 15:04:05.000")
	level := strings.ToUpper(entry.Level.String())[0:4] // 只取前4位：INFO, WARN, ERRO

	callerInfo := "???"
	if entry.HasCaller() {
		// 只显示文件名和行号
		_, file := filepath.Split(entry.Caller.File)
		callerInfo = fmt.Sprintf("%s:%d", file, entry.Caller.Line)
	}

	// log4j风格格式：时间戳 [级别] 调用位置 - 消息内容
	msg := fmt.Sprintf("%s [%s] %s - %s",
		timestamp,
		level,
		callerInfo,
		entry.Message)

	// 附加字段处理 (key=value格式)
	if len(entry.Data) > 0 {
		for k, v := range entry.Data {
			if k != logrus.ErrorKey { // 排除错误字段，单独处理
				msg += fmt.Sprintf(" %s=%v", k, v)
			}
		}
	}

	// 错误处理
	if err, ok := entry.Data[logrus.ErrorKey].(error); ok {
		msg += fmt.Sprintf("\nERROR: %v", err)
	}

	return []byte(msg + "\n"), nil
}

type GinLogWriter struct{}

func (w *GinLogWriter) Write(p []byte) (n int, err error) {
	log := Log
	// 去除尾部换行符
	msg := strings.TrimSpace(string(p))
	if len(msg) > 0 {
		// 根据日志级别转发到 logrus
		switch {
		case strings.Contains(msg, "[WARNING]"):
			log.Warn(msg)
		case strings.Contains(msg, "[DEBUG]"):
			log.Debug(msg)
		case strings.Contains(msg, "[ERROR]"):
			log.Error(msg)
		default:
			log.Info(msg)
		}
	}

	return len(p), nil
}
