package logger

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog"
	"golang.org/x/term"
)

type LogLevel = zerolog.Level

const (
	DEBUG = zerolog.DebugLevel
	INFO  = zerolog.InfoLevel
	WARN  = zerolog.WarnLevel
	ERROR = zerolog.ErrorLevel
	FATAL = zerolog.FatalLevel

	Component = "component"
)

var (
	logLevelNames = map[LogLevel]string{
		DEBUG: "DEBUG",
		INFO:  "INFO",
		WARN:  "WARN",
		ERROR: "ERROR",
		FATAL: "FATAL",
	}

	// currentLevel and logger are read on every log call while a reload
	// may change them, so they are atomics; mu serializes the changes and
	// guards writers and logFile.
	currentLevel  atomic.Int32
	logger        atomic.Pointer[zerolog.Logger]
	logFile       *rotatingFile
	once          sync.Once
	mu            sync.Mutex
	writers       []io.Writer
	consoleWriter zerolog.ConsoleWriter
)

func init() {
	currentLevel.Store(int32(INFO))
	once.Do(func() {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
		// Log lines name their source relative to the module, never by the
		// build machine's absolute path.
		zerolog.CallerMarshalFunc = marshalCaller

		isTTY := term.IsTerminal(int(os.Stdout.Fd()))

		consoleWriter = zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "15:04:05", // TODO: make it configurable???

			// Custom formatter to handle multiline strings and JSON objects
			FormatFieldValue: formatFieldValue,
			FormatCaller:     formatCaller(isTTY),
			PartsOrder: []string{
				zerolog.TimestampFieldName,
				zerolog.LevelFieldName,
				Component,
				zerolog.CallerFieldName,
				zerolog.MessageFieldName,
			},
			FieldsExclude: []string{Component},
			FormatPrepare: func(fields map[string]any) error {
				if isTTY {
					fields[Component] = fmt.Sprintf("\x1b[33m%v\x1b[0m", fields[Component])
				}
				return nil
			},
			NoColor: !isTTY,
		}

		writers = append(writers, consoleWriter)

		l := zerolog.New(io.MultiWriter(writers...)).With().Timestamp().Caller().Logger()
		logger.Store(&l)
	})
}

// setOutputLocked points the logger at the current writers. mu is held.
func setOutputLocked() {
	l := logger.Load().Output(io.MultiWriter(writers...))
	logger.Store(&l)
}

func formatFieldValue(i any) string {
	var s string

	switch val := i.(type) {
	case string:
		s = val
	case []byte:
		s = string(val)
	default:
		return fmt.Sprintf("%v", i)
	}

	if unquoted, err := strconv.Unquote(s); err == nil {
		s = unquoted
	}

	if strings.Contains(s, "\n") {
		return fmt.Sprintf("\n%s", s)
	}

	if strings.Contains(s, " ") {
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			return s
		}
		return fmt.Sprintf("%q", s)
	}

	return s
}

func SetLevel(level LogLevel) {
	mu.Lock()
	defer mu.Unlock()
	currentLevel.Store(int32(level))
	zerolog.SetGlobalLevel(level)
}

func SetConsoleLevel(level LogLevel) {
	mu.Lock()
	defer mu.Unlock()
	l := logger.Load().Level(level)
	logger.Store(&l)
}

func DisableConsole() {
	mu.Lock()
	defer mu.Unlock()
	writers[0] = io.Discard
	setOutputLocked()
}

func EnableConsole() {
	mu.Lock()
	defer mu.Unlock()
	writers[0] = consoleWriter
	setOutputLocked()
}

// SetConsoleOutput sends the console log to w instead of stdout, for a
// command whose stdout another program parses. A disabled console stays
// disabled.
func SetConsoleOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	consoleWriter.Out = w
	if _, console := writers[0].(zerolog.ConsoleWriter); console {
		writers[0] = consoleWriter
		setOutputLocked()
	}
}

