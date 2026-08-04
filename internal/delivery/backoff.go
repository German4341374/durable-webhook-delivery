package delivery

import (
	"crypto/rand"
	"math/big"
	"time"
)

const maxBackoff = time.Hour

func Backoff(attempt int, base time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for range attempt - 1 {
		if delay >= maxBackoff/2 {
			delay = maxBackoff
			break
		}
		delay *= 2
	}
	upper := new(big.Int).SetInt64(int64(delay) + 1)
	jitter, err := rand.Int(rand.Reader, upper)
	if err != nil {
		return delay
	}
	return time.Duration(jitter.Int64())
}
