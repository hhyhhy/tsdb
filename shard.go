package tsdb

import (
	"sync"

	"github.com/cespare/xxhash"
)

const _partitionNum = 16

// Value 保存时间和值
type Value[T any] struct {
	UnixNano int64
	V        T
}

// entry 保存 values，目的减少写入已存在系列的数据的锁争用
type entry[T any] struct {
	mu sync.RWMutex

	values []Value[T]
}

// newEntry copy Value 并构建一个新的 entry
func newEntry[T any](vs []Value[T]) *entry[T] {
	values := make([]Value[T], 0, len(vs))
	values = append(values, vs...)

	return &entry[T]{values: values}
}

// add 往 entry 中写入数据
func (e *entry[T]) add(values []Value[T]) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.values = append(e.values, values...)
}

// removeBefore 删除小于 unixNano 的数据
func (e *entry[T]) removeBefore(unixNano int64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	values := make([]Value[T], 0, len(e.values))
	for _, v := range e.values {
		if v.UnixNano >= unixNano {
			values = append(values, v)
		}
	}
	e.values = values
}

// valuesBetween 获取两个时间之间的 Value
func (e *entry[T]) valuesBetween(min, max int64) []Value[T] {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var values []Value[T]
	for _, v := range e.values {
		if v.UnixNano >= min && v.UnixNano <= max {
			values = append(values, v)
		}
	}

	return values
}

// partition hash ring 的一个分片，目的是减少新新系列的锁争用
type partition[T any] struct {
	mu sync.RWMutex
	// 存储系列和值
	// {"series ex:host=A,region=SH":[value1, value2]}
	store map[string]*entry[T]
}

func newPartition[T any]() *partition[T] {
	store := make(map[string]*entry[T])

	return &partition[T]{store: store}
}

// write 往分片中写入数据
func (p *partition[T]) write(key string, values []Value[T]) {
	p.mu.RLock()
	e := p.store[key]
	p.mu.RUnlock()
	if e != nil {
		// 大部分情况会走进这个 if 里面，如果 系列 已经存在
		e.add(values)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// 因为中间有一段过程没锁，可能有别的协程已经写入，所以再检查一遍
	if e := p.store[key]; e != nil {
		e.add(values)
		return
	}

	e = newEntry(values)
	p.store[key] = e
}

// removeBefore 移除时间小于给定值的数据
func (p *partition[T]) removeBefore(unixNano int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	store := make(map[string]*entry[T], len(p.store))
	for k, e := range p.store {
		e.removeBefore(unixNano)
		// cap = 0 说明上次 remove 的时候已经没有 Value ， 较大可能后续也没有 Value ，就不加入 store 了
		if cap(e.values) != 0 {
			store[k] = e
		}
	}
	p.store = store
}

func (p *partition[T]) valuesBetween(key string, min, max int64) []Value[T] {
	p.mu.RLock()
	e := p.store[key]
	p.mu.RUnlock()

	if e == nil {
		return nil
	}
	return e.valuesBetween(min, max)
}

type shard[T any] struct {
	partitions []*partition[T]
}

func newShard[T any]() *shard[T] {
	partitions := make([]*partition[T], 0, _partitionNum)

	for i := 0; i < _partitionNum; i++ {
		partitions = append(partitions, newPartition[T]())
	}

	return &shard[T]{partitions: partitions}
}

func (s *shard[T]) removeBefore(unixNano int64) {
	for _, p := range s.partitions {
		p.removeBefore(unixNano)
	}
}

func (s *shard[T]) getPartitions(key string) *partition[T] {
	return s.partitions[int(xxhash.Sum64([]byte(key))%uint64(len(s.partitions)))]
}

func (s *shard[T]) writeMulti(values map[string][]Value[T]) {
	for k, v := range values {
		s.getPartitions(k).write(k, v)
	}
}
