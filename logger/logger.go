package logger

import (
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/flokiorg/lokihub/logger/crashlog"
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
	zerolog.TimeFieldFormat = time.RFC3339

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

	// Level mapping: Logrus-style (0=Panic,1=Fatal,2=Error,3=Warn,4=Info,5=Debug,6=Trace)
	// to Zerolog (Trace=-1,Debug=0,Info=1,Warn=2,Error=3,Fatal=4,Panic=5)
	level, err := strconv.Atoi(logLevel)
	if err != nil {
		level = int(zerolog.InfoLevel)
	}

	var zLevel zerolog.Level
	switch level {
	case 6:
		zLevel = zerolog.TraceLevel
	case 5:
		zLevel = zerolog.DebugLevel
	case 4:
		zLevel = zerolog.InfoLevel
	case 3:
		zLevel = zerolog.WarnLevel
	case 2:
		zLevel = zerolog.ErrorLevel
	case 1:
		zLevel = zerolog.FatalLevel
	case 0:
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

	crashLogPath := filepath.Join(workdir, logDir, "crash.log")
	if crashFile, err := os.OpenFile(crashLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		crashlog.RedirectStderr(crashFile)
	}

	fileLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    50, // MB — rotate before age if file grows large
		MaxAge:     3,
		MaxBackups: 3,
	}

	// File writer uses ConsoleWriter (no color) for human-readable ops format.
	// Console writer keeps colored output for interactive use.
	fileConsoleWriter := zerolog.ConsoleWriter{
		Out:        fileLogger,
		TimeFormat: time.RFC3339,
		NoColor:    true,
	}
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.DateTime,
	}
	multi := zerolog.MultiLevelWriter(consoleWriter, fileConsoleWriter)
	Writer = multi

	zLevel := zerolog.GlobalLevel()
	Logger = zerolog.New(multi).
		With().
		Timestamp().
		Logger().
		Level(zLevel)

	if zLevel <= zerolog.DebugLevel {
		buildInfo, _ := debug.ReadBuildInfo()
		Logger = Logger.With().
			Caller().
			Interface("build_info", buildInfo).
			Logger()
	}

	// HttpLogger writes to file only (no console noise for HTTP traffic)
	HttpLogger = zerolog.New(fileConsoleWriter).
		With().
		Timestamp().
		Logger().
		Level(zLevel)

	return nil
}

func GetLogFilePath() string {
	return logFilePath
}
