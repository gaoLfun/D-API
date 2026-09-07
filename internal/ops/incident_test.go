package ops

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIncidentConfirmationFlappingAndRestart(t *testing.T) {
	policy := IncidentPolicy{FiringConfirmations: 2, RecoveryConfirmations: 3,
		Cooldown: 30 * time.Minute, StableFor: 30 * time.Minute, MaxGap: 90 * time.Second,
		MaxNotifications: 3, Repeat: true}
	start := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	var state IncidentState
	step := func(minute int, active, ignore, hold bool, want string) {
		t.Helper()
		now := start.Add(time.Duration(minute) * time.Minute)
		got := state.Observe(now, active, ignore, hold, policy)
		if got != want {
			t.Fatalf("minute %d: notification=%q want=%q state=%+v", minute, got, want, state)
		}
		if got != "" {
			state.Accepted(now, got)
		}
		// Round-trip every observation to model a new process using persisted state.
		payload, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		state = IncidentState{}
		if err := json.Unmarshal(payload, &state); err != nil {
			t.Fatal(err)
		}
	}
	step(0, true, false, false, "")
	step(1, true, false, false, "firing")
	step(2, false, false, false, "")
	step(3, false, true, false, "")
	step(4, false, false, false, "")
	step(5, false, false, false, "")
	step(6, false, false, true, "")
	step(7, false, false, false, "")
	step(8, false, false, false, "")
	step(9, false, false, false, "resolved")
	step(10, true, false, false, "")
	step(11, true, false, false, "")
	for minute := 12; minute < 31; minute++ {
		step(minute, true, false, false, "")
	}
	step(31, true, false, false, "firing")
	for minute := 32; minute < 61; minute++ {
		step(minute, true, false, false, "")
	}
	step(61, true, false, false, "firing")
	for minute := 62; minute <= 92; minute++ {
		step(minute, true, false, false, "")
	}
	step(93, false, false, false, "")
	step(94, false, false, false, "")
	step(95, false, false, false, "resolved")
	if state.NotificationCount != 3 {
		t.Fatal("short recovery reset the budget")
	}
	step(96, true, false, false, "")
	step(97, true, false, false, "")
	for minute := 98; minute <= 128; minute++ {
		step(minute, false, false, false, "")
	}
	if state.NotificationCount != 0 {
		t.Fatal("stable recovery did not reset the budget")
	}
	step(129, true, false, false, "")
	step(130, true, false, false, "firing")
}

func TestIncidentObservationGapBreaksConfirmationAndStability(t *testing.T) {
	p := IncidentPolicy{FiringConfirmations: 2, RecoveryConfirmations: 3, MaxGap: 90 * time.Second, StableFor: 30 * time.Minute}
	now := time.Now()
	var s IncidentState
	s.Observe(now, true, false, false, p)
	if got := s.Observe(now.Add(3*time.Minute), true, false, false, p); got != "" || s.Active {
		t.Fatal("observation gap confirmed an incident")
	}
	s.NotificationCount = 3
	s.Observe(now.Add(4*time.Minute), false, false, false, p)
	s.Observe(now.Add(time.Hour), false, false, false, p)
	if s.NotificationCount != 3 {
		t.Fatal("downtime counted as stable recovery")
	}
}

func TestFailedRecoveryDoesNotNotifyRecoveryDuringRelapse(t *testing.T) {
	p := IncidentPolicy{FiringConfirmations: 2, RecoveryConfirmations: 3, StableFor: 30 * time.Minute, Cooldown: 30 * time.Minute, Repeat: true}
	now := time.Now()
	s := IncidentState{Active: true, NotificationCount: 1, LastNotifiedAt: &now}
	for i := 0; i < 3; i++ {
		s.Observe(now.Add(time.Duration(i)*time.Minute), false, false, false, p)
	}
	s.EnqueueFailed(now.Add(2*time.Minute), time.Minute)
	if got := s.Observe(now.Add(3*time.Minute), true, false, false, p); got != "" {
		t.Fatalf("relapse emitted stale %q", got)
	}
}

func TestHealthIncidentMergesRecurrenceWithoutRepeatingContinuousFailure(t *testing.T) {
	p := IncidentPolicy{FiringConfirmations: 1, RecoveryConfirmations: 1,
		Cooldown: 30 * time.Minute, StableFor: 30 * time.Minute, MaxNotifications: 3}
	now := time.Now()
	var s IncidentState
	if got := s.Observe(now, true, false, false, p); got != "firing" {
		t.Fatal(got)
	}
	s.Accepted(now, "firing")
	if got := s.Observe(now.Add(time.Hour), true, false, false, p); got != "" {
		t.Fatal("continuous health failure repeated")
	}
	if got := s.Observe(now.Add(61*time.Minute), false, false, false, p); got != "resolved" {
		t.Fatal(got)
	}
	s.Accepted(now.Add(61*time.Minute), "resolved")
	if got := s.Observe(now.Add(62*time.Minute), true, false, false, p); got != "firing" {
		t.Fatal(got)
	}
	s.Accepted(now.Add(62*time.Minute), "firing")
	if got := s.Observe(now.Add(63*time.Minute), false, false, false, p); got != "resolved" {
		t.Fatal(got)
	}
	s.Accepted(now.Add(63*time.Minute), "resolved")
	if got := s.Observe(now.Add(64*time.Minute), true, false, false, p); got != "" {
		t.Fatal("recurrence bypassed cooldown")
	}
	if got := s.Observe(now.Add(65*time.Minute), false, false, false, p); got != "" {
		t.Fatal("orphan recovery notification")
	}
}
