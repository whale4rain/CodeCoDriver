package server

import (
	"errors"
	"os"
	"strconv"
	"time"
)

var errRateLimitExceeded = errors.New("request rate limit exceeded")

func rateLimitFromEnv() int {
	value, err := strconv.Atoi(os.Getenv("CODECODRIVER_RATE_LIMIT"))
	if err != nil || value < 0 {
		return 60
	}
	return value
}

func HTTPTimeoutFromEnv(name string, fallback time.Duration) time.Duration {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Second
}
