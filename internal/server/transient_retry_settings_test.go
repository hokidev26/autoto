package server

import "testing"

// The retry ceiling is validated on the same terms as the budgets beside it: -1
// removes the ceiling, and any other negative number is an error rather than a
// second spelling of unlimited, so a client sending -5 is told rather than having
// a limit silently removed.
func TestStrictMaxTransientRetriesAcceptsUnlimitedAndRange(t *testing.T) {
	value := -1
	got, provided, err := strictMaxTransientRetries(&value)
	if err != nil || !provided || got != -1 {
		t.Fatalf("-1 must be accepted as unlimited: got=%d provided=%v err=%v", got, provided, err)
	}

	// Zero is a real setting, not unlimited: report the failure on the first try.
	zero := 0
	got, provided, err = strictMaxTransientRetries(&zero)
	if err != nil || !provided || got != 0 {
		t.Fatalf("0 must be accepted as its own setting: got=%d provided=%v err=%v", got, provided, err)
	}

	ten := 10
	if got, provided, err = strictMaxTransientRetries(&ten); err != nil || !provided || got != 10 {
		t.Fatalf("10 must be accepted: got=%d provided=%v err=%v", got, provided, err)
	}

	top := 64
	if _, _, err = strictMaxTransientRetries(&top); err != nil {
		t.Fatalf("64 is the documented ceiling and must be accepted: %v", err)
	}
}

func TestStrictMaxTransientRetriesRejectsOutOfRange(t *testing.T) {
	for _, value := range []int{-2, -100, 65, 1000} {
		candidate := value
		if _, _, err := strictMaxTransientRetries(&candidate); err == nil {
			t.Fatalf("%d must be rejected rather than reinterpreted", value)
		}
	}
}

// An older client that does not know the field must not blank it. Absent is not
// zero here, because zero would mean "never retry".
func TestStrictMaxTransientRetriesTreatsAbsentAsUnchanged(t *testing.T) {
	got, provided, err := strictMaxTransientRetries(nil)
	if err != nil {
		t.Fatalf("an omitted field is not an error: %v", err)
	}
	if provided {
		t.Fatal("an omitted field must not be reported as provided")
	}
	if got != 0 {
		t.Fatalf("the ignored value should be the zero value, got %d", got)
	}
}
