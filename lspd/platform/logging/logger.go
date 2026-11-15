package logging

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Default logging configuration values (best practices for production)
const (
	DefaultLogDir        = "logs"
	LogFileName          = "lspd.log"
	DefaultLogMaxSizeMB  = 100 // megabytes - industry standard for production logs
	DefaultLogMaxBackups = 5   // number of backup files to keep
	DefaultLogMaxAgeDays = 30  // days to retain old log files
	DefaultLogCompress   = true
)

// InitLogger configures the global zerolog logger with console and file output.
// Configuration is read from environment variables:
//   - LOG_DIR: Directory for log files (default: "logs")
//   - LOG_MAX_SIZE_MB: Max size per file before rotation in MB (default: 100)
//   - LOG_MAX_BACKUPS: Max number of backup files to keep (default: 5)
//   - LOG_MAX_AGE_DAYS: Max days to retain old log files (default: 30)
//   - LOG_COMPRESS: Compress rotated files with gzip (default: true)
func InitLogger() {
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.DateTime,
	}

	// Read configuration from environment variables with defaults
	logDir := getEnvOrDefault("LOG_DIR", DefaultLogDir)
	logFilePath := filepath.Join(logDir, LogFileName)
	logMaxSizeMB := getEnvAsIntOrDefault("LOG_MAX_SIZE_MB", DefaultLogMaxSizeMB)
	logMaxBackups := getEnvAsIntOrDefault("LOG_MAX_BACKUPS", DefaultLogMaxBackups)
	logMaxAgeDays := getEnvAsIntOrDefault("LOG_MAX_AGE_DAYS", DefaultLogMaxAgeDays)
	logCompress := getEnvAsBoolOrDefault("LOG_COMPRESS", DefaultLogCompress)

	fileLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    logMaxSizeMB,
		MaxBackups: logMaxBackups,
		MaxAge:     logMaxAgeDays,
		Compress:   logCompress,
	}

	multi := zerolog.MultiLevelWriter(consoleWriter, fileLogger)

	log.Logger = zerolog.New(multi).With().Timestamp().Logger()
}

// getEnvOrDefault returns the value of an environment variable or a default value.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvAsIntOrDefault returns the value of an environment variable as an integer or a default value.
func getEnvAsIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// getEnvAsBoolOrDefault returns the value of an environment variable as a boolean or a default value.
func getEnvAsBoolOrDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}
