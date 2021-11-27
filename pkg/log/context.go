package log

import (
	"context"

	"github.com/sirupsen/logrus"
)

var (
	// G is an alias for GetLogger.
	//
	// We may want to define this locally to a package to get package tagged log
	// messages.
	G = GetLogger

	// L is an alias for the standard logger.
	L = logrus.NewEntry(logrus.StandardLogger())
)

type (
	loggerKey struct{}
)

const (
	// RFC3339NanoFixed is time.RFC3339Nano with nanoseconds padded using zeros to
	// ensure the formatted time is always the same number of characters.
	RFC3339NanoFixed = "2006-01-02T15:04:05.000000000Z07:00"
)

// WithLogger returns a new context with the provided logger. Use in
// combination with logger.WithField(s) for great effect.
func WithLogger(ctx context.Context, logger *logrus.Entry) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// GetLogger retrieves the current logger from the context. If no logger is
// available, the default logger is returned.
func GetLogger(ctx context.Context) *logrus.Entry {
	logger := ctx.Value(loggerKey{})

	if logger == nil {
		return L
	}

	return logger.(*logrus.Entry)
}
