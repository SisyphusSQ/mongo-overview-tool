package log

import (
	"bytes"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/SisyphusSQ/mongo-overview-tool/utils/timeutil"
)

var (
	Logger *ZapLogger
)

func New(isDebug bool) {
	loglevel := zapcore.InfoLevel
	if isDebug {
		loglevel = zapcore.DebugLevel
	}

	// -------- new print logger --------
	priCfg := zap.Config{
		Level:       zap.NewAtomicLevelAt(loglevel),
		Development: true,
		Encoding:    "console", // 使用 console 编码器输出文本日志
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        "ts",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			MessageKey:     "msg",
			FunctionKey:    zapcore.OmitKey,
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    customLevelEncoder,
			EncodeTime:     zapcore.TimeEncoderOfLayout(timeutil.CSTLayout),
			EncodeDuration: zapcore.SecondsDurationEncoder,
			EncodeCaller:   customCallerEncoder,
		},
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	pri, err := priCfg.Build()
	if err != nil {
		panic(err)
	}
	Logger = NewZapLogger(pri.Sugar())
}

func customLevelEncoder(level zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	levelString := "[" + level.CapitalString() + "]"
	enc.AppendString(levelString)
}

func customCallerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	if caller.Defined {
		enc.AppendString("[" + caller.TrimmedPath() + "]")
	} else {
		enc.AppendString("[undefined]")
	}
}

type ZapLogger struct {
	logger *zap.SugaredLogger
}

func NewZapLogger(logger *zap.SugaredLogger) *ZapLogger {
	return &ZapLogger{logger: logger}
}

// Printf formats according to a format specifier and writes to the logger.
func (l *ZapLogger) Printf(format string, v ...interface{}) {
	l.logger.Infof(format, v...)
}

// Print calls Printf with the default message format.
func (l *ZapLogger) Print(v ...interface{}) {
	l.logger.Info(v...)
}

// Println calls Print with a newline.
func (l *ZapLogger) Println(v ...interface{}) {
	l.logger.Info(v...)
}

// Fatal calls Print followed by a call to os.Exit(1).
func (l *ZapLogger) Fatal(v ...interface{}) {
	l.logger.Fatal(v...)
}

// Fatalf is equivalent to Printf followed by a call to os.Exit(1).
func (l *ZapLogger) Fatalf(format string, v ...interface{}) {
	l.logger.Fatalf(format, v...)
}

// Fatalln is equivalent to Fatal.
func (l *ZapLogger) Fatalln(v ...interface{}) {
	l.logger.Fatal(v...)
}

// Panic is equivalent to Print followed by a call to panic().
func (l *ZapLogger) Panic(v ...interface{}) {
	l.logger.Panic(v...)
}

// Panicf is equivalent to Printf followed by a call to panic().
func (l *ZapLogger) Panicf(format string, v ...interface{}) {
	l.logger.Panicf(format, v...)
}

func (l *ZapLogger) Debugf(format string, args ...interface{}) {
	l.logger.Debugf(format, args...)
}

func (l *ZapLogger) Infof(format string, args ...interface{}) {
	l.logger.Infof(format, args...)
}

func (l *ZapLogger) Warnf(format string, args ...interface{}) {
	l.logger.Warnf(format, args...)
}

func (l *ZapLogger) Errorf(format string, args ...interface{}) {
	l.logger.Errorf(format, args...)
}

func (l *ZapLogger) Debug(format string, args ...interface{}) {
	l.logger.Debugf(format, args...)
}

func (l *ZapLogger) Info(format string, args ...interface{}) {
	l.logger.Infof(format, args...)
}

func (l *ZapLogger) Warn(format string, args ...interface{}) {
	l.logger.Warnf(format, args...)
}

func (l *ZapLogger) Error(format string, args ...interface{}) {
	l.logger.Errorf(format, args...)
}

func (l *ZapLogger) Sync() {
	_ = l.logger.Sync()
}

type Writer struct {
	logger *zap.SugaredLogger
}

func (w *Writer) Write(p []byte) (n int, err error) {
	// 按行分割输入的字节切片
	lines := bytes.Split(p, []byte("\n"))
	for _, line := range lines {
		if len(line) > 0 {
			// 记录日志
			w.logger.Info(string(line))
		}
	}
	return len(p), nil
}

func (w *Writer) Infof(format string, args ...interface{}) {
	w.logger.Infof(format, args...)
}
