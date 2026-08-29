package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/gaoLfun/dapi/internal/store"
)

func TestValidateChannelRejectsEmptyRecipients(t *testing.T) {
	for _, config := range []string{
		`{"smtp_host":"smtp.example.com","smtp_port":587,"to":""}`,
		`{"smtp_host":"smtp.example.com","smtp_port":587,"to":["ops@example.com",""]}`,
	} {
		channel := store.NotificationChannel{Name: "mail", Kind: "email", Config: json.RawMessage(config)}
		if validateChannel(channel) == nil {
			t.Fatalf("accepted empty recipient: %s", config)
		}
	}
}

func TestValidateChannelRejectsUnknownWebhookProvider(t *testing.T) {
	channel := store.NotificationChannel{
		Name:   "hook",
		Kind:   "webhook",
		Config: json.RawMessage(`{"url":"https://hooks.example.com/test","provider":"dingtal"}`),
	}
	if validateChannel(channel) == nil {
		t.Fatal("accepted unknown webhook provider")
	}
}
