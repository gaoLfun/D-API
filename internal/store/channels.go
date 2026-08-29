package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ChannelByID returns a notification channel with its decrypted configuration.
// The admin API uses this for one-off connectivity checks without exposing the
// stored secret to the client.
func (s *Store) ChannelByID(ctx context.Context, id int64) (NotificationChannel, error) {
	var channel NotificationChannel
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id,name,kind,enabled,config_encrypted,created_at,updated_at
		FROM notification_channels WHERE id=$1`, id).
		Scan(&channel.ID, &channel.Name, &channel.Kind, &channel.Enabled, &encrypted, &channel.CreatedAt, &channel.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NotificationChannel{}, ErrNotFound
		}
		return NotificationChannel{}, err
	}
	plain, err := s.box.Decrypt(encrypted)
	if err != nil {
		return NotificationChannel{}, err
	}
	channel.Config = json.RawMessage(plain)
	return channel, nil
}
