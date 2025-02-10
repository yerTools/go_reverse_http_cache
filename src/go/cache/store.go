package cache

import (
	"sync"
	"time"
)

const (
	ShardCount                  uint64 = 256
	StoreItemOverhead                  = 8 + 8 + 8 + 24 + 8
	StoreItemExpirationOverhead        = 8
)

type StoreItem[V any] struct {
	Key        StoreKey
	Value      V
	Cost       int64
	Expiration time.Time
}

type store[V any] struct {
	shards []*concurrentMap[V]
}

func NewStore[V any](now time.Time, bucketSize time.Duration, onRemoved func(value *StoreItem[V])) *store[V] {
	s := &store[V]{
		shards: make([]*concurrentMap[V], ShardCount),
	}

	for i := range s.shards {
		s.shards[i] = NewConcurrentMap[V](now, bucketSize, onRemoved)
	}

	return s
}

func (s *store[V]) Cost() int64 {
	cost := int64(0)
	for _, shard := range s.shards {
		cost += shard.Cost()
	}

	return cost
}

func (s *store[V]) Len() int {
	l := 0
	for _, shard := range s.shards {
		l += shard.Len()
	}
	return l
}

func (s *store[V]) Clear() {
	for _, shard := range s.shards {
		shard.Clear()
	}
}

func (s *store[V]) Keys(buffer []StoreKey) []StoreKey {
	for _, shard := range s.shards {
		buffer = shard.Keys(buffer)
	}
	return buffer
}

func (s *store[V]) Values(buffer []*StoreItem[V]) []*StoreItem[V] {
	for _, shard := range s.shards {
		buffer = shard.Values(buffer)
	}
	return buffer
}

func (s *store[V]) ValuesInExpirationBuckets(buffer []*StoreItem[V], from, until time.Time) []*StoreItem[V] {
	for _, shard := range s.shards {
		buffer = shard.ValuesInExpirationBuckets(buffer, from, until)
	}
	return buffer
}

func (s *store[V]) Set(now time.Time, item *StoreItem[V]) *StoreItem[V] {
	return s.shards[item.Key.Key%ShardCount].Set(now, item)
}

func (s *store[v]) PurgeExpired(now time.Time) {
	for _, shard := range s.shards {
		shard.PurgeExpired(now)
	}
}

func (s *store[V]) Get(now time.Time, key StoreKey) (*StoreItem[V], bool) {
	return s.shards[key.Key%ShardCount].Get(now, key)
}

func (s *store[V]) Remove(key StoreKey) {
	s.shards[key.Key%ShardCount].Remove(key)
}

type storeItemMap[V any] struct {
	data map[uint64][]*StoreItem[V]
}

func newStoreItemMap[V any]() storeItemMap[V] {
	return storeItemMap[V]{
		data: make(map[uint64][]*StoreItem[V]),
	}
}

func (m *storeItemMap[V]) Set(item *StoreItem[V]) *StoreItem[V] {
	var removed *StoreItem[V]

	existing, ok := m.data[item.Key.Key]
	if ok {
		for i, e := range existing {
			if e.Key.Conflict == item.Key.Conflict {
				removed = existing[i]
				existing[i] = item
				break
			}
		}
	}

	if removed == nil {
		if existing == nil {
			existing = []*StoreItem[V]{item}
		} else {
			existing = append(existing, item)
		}
	}
	m.data[item.Key.Key] = existing

	return removed
}

func (m *storeItemMap[V]) Remove(key StoreKey) *StoreItem[V] {
	existing, ok := m.data[key.Key]
	if !ok {
		return nil
	}

	if len(existing) == 1 {
		if existing[0].Key.Conflict == key.Conflict {
			delete(m.data, key.Key)
			return existing[0]
		}
		return nil
	}

	for i, e := range existing {
		if e.Key.Conflict == key.Conflict {
			removed := existing[i]

			m.data[key.Key] = append(existing[:i], existing[i+1:]...)

			return removed
		}
	}

	return nil
}

func (m *storeItemMap[V]) Get(key StoreKey) (*StoreItem[V], bool) {
	existing, ok := m.data[key.Key]
	if !ok {
		return nil, false
	}

	for _, e := range existing {
		if e.Key.Conflict == key.Conflict {
			return e, true
		}
	}

	return nil, false
}

