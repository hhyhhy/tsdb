package memtsdb

import (
	"sync"
	"time"
)

const _shardGroupDuration = 1 * time.Minute

type TSDB interface {
	InsertPoints(points []Point)
	Query(tag Tag, min, max time.Time) []Point
}

type shardGroup struct {
	// [min, max)
	max   time.Time
	min   time.Time
	shard Shard
}

func newShardGroup(t time.Time, round time.Duration) shardGroup {
	sg := shardGroup{shard: NewShard()}
	sg.initTime(t, round)

	return sg
}

func (g *shardGroup) initTime(t time.Time, round time.Duration) {
	rounded := t.Round(round)

	if rounded.Sub(t) >= 0 {
		// round up
		g.min = rounded.Add(-round)
		g.max = rounded
	} else {
		// round down
		g.min = rounded
		g.max = rounded.Add(round)
	}
}

func (g *shardGroup) contains(t time.Time) bool {
	// [min, max)
	if g.min.Before(t) && g.max.After(t) {
		return true
	}

	if g.min.Equal(t) {
		return true
	}

	return false
}

func (g *shardGroup) have(min, max time.Time) bool {
	// [min, max)
	if g.min.Before(min) && g.max.After(min) {
		return true
	}

	if g.min.Before(max) && g.max.After(max) {
		return true
	}

	if g.min.Equal(min) {
		return true
	}

	return false
}

type tsdb struct {
	rd time.Duration
	mu sync.RWMutex

	sgs      []shardGroup
	emptySgs []shardGroup
}

func NewTSDB(retentionDuration time.Duration) TSDB {
	return &tsdb{rd: retentionDuration}
}

func (t *tsdb) getShardGroup(ti time.Time) shardGroup {
	for _, sg := range t.sgs {
		if sg.contains(ti) {
			return sg
		}
	}

	var sg shardGroup
	if len(t.emptySgs) == 0 {
		sg = newShardGroup(ti, _shardGroupDuration)
	} else {
		// pop a shardGroup from used groups
		sg = t.emptySgs[len(t.emptySgs)-1]
		t.emptySgs = t.emptySgs[:len(t.emptySgs)-1]
		sg.initTime(ti, _shardGroupDuration)
	}

	t.sgs = append(t.sgs, sg)

	return sg
}

func (t *tsdb) InsertPoints(points []Point) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, point := range points {
		sg := t.getShardGroup(point.Time)
		sg.shard.Insert(point)
	}
}

func (t *tsdb) Query(tag Tag, min, max time.Time) []Point {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var ps []Point
	for _, sg := range t.sgs {
		if !sg.have(min, max) {
			continue
		}

		sgp := sg.shard.Query(tag, min, max)
		ps = append(ps, sgp...)
	}

	return ps
}