// Package logger provides file-based structured logging for MCP diagnostics.
package logger

import (
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// FileLogger provides file-based logging for MCP diagnostics.
// Thread-safety is provided by the underlying zap.Logger.
type FileLogger struct {
	Logger *zap.Logger
}

// New creates a new FileLogger that writes JSON-formatted logs to the given path.
// Logs are automatically rotated: max 50 MB per file, 3 backups, 30 days retention.
func New(logPath string) (*FileLogger, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return nil, err
	}

	writer := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    50, // MB
		MaxBackups: 3,
		MaxAge:     30, // days
		Compress:   true,
	}

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.LevelKey = "level"
	encoderConfig.MessageKey = "message"
	encoderConfig.CallerKey = "caller"
	encoderConfig.StacktraceKey = "stacktrace"

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(writer),
		zapcore.InfoLevel,
	)

	zapLogger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return &FileLogger{
		Logger: zapLogger,
	}, nil
}

// Info logs a message at info level.
func (fl *FileLogger) Info(msg string, fields ...zap.Field) {
	if fl == nil || fl.Logger == nil {
		return
	}
	fl.Logger.Info(msg, fields...)
}

// Warn logs a message at warn level.
func (fl *FileLogger) Warn(msg string, fields ...zap.Field) {
	if fl == nil || fl.Logger == nil {
		return
	}
	fl.Logger.Warn(msg, fields...)
}

// Error logs a message at error level.
func (fl *FileLogger) Error(msg string, fields ...zap.Field) {
	if fl == nil || fl.Logger == nil {
		return
	}
	fl.Logger.Error(msg, fields...)
}

// Debug logs a message at debug level.
func (fl *FileLogger) Debug(msg string, fields ...zap.Field) {
	if fl == nil || fl.Logger == nil {
		return
	}
	fl.Logger.Debug(msg, fields...)
}

// Close syncs and closes the logger.
func (fl *FileLogger) Close() error {
	if fl == nil || fl.Logger == nil {
		return nil
	}
	return fl.Logger.Sync()
}

// Sync flushes any buffered log entries to disk.
func (fl *FileLogger) Sync() error {
	return fl.Close()
}
