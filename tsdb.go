package tsdb

import (
	"time"
)

type TSDB[T any] struct {
	retentionPolicy time.Duration

	stop     chan struct{}
	isClosed bool

	idx   *index
	store *shard[T]
}

func New[T any](retentionPolicy time.Duration) *TSDB[T] {
	store := newShard[T]()
	idx := newIndex()

	stop := make(chan struct{})
	db := &TSDB[T]{retentionPolicy: retentionPolicy, store: store, idx: idx, stop: stop}

	go db.gc()

	return db
}

func (db *TSDB[T]) WritePoints(points []Point[T]) error {
	if db.isClosed {
		return ErrDBClosed
	}

	seriesTags := make(map[string][]Tag, len(points))
	values := make(map[string][]value[T], len(points))
	for _, point := range points {
		s := point.Series()
		if len(s) == 0 {
			return ErrPointMissingTag
		}

		v := value[T]{unixNano: point.time.UnixNano(), v: point.field}
		values[s] = append(values[s], v)

		if _, ok := seriesTags[s]; ok {
			continue
		}
		seriesTags[s] = point.tags
	}

	db.idx.createSeriesIfNotExists(seriesTags)
	db.store.writeMulti(values)

	return nil
}

func (db *TSDB[T]) Stop() {
	db.stop <- struct{}{}
	db.isClosed = true
}

func (db *TSDB[T]) gc() {
	ticker := time.NewTicker(db.retentionPolicy)
	defer ticker.Stop()

	for {
		select {
		case <-db.stop:
			return
		case <-ticker.C:
			remove := time.Now().Add(-db.retentionPolicy).UnixNano()
			db.store.removeBefore(remove)
		}
	}
}
