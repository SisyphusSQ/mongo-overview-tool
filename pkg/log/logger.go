package log

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/SisyphusSQ/mongo-overview-tool/utils/timeutil"
)

var Logger *ZapLogger

func init() {
	// 初始化默认 Info 级别 Logger，确保任何时候 Logger 都不为 nil
	New(false)
}

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

func (l *ZapLogger) Debugf(format string, args ...any) {
	l.logger.Debugf(format, args...)
}

func (l *ZapLogger) Infof(format string, args ...any) {
	l.logger.Infof(format, args...)
}

func (l *ZapLogger) Warnf(format string, args ...any) {
	l.logger.Warnf(format, args...)
}

func (l *ZapLogger) Errorf(format string, args ...any) {
	l.logger.Errorf(format, args...)
}

func (l *ZapLogger) Sync() {
	_ = l.logger.Sync()
}
