package channel

import (
	"context"
	"strings"

	"go.uber.org/zap"
)

type LoggingSender struct {
	log     *zap.Logger
	channel string
}

func NewLoggingSender(logger *zap.Logger, channelName string) *LoggingSender {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LoggingSender{log: logger, channel: channelName}
}

func (s *LoggingSender) Send(_ context.Context, target, _ string) error {
	s.log.Info("code dispatched",
		zap.String("channel", s.channel),
		zap.String("target_masked", maskTarget(target)),
	)
	return nil
}

func maskTarget(target string) string {
	if strings.ContainsRune(target, '@') {
		at := strings.IndexByte(target, '@')
		local := target[:at]
		if local == "" {
			return "***" + target[at:]
		}
		return string(local[0]) + "***" + target[at:]
	}
	digits := make([]byte, 0, len(target))
	for i := 0; i < len(target); i++ {
		if target[i] >= '0' && target[i] <= '9' {
			digits = append(digits, target[i])
		}
	}
	if len(digits) > 11 {
		digits = digits[len(digits)-11:]
	}
	if len(digits) < 7 {
		return "***"
	}
	return string(digits[:3]) + "****" + string(digits[len(digits)-4:])
}