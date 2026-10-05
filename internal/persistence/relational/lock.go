package relational

import (
	"context"
	"hash/fnv"
	"sync"

	"gorm.io/gorm"
)

// lockStripes serialize WithLock callers inside one process. SQLite has a
// single writer process, so the stripes are its whole lock; Postgres servers
// share the database and additionally take a session advisory lock.
type lockStripes [64]sync.Mutex

func (l *lockStripes) of(key string) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key))
	return &l[hash.Sum32()%uint32(len(l))]
}

// WithLock runs fn while holding the named lock across every server sharing
// this database. It must not be called inside a transaction, and fn must not
// take another named lock.
func (s *Store) WithLock(ctx context.Context, key string, fn func(context.Context) error) error {
	stripe := s.locks.of(key)
	stripe.Lock()
	defer stripe.Unlock()
	if s.db.Dialector.Name() != "postgres" {
		return fn(ctx)
	}
	return s.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SELECT pg_advisory_lock(hashtextextended(?, 0))", key).Error; err != nil {
			return err
		}
		// The unlock must run even when ctx was canceled; closing the session
		// would release the lock too, but the pool keeps the session open.
		defer conn.WithContext(context.WithoutCancel(ctx)).Exec("SELECT pg_advisory_unlock(hashtextextended(?, 0))", key)
		return fn(ctx)
	})
}