func (m *storeItemMap[V]) IsEmpty() bool {
	return len(m.data) == 0
}

func (m *storeItemMap[V]) Len() int {
	i := 0
	for _, v := range m.data {
		i += len(v)
	}

	return i
}

func (m *storeItemMap[V]) Clear() {
	for k := range m.data {
		delete(m.data, k)
	}
}

func (m *storeItemMap[V]) Keys(buffer []StoreKey) []StoreKey {
	if buffer == nil {
		buffer = make([]StoreKey, 0, m.Len())
	}

	for _, v := range m.data {
		for _, e := range v {
			buffer = append(buffer, e.Key)
		}
	}

	return buffer
}

func (m *storeItemMap[V]) Values(buffer []*StoreItem[V]) []*StoreItem[V] {
	if buffer == nil {
		buffer = make([]*StoreItem[V], 0, m.Len())
	}

	for _, v := range m.data {
		buffer = append(buffer, v...)
	}

	return buffer
}

func currentBucket(t time.Time, size time.Duration) int64 {
	return t.UnixNano() / int64(size)
}

type concurrentMap[V any] struct {
	mutex     sync.RWMutex
	data      storeItemMap[V]
	onRemoved func(value *StoreItem[V])
	cost      int64

	bucketSize        time.Duration
	expirationBuckets map[int64]storeItemMap[V]
	purgedBucket      int64
}

func NewConcurrentMap[V any](now time.Time, bucketSize time.Duration, onRemoved func(value *StoreItem[V])) *concurrentMap[V] {
	m := &concurrentMap[V]{
		data:      newStoreItemMap[V](),
		onRemoved: onRemoved,

		bucketSize:        bucketSize,
		expirationBuckets: make(map[int64]storeItemMap[V]),
		purgedBucket:      currentBucket(now, bucketSize) - 1,
	}

	return m
}

func (m *concurrentMap[V]) Cost() int64 {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	return m.cost
}

func (m *concurrentMap[V]) Clear() {
	var removed []*StoreItem[V]
	defer func() {
		if removed != nil && m.onRemoved != nil {
			for _, item := range removed {
				m.onRemoved(item)
			}
		}
	}()

	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.onRemoved != nil {
		removed = m.data.Values(nil)
	}
	m.data.Clear()
	for k := range m.expirationBuckets {
		delete(m.expirationBuckets, k)
	}
	m.cost = 0
}

func (m *concurrentMap[V]) Len() int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	return m.data.Len()
}

func (m *concurrentMap[V]) Keys(buffer []StoreKey) []StoreKey {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	return m.data.Keys(buffer)
}

func (m *concurrentMap[V]) Values(buffer []*StoreItem[V]) []*StoreItem[V] {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	return m.data.Values(buffer)
}

func (m *concurrentMap[V]) ValuesInExpirationBuckets(buffer []*StoreItem[V], from, until time.Time) []*StoreItem[V] {
	var fromBucket int64
	if !from.IsZero() {
		fromBucket = currentBucket(from, m.bucketSize)
	}
	var untilBucket int64
	if !until.IsZero() {
		untilBucket = currentBucket(until, m.bucketSize)
	}

	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if len(m.expirationBuckets) == 0 || untilBucket <= m.purgedBucket && untilBucket != 0 {
		return buffer
	}

	if untilBucket == 0 {
		for number, bucket := range m.expirationBuckets {
			if number < fromBucket {
				continue
			}

			buffer = bucket.Values(buffer)
		}
		return buffer
	}

	if fromBucket <= m.purgedBucket {
		fromBucket = m.purgedBucket + 1
	}

	if len(m.expirationBuckets) <= int(untilBucket-fromBucket+1) {
		for number, bucket := range m.expirationBuckets {
			if number < fromBucket || number > untilBucket {
				continue
			}

			buffer = bucket.Values(buffer)
		}
	} else {
		for number := fromBucket; number <= untilBucket; number++ {
			bucket, ok := m.expirationBuckets[number]
			if !ok {
				continue
			}

			buffer = bucket.Values(buffer)
		}
	}

	return buffer
}