func GetLevel() LogLevel {
	return LogLevel(currentLevel.Load())
}

// ParseLevel converts a case-insensitive level name to a LogLevel.
// Returns the level and true if valid, or (INFO, false) if unrecognized.
func ParseLevel(s string) (LogLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return DEBUG, true
	case "info":
		return INFO, true
	case "warn", "warning":
		return WARN, true
	case "error":
		return ERROR, true
	case "fatal":
		return FATAL, true
	default:
		return INFO, false
	}
}

// SetLevelFromString sets the log level from a string value.
// If the string is empty or not a recognized level name, the current level is kept.
func SetLevelFromString(s string) {
	if s == "" {
		return
	}
	if level, ok := ParseLevel(s); ok {
		SetLevel(level)
	}
}

// EnableFileLogging also writes the log to filePath, which is created owner
// only (0600, in a 0700 directory when the directory is new) and rotated by
// size (see SetRotation). Calling it again moves file logging to filePath:
// the new file is opened before the old one is closed, so a file that cannot
// be opened leaves the current one in place.
func EnableFileLogging(filePath string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}
	maxSize, maxFiles := rotationLimits()
	newFile, err := openRotatingFile(filePath, maxSize, maxFiles)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	mu.Lock()
	old := logFile
	logFile = newFile
	if len(writers) > 1 {
		writers[1] = newFile
	} else {
		writers = append(writers, newFile)
	}
	setOutputLocked()
	mu.Unlock()

	if old != nil {
		_ = old.Close()
	}
	return nil
}

func DisableFileLogging() {
	mu.Lock()
	old := logFile
	logFile = nil
	if len(writers) > 1 {
		writers = writers[:1]
		setOutputLocked()
	}
	mu.Unlock()

	if old != nil {
		_ = old.Close()
	}
}

func ConfigureFromEnv() {
	if logFile := os.Getenv("COMPA_LOG_FILE"); logFile != "" {
		if strings.HasPrefix(logFile, "~/") {
			if home := os.Getenv("HOME"); home != "" {
				logFile = filepath.Join(home, logFile[2:])
			}
		}

		if err := EnableFileLogging(logFile); err != nil {
			fmt.Fprintf(os.Stderr, "failed to enable file logging: %v\n", err)
		} else {
			DisableConsole()
		}
	}
}

const (
	locUnknown = "<unknown>"
)

func getPackageNameFromFile(filePath string) string {
	dir := filepath.Dir(filePath)
	importPath := filepath.ToSlash(dir)

	parts := strings.Split(importPath, "/")
	if len(parts) == 0 {
		return locUnknown
	}

	pkg := parts[len(parts)-1]
	if pkg == "." {
		return "<main>"
	}

	return pkg
}

func getCallerSkip() (int, string) {
	for i := 2; i < 15; i++ {
		pc, file, _, ok := runtime.Caller(i)
		if !ok {
			continue
		}

		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}

		// bypass common loggers
		if strings.HasSuffix(file, "/logger.go") ||
			strings.HasSuffix(file, "/logger_3rd_party.go") ||
			strings.HasSuffix(file, "/log.go") {
			continue
		}

		funcName := fn.Name()
		if strings.HasPrefix(funcName, "runtime.") {
			continue
		}

		return i - 1, getPackageNameFromFile(file)
	}

	return 3, locUnknown
}

//nolint:zerologlint
func getEvent(logger zerolog.Logger, level LogLevel) *zerolog.Event {
	switch level {
	case zerolog.DebugLevel:
		return logger.Debug()
	case zerolog.InfoLevel:
		return logger.Info()
	case zerolog.WarnLevel:
		return logger.Warn()
	case zerolog.ErrorLevel:
		return logger.Error()
	case zerolog.FatalLevel:
		return logger.Fatal()
	default:
		return logger.Info()
	}
}

