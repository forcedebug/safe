package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/MixinNetwork/safe/common"
)

var requestCols = []string{"request_id", "mixin_hash", "mixin_index", "asset_id", "amount", "role", "action", "curve", "holder", "extra", "state", "created_at", "updated_at"}

func requestFromRow(row *sql.Row) (*common.Request, error) {
	var r common.Request
	err := row.Scan(&r.Id, &r.MixinHash, &r.MixinIndex, &r.AssetId, &r.Amount, &r.Role, &r.Action, &r.Curve, &r.Holder, &r.Extra, &r.State, &r.CreatedAt, &time.Time{})
	return &r, err
}

func (s *SQLite3Store) WriteRequestIfNotExist(ctx context.Context, req *common.Request) error {
	if req.State == 0 || req.Role == 0 {
		panic(req)
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existed, err := s.checkExistence(ctx, tx, "SELECT request_id FROM requests WHERE request_id=?", req.Id)
	if err != nil || existed {
		return err
	}

	vals := []any{req.Id, req.MixinHash, req.MixinIndex, req.AssetId, req.Amount, req.Role, req.Action, req.Curve, req.Holder, req.Extra, req.State, req.CreatedAt, req.CreatedAt}
	err = s.execOne(ctx, tx, buildInsertionSQL("requests", requestCols), vals...)
	if err != nil {
		return fmt.Errorf("INSERT requests %v", err)
	}
	return tx.Commit()
}

func (s *SQLite3Store) ReadRequest(ctx context.Context, id string) (*common.Request, error) {
	query := fmt.Sprintf("SELECT %s FROM requests WHERE request_id=?", strings.Join(requestCols, ","))
	row := s.db.QueryRowContext(ctx, query, id)

	r, err := requestFromRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *SQLite3Store) FinishRequest(ctx context.Context, id string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	err = s.execOne(ctx, tx, "UPDATE requests SET state=?, updated_at=? WHERE request_id=? AND state=?",
		common.RequestStateDone, time.Now().UTC(), id, common.RequestStateInitial)
	if err != nil {
		return fmt.Errorf("UPDATE requests %v", err)
	}
	return tx.Commit()
}

func (s *SQLite3Store) ReadPendingRequest(ctx context.Context) (*common.Request, error) {
	query := fmt.Sprintf("SELECT %s FROM requests WHERE state=? ORDER BY created_at ASC", strings.Join(requestCols, ","))
	row := s.db.QueryRowContext(ctx, query, common.RequestStateInitial)

	r, err := requestFromRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *SQLite3Store) ReadLatestRequest(ctx context.Context) (*common.Request, error) {
	query := fmt.Sprintf("SELECT %s FROM requests ORDER BY created_at DESC LIMIT 1", strings.Join(requestCols, ","))
	row := s.db.QueryRowContext(ctx, query)

	r, err := requestFromRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}