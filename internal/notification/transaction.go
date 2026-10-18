package notification

import (
	"context"
	"database/sql"
	"gorm.io/gorm"
	"sync/atomic"
)

// A wake belongs to the outer SQL commit, not an ORM after-create callback:
// callbacks can run inside an uncommitted caller-owned transaction.
type notifyingPool struct {
	gorm.ConnPool
	database *sql.DB
}

func (p *notifyingPool) GetDBConn() (*sql.DB, error) { return p.database, nil }
func (p *notifyingPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.database.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &notifyingTx{Tx: tx}, nil
}

type notifyingTx struct {
	*sql.Tx
	dirty atomic.Bool
}

func (t *notifyingTx) NotificationQueued() { t.dirty.Store(true) }
func (t *notifyingTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil && t.dirty.Load() {
		Wake()
	}
	return err
}
func markQueued(tx *gorm.DB) {
	if q, yes := tx.Statement.ConnPool.(interface{ NotificationQueued() }); yes {
		q.NotificationQueued()
	}
}
func wrapTransactions(db *gorm.DB) error {
	if _, yes := db.ConnPool.(*notifyingPool); yes {
		return nil
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	wrapped := &notifyingPool{ConnPool: db.ConnPool, database: pool}
	db.ConnPool = wrapped
	db.Statement.ConnPool = wrapped
	return nil
}
