package cache

import "time"

type Cache[V any] struct {
	store *store[V]
}

func NewCache[V any](bucketSize time.Duration, onRemoved func(value *StoreItem[V])) *Cache[V] {
	c := &Cache[V]{
		store: NewStore[V](time.Now(), bucketSize, onRemoved),
	}

	t := time.NewTicker(bucketSize)
	go func() {
		for now := range t.C {
			c.store.PurgeExpired(now)
		}
	}()

	return c
}

func (c *Cache[V]) Cost() int64 {
	return c.store.Cost()
}

func (c *Cache[V]) Set(key StoreKey, value V, cost int64, ttl time.Duration) *StoreItem[V] {
	if c == nil {
		return nil
	}

	var expiration time.Time
	now := time.Now()
	switch {
	case ttl == 0:
		break
	case ttl < 0:
		return nil
	default:
		expiration = now.Add(ttl)
	}

	i := &StoreItem[V]{
		Key:        key,
		Value:      value,
		Cost:       cost,
		Expiration: expiration,
	}

	return c.store.Set(now, i)
}

func (c *Cache[V]) Get(key StoreKey) (StoreItem[V], bool) {

	item, ok := c.store.Get(time.Now(), key)
	if !ok {
		var zeroValue StoreItem[V]
		return zeroValue, false
	}

	return *item, true
}
