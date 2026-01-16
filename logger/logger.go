package logger

import (
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	logDir      = "log"
	logFilename = "nwc.log"
)

var Logger zerolog.Logger
var HttpLogger zerolog.Logger
var logFilePath string
var Writer io.Writer

func Init(logLevel string) {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	// Default console writer
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.DateTime,
	}
	Writer = consoleWriter

	Logger = zerolog.New(consoleWriter).
		With().
		Timestamp().
		Logger()

	// HttpLogger initially discards
	HttpLogger = zerolog.New(io.Discard).
		With().
		Timestamp().
		Logger()

	level, err := strconv.Atoi(logLevel)
	if err != nil {
		level = int(zerolog.InfoLevel)
	}

	// Helper to map numeric level to zerolog.Level if needed,
	// checking against specific values or just casting if assuming compatibility.
	// However, Logrus and Zerolog levels might differ slightly in integer values.
	// Logrus: Panic=0, Fatal=1, Error=2, Warn=3, Info=4, Debug=5, Trace=6
	// Zerolog: Debug=0, Info=1, Warn=2, Error=3, Fatal=4, Panic=5, NoLevel=, Disabled=-1, Trace=-1 (actually Trace is 0 in recent versions? No, Trace is -1 by default or 0 depends on version. Actually Debug=0, Info=1...)
	// UPDATE: Zerolog: Trace=-1, Debug=0, Info=1, Warn=2, Error=3, Fatal=4, Panic=5
	// Logrus: Panic=0, Fatal=1, Error=2, Warn=3, Info=4, Debug=5, Trace=6
	// The levels are INVERTED/DIFFERENT. We need a mapping.

	var zLevel zerolog.Level
	switch level {
	case 6: // Trace
		zLevel = zerolog.TraceLevel
	case 5: // Debug
		zLevel = zerolog.DebugLevel
	case 4: // Info
		zLevel = zerolog.InfoLevel
	case 3: // Warn
		zLevel = zerolog.WarnLevel
	case 2: // Error
		zLevel = zerolog.ErrorLevel
	case 1: // Fatal
		zLevel = zerolog.FatalLevel
	case 0: // Panic
		zLevel = zerolog.PanicLevel
	default:
		zLevel = zerolog.InfoLevel
	}

	zerolog.SetGlobalLevel(zLevel)
	Logger = Logger.Level(zLevel)
	HttpLogger = HttpLogger.Level(zLevel)

	if zLevel <= zerolog.DebugLevel {
		buildInfo, _ := debug.ReadBuildInfo()
		Logger = Logger.With().
			Caller().
			Interface("build_info", buildInfo).
			Logger()
		Logger.Debug().Msg("Zerolog caller reporting enabled in debug mode")
	}
}

func AddFileLogger(workdir string) error {
	logFilePath = filepath.Join(workdir, logDir, logFilename)
	fileLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxAge:     3,
		MaxBackups: 3,
	}

	// MultiWriter to write to both console and file
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.DateTime,
	}
	multi := zerolog.MultiLevelWriter(consoleWriter, fileLogger)
	Writer = multi

	Logger = zerolog.New(multi).
		With().
		Timestamp().
		Logger()

	// HttpLogger also writes to file
	HttpLogger = zerolog.New(fileLogger).
		With().
		Timestamp().
		Logger()

	return nil
}

func GetLogFilePath() string {
	return logFilePath
}
