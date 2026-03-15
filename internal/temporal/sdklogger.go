package temporal

import "log/slog"

// SDKLogger adapts slog to the Temporal SDK Logger interface.
type SDKLogger struct {
	log *slog.Logger
}

// NewSDKLogger creates a Temporal SDK logger backed by slog.
func NewSDKLogger(log *slog.Logger) *SDKLogger {
	if log == nil {
		log = slog.Default()
	}
	return &SDKLogger{log: log}
}

func (l *SDKLogger) Debug(msg string, keyvals ...any) { l.log.Debug(msg, keyvals...) }
func (l *SDKLogger) Info(msg string, keyvals ...any)  { l.log.Info(msg, keyvals...) }
func (l *SDKLogger) Warn(msg string, keyvals ...any)  { l.log.Warn(msg, keyvals...) }
func (l *SDKLogger) Error(msg string, keyvals ...any) { l.log.Error(msg, keyvals...) }

