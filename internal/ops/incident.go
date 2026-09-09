package ops

import "time"

// IncidentHistory survives recovery so brief flapping cannot renew the budget.
type IncidentHistory struct {
	ActiveSince    *time.Time `json:"active_since,omitempty"`
	NotifiedActive *bool      `json:"notified_active,omitempty"`
	LastFiringAt   *time.Time `json:"last_firing_at,omitempty"`
	NormalSince    *time.Time `json:"normal_since,omitempty"`
	RetryAfter     *time.Time `json:"retry_after,omitempty"`
	FiringStreak   int        `json:"firing_streak"`
	RecoveryStreak int        `json:"recovery_streak"`
}

type IncidentState struct {
	Active            bool            `json:"active"`
	Value             float64         `json:"value"`
	Message           string          `json:"message"`
	LastObservedAt    time.Time       `json:"last_observed_at"`
	LastNotifiedAt    *time.Time      `json:"last_notified_at,omitempty"`
	NotificationCount int             `json:"notification_count"`
	Incident          IncidentHistory `json:"incident"`
}

type IncidentPolicy struct {
	FiringConfirmations   int
	RecoveryConfirmations int
	Cooldown              time.Duration
	StableFor             time.Duration
	MaxGap                time.Duration
	MaxNotifications      int
	Repeat                bool
}

// Observe changes the confirmed state and returns a notification to enqueue.
// Hold represents the hysteresis band; Ignore represents missing evidence.
func (s *IncidentState) Observe(now time.Time, active, ignore, hold bool, p IncidentPolicy) string {
	h := &s.Incident
	if h.NotifiedActive == nil {
		previous := s.Active
		h.NotifiedActive = &previous
		if s.Active {
			h.LastFiringAt = s.LastNotifiedAt
		}
	}
	if p.MaxGap > 0 && !s.LastObservedAt.IsZero() && now.Sub(s.LastObservedAt) > p.MaxGap {
		h.FiringStreak, h.RecoveryStreak, h.NormalSince = 0, 0, nil
	}
	s.LastObservedAt = now
	if ignore || hold {
		h.FiringStreak, h.RecoveryStreak, h.NormalSince = 0, 0, nil
		return ""
	}
	if active {
		h.RecoveryStreak, h.NormalSince = 0, nil
		if h.FiringStreak < p.FiringConfirmations {
			h.FiringStreak++
		}
		if h.FiringStreak >= p.FiringConfirmations {
			if !s.Active {
				at := now
				h.ActiveSince = &at
			}
			s.Active = true
		}
	} else {
		h.FiringStreak = 0
		if h.NormalSince == nil {
			h.NormalSince = &now
		}
		if h.RecoveryStreak < p.RecoveryConfirmations {
			h.RecoveryStreak++
		}
		if h.RecoveryStreak >= p.RecoveryConfirmations {
			s.Active = false
		}
		if !s.Active && now.Sub(*h.NormalSince) >= p.StableFor {
			s.NotificationCount = 0
			if p.StableFor == 0 {
				h.LastFiringAt = nil
			}
		}
	}
	if h.RetryAfter != nil && now.Before(*h.RetryAfter) {
		return ""
	}
	if !s.Active && !active && *h.NotifiedActive {
		return "resolved"
	}
	if !s.Active || !active || (p.MaxNotifications > 0 && s.NotificationCount >= p.MaxNotifications) {
		return ""
	}
	if h.LastFiringAt != nil && (now.Sub(*h.LastFiringAt) < p.Cooldown || (*h.NotifiedActive && !p.Repeat)) {
		return ""
	}
	return "firing"
}

// Accepted means durable enqueue, not successful external delivery.
func (s *IncidentState) Accepted(now time.Time, event string) {
	active := event == "firing"
	s.Incident.NotifiedActive = &active
	s.Incident.RetryAfter = nil
	s.LastNotifiedAt = &now
	if active {
		s.Incident.LastFiringAt = &now
		s.NotificationCount++
	}
}

func (s *IncidentState) EnqueueFailed(now time.Time, cooldown time.Duration) {
	if cooldown < time.Minute {
		cooldown = time.Minute
	}
	retry := now.Add(cooldown)
	s.Incident.RetryAfter = &retry
}