func logMessage(level LogLevel, component string, message string, fields map[string]any) {
	if level < LogLevel(currentLevel.Load()) {
		return
	}

	skip, pkg := getCallerSkip()

	event := getEvent(*logger.Load(), level)

	if component == "" {
		component = pkg
	}

	event.Str(Component, component)

	appendFields(event, fields)

	event.CallerSkipFrame(skip).Msg(Redact(message))
}

// appendFields adds fields to event, each string, error and structured value
// redacted (see Redact).
func appendFields(event *zerolog.Event, fields map[string]any) {
	for k, v := range fields {
		// Type switch to avoid double JSON serialization of strings
		switch val := v.(type) {
		case error:
			event.Str(k, Redact(val.Error()))
		case string:
			event.Str(k, Redact(val))
		case int:
			event.Int(k, val)
		case int64:
			event.Int64(k, val)
		case float64:
			event.Float64(k, val)
		case bool:
			event.Bool(k, val)
		default:
			// Structs, slices and maps are logged as JSON, redacted as such.
			if raw, err := json.Marshal(v); err == nil && redactionEnabled() {
				if redacted := Redact(string(raw)); json.Valid([]byte(redacted)) {
					event.RawJSON(k, []byte(redacted))
				} else {
					event.Str(k, redacted)
				}
				continue
			}
			event.Interface(k, v)
		}
	}
}

func Debug(message string) {
	logMessage(DEBUG, "", message, nil)
}

func DebugC(component string, message string) {
	logMessage(DEBUG, component, message, nil)
}

func Debugf(message string, ss ...any) {
	logMessage(DEBUG, "", fmt.Sprintf(message, ss...), nil)
}

func DebugF(message string, fields map[string]any) {
	logMessage(DEBUG, "", message, fields)
}

func DebugCF(component string, message string, fields map[string]any) {
	logMessage(DEBUG, component, message, fields)
}

func Info(message string) {
	logMessage(INFO, "", message, nil)
}

func InfoC(component string, message string) {
	logMessage(INFO, component, message, nil)
}

func InfoF(message string, fields map[string]any) {
	logMessage(INFO, "", message, fields)
}

func Infof(message string, ss ...any) {
	logMessage(INFO, "", fmt.Sprintf(message, ss...), nil)
}

func InfoCF(component string, message string, fields map[string]any) {
	logMessage(INFO, component, message, fields)
}

func Warn(message string) {
	logMessage(WARN, "", message, nil)
}

func WarnC(component string, message string) {
	logMessage(WARN, component, message, nil)
}

func WarnF(message string, fields map[string]any) {
	logMessage(WARN, "", message, fields)
}

func WarnCF(component string, message string, fields map[string]any) {
	logMessage(WARN, component, message, fields)
}

func Warnf(message string, ss ...any) {
	logMessage(WARN, "", fmt.Sprintf(message, ss...), nil)
}

func Error(message string) {
	logMessage(ERROR, "", message, nil)
}

func ErrorC(component string, message string) {
	logMessage(ERROR, component, message, nil)
}

func Errorf(message string, ss ...any) {
	logMessage(ERROR, "", fmt.Sprintf(message, ss...), nil)
}

func ErrorF(message string, fields map[string]any) {
	logMessage(ERROR, "", message, fields)
}

func ErrorCF(component string, message string, fields map[string]any) {
	logMessage(ERROR, component, message, fields)
}

func Fatal(message string) {
	logMessage(FATAL, "", message, nil)
}

func FatalC(component string, message string) {
	logMessage(FATAL, component, message, nil)
}

func Fatalf(message string, ss ...any) {
	logMessage(FATAL, "", fmt.Sprintf(message, ss...), nil)
}

func FatalF(message string, fields map[string]any) {
	logMessage(FATAL, "", message, fields)
}

func FatalCF(component string, message string, fields map[string]any) {
	logMessage(FATAL, component, message, fields)
}
