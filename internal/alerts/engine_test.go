package alerts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gaoLfun/dapi/internal/ops"
)

type testRepository struct {
	rules        []Rule
	observations map[int64][]Observation
	observeErr   map[int64]error
	states       map[string]State
}

func (r *testRepository) ListRules(context.Context) ([]Rule, error) { return r.rules, nil }

func (r *testRepository) Observe(_ context.Context, rule Rule) ([]Observation, error) {
	return r.observations[rule.ID], r.observeErr[rule.ID]
}

func (r *testRepository) LoadState(_ context.Context, ruleID int64, key string) (State, bool, error) {
	state, ok := r.states[stateKey(ruleID, key)]
	return state, ok, nil
}

func (r *testRepository) SaveState(_ context.Context, ruleID int64, key string, state State) error {
	if r.states == nil {
		r.states = make(map[string]State)
	}
	r.states[stateKey(ruleID, key)] = state
	return nil
}

func (r *testRepository) PruneStates(_ context.Context, ruleID int64, keys []string) error {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[stateKey(ruleID, key)] = true
	}
	prefix := string(rune(ruleID)) + ":"
	for key := range r.states {
		if strings.HasPrefix(key, prefix) && !allowed[key] {
			delete(r.states, key)
		}
	}
	return nil
}

func stateKey(ruleID int64, key string) string { return string(rune(ruleID)) + ":" + key }

func newTestEngine(repository *testRepository, now time.Time, events *[]ops.Event) *Engine {
	engine := NewEngine(repository, ops.NotifierFunc(func(_ context.Context, event ops.Event) error {
		*events = append(*events, event)
		return nil
	}))
	engine.now = func() time.Time { return now }
	return engine
}

func TestEngineTriggersFirstActiveObservation(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repository := &testRepository{
		rules:        []Rule{{ID: 1, Event: EventLowBalance, Enabled: true, Cooldown: time.Hour}},
		observations: map[int64][]Observation{1: {{Key: "upstream:7", Active: true, Value: 2, Message: "balance is low", UpstreamID: 7, UpstreamName: "primary"}}},
		states:       make(map[string]State),
	}
	var events []ops.Event
	if err := newTestEngine(repository, now, &events).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := repository.states[stateKey(1, "upstream:7")]
	if len(events) != 1 || events[0].State != "firing" || !state.Active || state.LastNotifiedAt == nil || !state.LastNotifiedAt.Equal(now) {
		t.Fatalf("events=%#v state=%#v", events, state)
	}
}

func TestEngineDeduplicatesWithinCooldown(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	last := now.Add(-30 * time.Minute)
	repository := &testRepository{
		rules:        []Rule{{ID: 1, Event: EventErrorRate, Enabled: true, Cooldown: time.Hour}},
		observations: map[int64][]Observation{1: {{Key: "upstream:7", Active: true}}},
		states:       map[string]State{stateKey(1, "upstream:7"): {Active: true, LastNotifiedAt: &last}},
	}
	var events []ops.Event
	if err := newTestEngine(repository, now, &events).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events=%#v", events)
	}
}

func TestEngineRemindsAfterCooldown(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	last := now.Add(-2 * time.Hour)
	repository := &testRepository{
		rules:        []Rule{{ID: 1, Event: EventLatency, Enabled: true, Cooldown: time.Hour}},
		observations: map[int64][]Observation{1: {{Key: "upstream:7", Active: true}}},
		states:       map[string]State{stateKey(1, "upstream:7"): {Active: true, LastNotifiedAt: &last}},
	}
	var events []ops.Event
	if err := newTestEngine(repository, now, &events).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].State != "firing" {
		t.Fatalf("events=%#v", events)
	}
}

func TestEngineStopsRemindersAtMaximum(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repository := &testRepository{
		rules:        []Rule{{ID: 1, Event: EventLatency, Enabled: true, Cooldown: time.Hour, MaxNotifications: 2}},
		observations: map[int64][]Observation{1: {{Key: "upstream:7", Active: true}}},
		states:       make(map[string]State),
	}
	var events []ops.Event
	engine := newTestEngine(repository, now, &events)
	for _, advance := range []time.Duration{0, 2 * time.Hour, 4 * time.Hour} {
		engine.now = func() time.Time { return now.Add(advance) }
		if err := engine.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 2 || repository.states[stateKey(1, "upstream:7")].NotificationCount != 2 {
		t.Fatalf("events=%d state=%#v", len(events), repository.states[stateKey(1, "upstream:7")])
	}
	repository.observations[1][0].Active = false
	engine.now = func() time.Time { return now.Add(5 * time.Hour) }
	if err := engine.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || repository.states[stateKey(1, "upstream:7")].NotificationCount != 0 {
		t.Fatalf("recovery events=%d state=%#v", len(events), repository.states[stateKey(1, "upstream:7")])
	}
}

func TestEngineNotifiesRecovery(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	repository := &testRepository{
		rules:        []Rule{{ID: 1, Event: EventBalanceUnavailable, Enabled: true, Cooldown: time.Hour}},
		observations: map[int64][]Observation{1: {{Key: "upstream:7", Active: false, Message: "balance available"}}},
		states:       map[string]State{stateKey(1, "upstream:7"): {Active: true}},
	}
	var events []ops.Event
	if err := newTestEngine(repository, now, &events).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := repository.states[stateKey(1, "upstream:7")]
	if len(events) != 1 || events[0].State != "resolved" || state.Active {
		t.Fatalf("events=%#v state=%#v", events, state)
	}
}

func TestEngineContinuesAfterRuleFailure(t *testing.T) {
	repository := &testRepository{
		rules: []Rule{
			{ID: 1, Event: EventLoginFailure, Enabled: true},
			{ID: 2, Event: EventNewLoginIP, Enabled: true},
		},
		observations: map[int64][]Observation{2: {{Key: "login:2", Active: true}}},
		observeErr:   map[int64]error{1: errors.New("database timeout")},
		states:       make(map[string]State),
	}
	var events []ops.Event
	err := newTestEngine(repository, time.Now(), &events).RunOnce(context.Background())
	if err == nil || len(events) != 1 {
		t.Fatalf("error=%v events=%#v", err, events)
	}
}

func TestEnginePrunesStaleAndDisabledStates(t *testing.T) {
	repository := &testRepository{
		rules: []Rule{
			{ID: 1, Event: EventLowBalance, Enabled: true},
			{ID: 2, Event: EventLatency, Enabled: false},
		},
		observations: map[int64][]Observation{1: {{Key: "upstream:1"}}},
		states: map[string]State{
			stateKey(1, "upstream:1"):       {Active: false},
			stateKey(1, "upstream:deleted"): {Active: true},
			stateKey(2, "upstream:2"):       {Active: true},
		},
	}
	var events []ops.Event
	if err := newTestEngine(repository, time.Now(), &events).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.states) != 1 {
		t.Fatalf("states = %#v", repository.states)
	}
	if _, ok := repository.states[stateKey(1, "upstream:1")]; !ok {
		t.Fatal("current observation state was pruned")
	}
}
