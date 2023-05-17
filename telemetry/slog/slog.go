/*
Package slog provides a slog.Handler that will log to an OTEL span if one is active.

This allows logging events you record with the log or slog package to be linked into the OTEL
trace if one is active. If you do not use this package, OTEL events from this package will still be
recorded to the active span, however any log data you record will not be linked to the OTEL trace.

You can use this package as a wrapper around the logger you use in your application.

The best practice for using this package is to do the setup at package main.

Example usage with custom logger (package main):

	func main() {
		customLogger := yourLogPackage.New()
		slog.SetDefault(events.NewOTEL(customLogger))
	}

The above will always use your custom logger whenever logging is done. If there is an active
OTEL span, it will log to that span too along with our trace messages.

Example usage with default logger (package main):

	func main() {
		slog.SetDefault(events.NewOTEL(nil))
	}

The above will always use the default logger whenever logging is done. If there is an active
OTEL span, it will log to that span too along with our trace messages.
*/
package slog

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/exp/slog"
)

// LevelTrace is the level at which we record events to the span.  This correlates with
// the slog documentation recommendation.
const LevelTrace = slog.Level(-8)

// otelHandler implements slog.Handler
// It adds;
// (a) TraceIds & spanIds to logs.
// (b) Logs(as events) to the active span.
// This code is borrowed from: https://github.com/komuw/otero/blob/v0.0.1/log/slog.go
// Updated to work with new versions of slog and other minor changes.
type otel struct{ h slog.Handler }

// NewOTEL provides a new slog.Handler that can log to an active span.
// If there is no active span, it logs to the logger handler provided.
// If h == nil, it will use slog.Default() as the logger handler. If that
// handler is a of the OTEL type, it will use that Handler's wrapped handler.
// The underlying handler is always logged to.
func NewOTEL(h slog.Handler) slog.Handler {
	if h == nil {
		d := slog.Default()
		if v, ok := d.Handler().(otel); ok {
			return otel{v.h}
		}
		return otel{slog.Default().Handler()}
	}
	return otel{h}
}

func (s otel) Enabled(_ context.Context, _ slog.Level) bool {
	return true /* support all logging levels*/
}

func (s otel) WithAttrs(attrs []slog.Attr) slog.Handler {
	return otel{h: s.h.WithAttrs(attrs)}
}

func (s otel) WithGroup(name string) slog.Handler {
	return otel{h: s.h.WithGroup(name)}
}

func (s otel) Handle(ctx context.Context, r slog.Record) (err error) {
	if ctx == nil {
		return s.h.Handle(ctx, r)
	}
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return s.h.Handle(ctx, r)
	}

	// (a) adds TraceIds & spanIds to logs.
	sCtx := span.SpanContext()
	attrs := []slog.Attr{}
	if sCtx.HasTraceID() {
		attrs = append(
			attrs,
			slog.Attr{Key: "traceId", Value: slog.StringValue(sCtx.TraceID().String())},
		)
	}
	if sCtx.HasSpanID() {
		attrs = append(
			attrs,
			slog.Attr{Key: "spanId", Value: slog.StringValue(sCtx.SpanID().String())},
		)
	}
	if len(attrs) > 0 {
		r.AddAttrs(attrs...)
	}

	{
		// (b) adds logs to the active span as events.

		// code from: https://github.com/uptrace/opentelemetry-go-extra/tree/main/otellogrus
		// which is BSD 2-Clause license.

		attrs := make([]attribute.KeyValue, 0)

		logSeverityKey := attribute.Key("log.severity")
		logMessageKey := attribute.Key("log.message")
		attrs = append(attrs, logSeverityKey.String(r.Level.String()))
		attrs = append(attrs, logMessageKey.String(r.Message))

		// TODO: Obey the following rules from the slog documentation:
		//
		// Handle methods that produce output should observe the following rules:
		//   - If r.Time is the zero time, ignore the time.
		//   - If an Attr's key is the empty string, ignore the Attr.
		//
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "" {
				return true
			}

			attrs = append(attrs,
				attribute.KeyValue{
					Key:   attribute.Key(a.Key),
					Value: attribute.StringValue(a.Value.String()),
				},
			)
			return true
		})

		span.AddEvent("log", trace.WithAttributes(attrs...))
		if r.Level >= slog.LevelError {
			span.SetStatus(codes.Error, r.Message)
		}
	}

	return s.h.Handle(ctx, r)
}
