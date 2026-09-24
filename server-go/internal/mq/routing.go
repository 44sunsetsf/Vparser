package mq

import "time"

// MaxAttempts is the total number of processing attempts of a retryable task.
const MaxAttempts = 3

// Dead-letter reasons (kafka_dlq_total{reason}).
const (
	ReasonPermanent = "permanent" // redelivery would fail the same way
	ReasonExhausted = "exhausted" // retry budget used up
	ReasonPoison    = "poison"    // undecodable or incomplete message
)

// Route is where a failed attempt goes next.
type Route struct {
	Topic  string
	Delay  time.Duration // only for retry topics
	Reason string        // only for the DLQ
}

// RetryDelays maps each retry topic to its delay. Two fixed tiers (rather than one topic with
// per-record delays) keep every retry topic FIFO by due time, so pausing a partition until its head
// record is due never delays a record that is due earlier.
var RetryDelays = map[string]time.Duration{
	TopicRetry10s: 10 * time.Second,
	TopicRetry60s: 60 * time.Second,
}

// RouteFailure decides the next hop of a failed attempt (budget exhaustion is handled before this:
// it is a terminal outcome, not a failure to retry).
func RouteFailure(permanent bool, attempt int) Route {
	switch {
	case permanent:
		return Route{Topic: TopicDLQ, Reason: ReasonPermanent}
	case attempt <= 1:
		return Route{Topic: TopicRetry10s, Delay: RetryDelays[TopicRetry10s]}
	case attempt < MaxAttempts:
		return Route{Topic: TopicRetry60s, Delay: RetryDelays[TopicRetry60s]}
	default:
		return Route{Topic: TopicDLQ, Reason: ReasonExhausted}
	}
}
