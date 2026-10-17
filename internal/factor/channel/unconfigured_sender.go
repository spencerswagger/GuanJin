package channel

import (
	"context"
	"errors"
)

var ErrUpstreamUnconfigured = errors.New("UPSTREAM_UNAVAILABLE")

type UnconfiguredSender struct {
	channel string
}

func NewUnconfiguredSender(channelName string) *UnconfiguredSender {
	return &UnconfiguredSender{channel: channelName}
}

func (s *UnconfiguredSender) Channel() string { return s.channel }

func (s *UnconfiguredSender) Send(context.Context, string, string) error {
	return ErrUpstreamUnconfigured
}
