package domain

import "time"

type Channel struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	TargetURL          string    `json:"target_url"`
	TimeoutMS          int       `json:"timeout_ms"`
	MaxAttempts        int       `json:"max_attempts"`
	RateLimitPerSecond int       `json:"rate_limit_per_second"`
	MaxConcurrency     int       `json:"max_concurrency"`
	CreatedAt          time.Time `json:"created_at"`
	SecretCiphertext   []byte    `json:"-"`
	SecretNonce        []byte    `json:"-"`
}

type Delivery struct {
	ID             string     `json:"id"`
	EventID        string     `json:"event_id"`
	ChannelID      string     `json:"channel_id"`
	ChannelName    string     `json:"channel_name"`
	TargetURL      string     `json:"target_url"`
	Status         string     `json:"status"`
	AttemptCount   int        `json:"attempt_count"`
	CycleAttempts  int        `json:"cycle_attempt_count"`
	MaxAttempts    int        `json:"max_attempts"`
	NextAttemptAt  time.Time  `json:"next_attempt_at"`
	ResponseStatus *int       `json:"response_status,omitempty"`
	LastError      *string    `json:"last_error,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	ReplayCount    int        `json:"replay_count"`
	PayloadHash    string     `json:"payload_hash"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type ClaimedDelivery struct {
	Delivery
	Payload          []byte
	ContentType      string
	SecretCiphertext []byte
	SecretNonce      []byte
	Timeout          time.Duration
	RateLimit        int
	Concurrency      int
}

type AttemptResult struct {
	ResponseStatus *int
	Error          string
	Duration       time.Duration
	RetryAt        time.Time
	DeadLetter     bool
}

type DeliveryAttempt struct {
	AttemptNumber int        `json:"attempt_number"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	DurationMS    *int64     `json:"duration_ms,omitempty"`
	Response      *int       `json:"response_status,omitempty"`
	Error         *string    `json:"error,omitempty"`
}
