package service

import (
	"testing"
	"time"
)

// AC-33: the decline penalty window is bounded and configurable in exactly the
// same way as the stale-location threshold beside it.
func TestDeclinePenaltyWindowIsBoundedAndConfigurable(t *testing.T) {
	if got := declinePenaltyWindow(); got != time.Duration(defaultDeclinePenaltyWindowSeconds)*time.Second {
		t.Fatalf("default decline penalty window = %s", got)
	}
	t.Setenv("DECLINE_PENALTY_WINDOW_SECONDS", "120")
	if got := declinePenaltyWindow(); got != 2*time.Minute {
		t.Fatalf("configured decline penalty window = %s, want 2m", got)
	}
	// Out-of-range values fall back to the default rather than disabling decay.
	for _, value := range []string{"1", "9999999", "not-a-number"} {
		t.Setenv("DECLINE_PENALTY_WINDOW_SECONDS", value)
		if got := declinePenaltyWindow(); got != time.Duration(defaultDeclinePenaltyWindowSeconds)*time.Second {
			t.Fatalf("window for %q = %s, want the default", value, got)
		}
	}

	t.Setenv("DECLINE_PENALTY_WINDOW_SECONDS", "600")
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	if !declinePenaltyActive(now.Add(-9*time.Minute), now) {
		t.Fatal("a decline inside the window carried no penalty")
	}
	if declinePenaltyActive(now.Add(-11*time.Minute), now) {
		t.Fatal("a decline outside the window still carried a penalty")
	}
}

func TestStaleLocationThresholdIsConfigurable(t *testing.T) {
	t.Setenv("STALE_LOCATION_THRESHOLD_SECONDS", "60")
	recordedAt := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)

	if isLocationStale(recordedAt, recordedAt.Add(60*time.Second)) {
		t.Fatal("location became stale at the inclusive threshold")
	}
	if !isLocationStale(recordedAt, recordedAt.Add(61*time.Second)) {
		t.Fatal("location remained current after the configured threshold")
	}
}
