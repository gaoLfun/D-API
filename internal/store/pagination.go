package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// PageCursor uses database precision and a unique tie breaker, not a row offset.
type PageCursor struct {
	At time.Time `json:"at"`
	ID int64     `json:"id"`
}

func ParsePageCursor(raw string) (*PageCursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 256 {
		return nil, errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	var cursor PageCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.ID <= 0 || cursor.At.IsZero() || cursor.At.Year() < 1 || cursor.At.Year() > 9999 {
		return nil, errors.New("invalid cursor")
	}
	return &cursor, nil
}

func encodePageCursor(at time.Time, id int64) string {
	data, _ := json.Marshal(PageCursor{At: at, ID: id})
	return base64.RawURLEncoding.EncodeToString(data)
}

type CursorPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func (s *Store) RequestLogPage(ctx context.Context, filter LogFilter) (CursorPage[RequestLogView], error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	limit := filter.Limit
	filter.Limit++
	filter.Offset = 0
	items, err := s.listRequestLogs(ctx, filter)
	page := CursorPage[RequestLogView]{Items: items}
	if err == nil && len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodePageCursor(last.CreatedAt, last.ID)
	}
	return page, err
}

func (s *Store) DeadNotificationPage(ctx context.Context, cursor *PageCursor) (CursorPage[DeadNotification], error) {
	items, err := s.listDeadNotifications(ctx, 0, 51, cursor)
	page := CursorPage[DeadNotification]{Items: items}
	if err == nil && len(items) > 50 {
		last := items[49]
		page.Items = items[:50]
		page.NextCursor = encodePageCursor(last.DeadAt, last.ID)
	}
	return page, err
}
