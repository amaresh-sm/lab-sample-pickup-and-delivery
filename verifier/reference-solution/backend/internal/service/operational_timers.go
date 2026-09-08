package service

import (
	"os"
	"strconv"
	"time"
)

// Bounded operational timers. Each default is a named constant and each may be
// overridden by an environment variable inside a sane range, so an operator can
// tune the deployment without changing behaviour that the rules depend on.
const (
	defaultStaleLocationThresholdSeconds  = 300
	defaultDeclinePenaltyWindowSeconds    = 3600
	defaultUnassignedSearchTimeoutSeconds = 3600
)

func staleLocationThreshold() time.Duration {
	seconds := defaultStaleLocationThresholdSeconds
	if value, err := strconv.Atoi(os.Getenv("STALE_LOCATION_THRESHOLD_SECONDS")); err == nil &&
		value >= 5 && value <= 86400 {
		seconds = value
	}
	return time.Duration(seconds) * time.Second
}

func isLocationStale(recordedAt, now time.Time) bool {
	return now.After(recordedAt.Add(staleLocationThreshold()))
}

// declinePenaltyWindow bounds how long a decline may lower a worker's priority
// for future offers. A decline older than the window carries no penalty at all,
// so repeated declines can never permanently exclude a worker from future work.
func declinePenaltyWindow() time.Duration {
	seconds := defaultDeclinePenaltyWindowSeconds
	if value, err := strconv.Atoi(os.Getenv("DECLINE_PENALTY_WINDOW_SECONDS")); err == nil &&
		value >= 60 && value <= 604800 {
		seconds = value
	}
	return time.Duration(seconds) * time.Second
}

// declinePenaltyActive reports whether a decline is recent enough to still count
// against the worker who made it.
func declinePenaltyActive(declinedAt, now time.Time) bool {
	return !declinedAt.Before(now.Add(-declinePenaltyWindow()))
}

// unassignedSearchTimeout bounds how long an assignment that requires a
// qualified worker (currently only a collection assignment's qualified-
// collector requirement) may sit with nobody ever eligible before the search
// is given up on and the sample is flagged as an exception for that specific
// reason. This is deliberately separate from assignmentOfferTTL: the offer
// TTL bounds how long ONE offered worker has to respond, while this bounds
// how long the assignment may go without ever having anyone to offer it to.
func unassignedSearchTimeout() time.Duration {
	seconds := defaultUnassignedSearchTimeoutSeconds
	if value, err := strconv.Atoi(os.Getenv("UNASSIGNED_SEARCH_TIMEOUT_SECONDS")); err == nil &&
		value >= 60 && value <= 604800 {
		seconds = value
	}
	return time.Duration(seconds) * time.Second
}