func (m *concurrentMap[V]) Set(now time.Time, item *StoreItem[V]) *StoreItem[V] {
	item.Cost += StoreItemOverhead
	if !item.Expiration.IsZero() {
		item.Cost += StoreItemExpirationOverhead
	}

	if !item.Expiration.IsZero() && !item.Expiration.After(now) {
		return nil
	}

	var removed *StoreItem[V]
	defer func() {
		if removed != nil && m.onRemoved != nil {
			m.onRemoved(removed)
		}
	}()

	m.mutex.Lock()
	defer m.mutex.Unlock()

	removed = m.data.Set(item)

	if removed != nil {
		m.cost -= removed.Cost
	}
	m.cost += item.Cost

	m.addItemToTimeBucketLocked(item)
	m.removeItemFromTimeBucketLocked(removed)

	return item
}

func (m *concurrentMap[V]) addItemToTimeBucketLocked(item *StoreItem[V]) {
	if item == nil || item.Expiration.IsZero() {
		return
	}

	bucket := currentBucket(item.Expiration, m.bucketSize)
	if bucket <= m.purgedBucket {
		return
	}

	b, ok := m.expirationBuckets[bucket]
	if !ok {
		b = newStoreItemMap[V]()
	}

	b.Set(item)
	m.expirationBuckets[bucket] = b
}

func (m *concurrentMap[V]) removeItemFromTimeBucketLocked(item *StoreItem[V]) {
	if item == nil || item.Expiration.IsZero() {
		return
	}

	bucket := currentBucket(item.Expiration, m.bucketSize)
	if bucket <= m.purgedBucket {
		return
	}

	b, ok := m.expirationBuckets[bucket]
	if !ok {
		return
	}

	b.Remove(item.Key)
	if b.IsEmpty() {
		delete(m.expirationBuckets, bucket)
	}
}

func (m *concurrentMap[V]) PurgeExpired(now time.Time) {
	currentBucket := currentBucket(now, m.bucketSize) - 1

	var removed []*StoreItem[V]
	defer func() {
		if removed == nil || m.onRemoved == nil {
			return
		}

		for _, item := range removed {
			m.onRemoved(item)
		}
	}()

	m.mutex.Lock()
	defer m.mutex.Unlock()

	if currentBucket <= m.purgedBucket {
		return
	}

	if len(m.expirationBuckets) == 0 {
		m.purgedBucket = currentBucket
		return
	}

	if len(m.expirationBuckets) <= int(currentBucket-m.purgedBucket) {
		for number, bucket := range m.expirationBuckets {
			if number > currentBucket {
				continue
			}

			m.purgeRemoveItemsLocked(bucket, number, &removed)
		}
	} else {
		for number := m.purgedBucket + 1; number <= currentBucket; number++ {
			bucket, ok := m.expirationBuckets[number]
			if !ok {
				continue
			}

			m.purgeRemoveItemsLocked(bucket, number, &removed)
		}
	}

	m.purgedBucket = currentBucket
}

func (m *concurrentMap[V]) purgeRemoveItemsLocked(bucket storeItemMap[V], number int64, removed *[]*StoreItem[V]) {
	values := bucket.Values(nil)

	for _, item := range values {
		m.cost -= item.Cost
		m.data.Remove(item.Key)
	}

	if m.onRemoved != nil {
		*removed = append(*removed, values...)
	}

	delete(m.expirationBuckets, number)
}

func (m *concurrentMap[V]) Get(now time.Time, key StoreKey) (*StoreItem[V], bool) {
	item, ok := m.get(key)
	if !ok {
		return nil, false
	}

	if item.Expiration.IsZero() || now.Before(item.Expiration) {
		return item, true
	}

	m.Remove(key)
	return nil, false
}

func (m *concurrentMap[V]) get(key StoreKey) (*StoreItem[V], bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	return m.data.Get(key)
}

func (m *concurrentMap[V]) Remove(key StoreKey) {
	var removed *StoreItem[V]
	defer func() {
		if removed != nil && m.onRemoved != nil {
			m.onRemoved(removed)
		}
	}()

	m.mutex.Lock()
	defer m.mutex.Unlock()

	removed = m.data.Remove(key)
	if removed != nil {
		m.cost -= removed.Cost
	}
	m.removeItemFromTimeBucketLocked(removed)
}
