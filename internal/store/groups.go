package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/gaoLfun/dapi/internal/core"
	"github.com/lib/pq"
)

var ErrInvalidGroup = errors.New("invalid group")
var ErrGroupHasKeys = errors.New("group has bound api keys")
var ErrGroupDisabled = errors.New("group is disabled")
var ErrGroupEmpty = errors.New("group has no upstream")

func (s *Store) ListGroups(ctx context.Context) ([]core.Group, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.id,g.name,g.enabled,g.created_at,g.updated_at,
			COALESCE(array_agg(gu.upstream_id ORDER BY u.priority,u.id) FILTER (WHERE gu.upstream_id IS NOT NULL),'{}'),
			COALESCE((SELECT count(*) FROM api_keys k WHERE k.group_id=g.id),0)
		FROM groups g
		LEFT JOIN group_upstreams gu ON gu.group_id=g.id
		LEFT JOIN upstreams u ON u.id=gu.upstream_id
		GROUP BY g.id ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]core.Group, 0)
	for rows.Next() {
		var group core.Group
		if err := rows.Scan(&group.ID, &group.Name, &group.Enabled, &group.CreatedAt, &group.UpdatedAt, pq.Array(&group.UpstreamIDs), &group.KeyCount); err != nil {
			return nil, err
		}
		result = append(result, group)
	}
	return result, rows.Err()
}

func (s *Store) Group(ctx context.Context, id int64) (core.Group, error) {
	var group core.Group
	err := s.db.QueryRowContext(ctx, `
		SELECT g.id,g.name,g.enabled,g.created_at,g.updated_at,
			COALESCE(array_agg(gu.upstream_id ORDER BY u.priority,u.id) FILTER (WHERE gu.upstream_id IS NOT NULL),'{}'),
			COALESCE((SELECT count(*) FROM api_keys k WHERE k.group_id=g.id),0)
		FROM groups g
		LEFT JOIN group_upstreams gu ON gu.group_id=g.id
		LEFT JOIN upstreams u ON u.id=gu.upstream_id
		WHERE g.id=$1 GROUP BY g.id`, id).Scan(
		&group.ID, &group.Name, &group.Enabled, &group.CreatedAt, &group.UpdatedAt, pq.Array(&group.UpstreamIDs), &group.KeyCount)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Group{}, ErrNotFound
	}
	return group, err
}

func (s *Store) CreateGroup(ctx context.Context, group core.Group) (int64, error) {
	if len(group.UpstreamIDs) == 0 {
		return 0, ErrInvalidGroup
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO groups(name,enabled) VALUES($1,$2) RETURNING id`, strings.TrimSpace(group.Name), group.Enabled).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO group_upstreams(group_id,upstream_id) SELECT $1,id FROM upstreams WHERE id=ANY($2)`, id, pq.Array(group.UpstreamIDs)); err != nil {
		return 0, err
	}
	var members int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM group_upstreams WHERE group_id=$1`, id).Scan(&members); err != nil || members != len(uniqueIDs(group.UpstreamIDs)) {
		return 0, ErrInvalidGroup
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) UpdateGroup(ctx context.Context, group core.Group) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var keyCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM api_keys WHERE group_id=$1`, group.ID).Scan(&keyCount); err != nil {
		return err
	}
	if keyCount > 0 && !group.Enabled {
		return ErrGroupHasKeys
	}
	if len(group.UpstreamIDs) == 0 && group.Enabled {
		return ErrInvalidGroup
	}
	result, err := tx.ExecContext(ctx, `UPDATE groups SET name=$1,enabled=$2,updated_at=now() WHERE id=$3`, strings.TrimSpace(group.Name), group.Enabled, group.ID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_upstreams WHERE group_id=$1`, group.ID); err != nil {
		return err
	}
	if len(group.UpstreamIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_upstreams(group_id,upstream_id) SELECT $1,id FROM upstreams WHERE id=ANY($2)`, group.ID, pq.Array(group.UpstreamIDs)); err != nil {
			return err
		}
		var members int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM group_upstreams WHERE group_id=$1`, group.ID).Scan(&members); err != nil || members != len(uniqueIDs(group.UpstreamIDs)) {
			return ErrInvalidGroup
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	var keyCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM api_keys WHERE group_id=$1`, id).Scan(&keyCount); err != nil {
		return err
	}
	if keyCount > 0 {
		return ErrGroupHasKeys
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func uniqueIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value > 0 {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func (s *Store) GroupAvailable(ctx context.Context, id int64) error {
	var enabled bool
	var members int
	err := s.db.QueryRowContext(ctx, `SELECT g.enabled, count(gu.upstream_id) FROM groups g LEFT JOIN group_upstreams gu ON gu.group_id=g.id WHERE g.id=$1 GROUP BY g.id`, id).Scan(&enabled, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !enabled {
		return ErrGroupDisabled
	}
	if members == 0 {
		return ErrGroupEmpty
	}
	return nil
}
