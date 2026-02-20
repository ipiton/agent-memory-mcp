package main

import (
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// FileLogger provides file-based logging for MCP diagnostics
type FileLogger struct {
	logger *zap.Logger
	mu     sync.Mutex
}

// NewFileLogger creates a new file logger for diagnostics
func NewFileLogger(logPath string) (*FileLogger, error) {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return nil, err
	}

	// Create file writer
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	// Configure encoder
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.LevelKey = "level"
	encoderConfig.MessageKey = "message"
	encoderConfig.CallerKey = "caller"
	encoderConfig.StacktraceKey = "stacktrace"

	// Create core with file output
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(file),
		zapcore.InfoLevel, // Log info and above
	)

	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return &FileLogger{
		logger: logger,
	}, nil
}

// Info logs an info message
func (fl *FileLogger) Info(msg string, fields ...zap.Field) {
	if fl == nil || fl.logger == nil {
		return
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	fl.logger.Info(msg, fields...)
}

// Warn logs a warning message
func (fl *FileLogger) Warn(msg string, fields ...zap.Field) {
	if fl == nil || fl.logger == nil {
		return
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	fl.logger.Warn(msg, fields...)
}

// Error logs an error message
func (fl *FileLogger) Error(msg string, fields ...zap.Field) {
	if fl == nil || fl.logger == nil {
		return
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	fl.logger.Error(msg, fields...)
}

// Debug logs a debug message
func (fl *FileLogger) Debug(msg string, fields ...zap.Field) {
	if fl == nil || fl.logger == nil {
		return
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	fl.logger.Debug(msg, fields...)
}

// Sync flushes any buffered log entries
func (fl *FileLogger) Sync() error {
	if fl == nil || fl.logger == nil {
		return nil
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return fl.logger.Sync()
}
