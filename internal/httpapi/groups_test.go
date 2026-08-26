package httpapi

import "testing"

func TestCleanGroupIDs(t *testing.T) {
	got := cleanGroupIDs([]int64{3, 0, 3, -1, 2})
	if len(got) != 2 || got[0] != 3 || got[1] != 2 {
		t.Fatalf("clean group ids = %#v", got)
	}
}

func TestValidGroupPayload(t *testing.T) {
	if validGroupPayload(groupPayload{Name: "empty"}, false) {
		t.Fatal("empty group accepted")
	}
	if !validGroupPayload(groupPayload{Name: "disabled", Enabled: boolPtr(false)}, true) {
		t.Fatal("disabled group rejected")
	}
}

func boolPtr(value bool) *bool { return &value }
