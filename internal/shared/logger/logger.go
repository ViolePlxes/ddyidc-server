package logger

import (
	"github.com/zeromicro/go-zero/core/logx"
)

// Init 初始化日志
func Init(config logx.LogConf) { logx.MustSetup(config) }

// Info 信息日志
func Info(msg string, fields ...logx.LogField) {
	logx.Infow(msg, fields...)
}

// Error 错误日志
func Error(msg string, fields ...logx.LogField) {
	logx.Errorw(msg, fields...)
}

// Debug 调试日志
func Debug(msg string, fields ...logx.LogField) {
	logx.Debugw(msg, fields...)
}
