package observability

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseTraceparentValid(t *testing.T) {
	header := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", ParseTraceparent(header))
}

func TestParseTraceparentInvalid(t *testing.T) {
	cases := map[string]string{
		"missing fields":  "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
		"short trace id":  "00-4bf92f3577b34da6a3ce929d0e0e47-00f067aa0ba902b7-01",
		"all zero trace":  "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"all zero span":   "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"forbidden ver":   "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"uppercase hex":   "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		"non hex chars":   "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-00f067aa0ba902b7-01",
		"empty":           "",
		"extra separator": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-x",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, "", ParseTraceparent(header))
		})
	}
}

func TestResolveTraceIDUsesTraceparent(t *testing.T) {
	header := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", ResolveTraceID(header))
}

func TestResolveTraceIDFallbackIsUUIDv7(t *testing.T) {
	tid := ResolveTraceID("")
	require.NotEmpty(t, tid)

	id, err := uuid.Parse(tid)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), id.Version())

	require.NotEqual(t, tid, ResolveTraceID(""))
}

func TestNewTraceIDIsUUIDv7(t *testing.T) {
	id, err := uuid.Parse(NewTraceID())
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), id.Version())
}

func TestWithTraceIDFromContext(t *testing.T) {
	require.Equal(t, "", FromContext(context.Background()))

	var nilCtx context.Context
	require.Equal(t, "", FromContext(nilCtx))

	ctx := WithTraceID(context.Background(), "abc")
	require.Equal(t, "abc", FromContext(ctx))
}

func TestTraceparentRoundTrip(t *testing.T) {
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	tp := Traceparent(traceID)
	require.NotEmpty(t, tp)
	require.Equal(t, traceID, ParseTraceparent(tp))
}

func TestTraceparentInvalidInput(t *testing.T) {
	require.Equal(t, "", Traceparent("short"))
}
