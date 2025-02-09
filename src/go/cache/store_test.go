package cache_test

import (
	"sync"
	"testing"
	"time"

	"github.com/yerTools/go_reverse_http_cache/src/go/cache"
)

func fixedTime() time.Time {
	return time.Date(2025, 1, 1, 1, 12, 0, 0, time.UTC)
}

func TestStoreSetGet(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 1, Conflict: 0},
		Value: 42,
		Cost:  1,
	}
	store.Set(now, item)

	retrieved, ok := store.Get(now, item.Key)
	if !ok {
		t.Fatalf("Expected item to be present")
	}

	if retrieved.Value != item.Value {
		t.Fatalf("Expected value %d, got %d", item.Value, retrieved.Value)
	}
}

func TestStoreRemove(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 2, Conflict: 0},
		Value: 84,
		Cost:  1,
	}
	store.Set(now, item)

	store.Remove(item.Key)

	_, ok := store.Get(now, item.Key)
	if ok {
		t.Fatalf("Expected item to be removed")
	}
}

func TestStorePurgeExpired(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item := &cache.StoreItem[int]{
		Key:        cache.StoreKey{Key: 3, Conflict: 0},
		Value:      168,
		Cost:       1,
		Expiration: now.Add(-time.Minute), // expired item
	}
	store.Set(now, item)

	store.PurgeExpired(now)

	_, ok := store.Get(now, item.Key)
	if ok {
		t.Fatalf("Expected expired item to be purged")
	}
}

func TestStoreCost(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item1 := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 4, Conflict: 0},
		Value: 100,
		Cost:  10,
	}
	item2 := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 5, Conflict: 0},
		Value: 200,
		Cost:  20,
	}

	store.Set(now, item1)
	store.Set(now, item2)

	expectedCost := item1.Cost + item2.Cost
	if store.Cost() != expectedCost {
		t.Fatalf("Expected cost %d, got %d", expectedCost, store.Cost())
	}
}

func TestConcurrentAccess(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 6, Conflict: 0},
		Value: 300,
		Cost:  30,
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Concurrent Set
	go func() {
		defer wg.Done()
		store.Set(now, item)
	}()

	// Concurrent Get
	go func() {
		defer wg.Done()
		store.Get(now, item.Key)
	}()

	wg.Wait()
}

func TestBoundaryExpiration(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item := &cache.StoreItem[int]{
		Key:        cache.StoreKey{Key: 7, Conflict: 0},
		Value:      400,
		Cost:       1,
		Expiration: now, // expires exactly now
	}
	store.Set(now, item)

	_, ok := store.Get(now, item.Key)
	if ok {
		t.Fatalf("Expected item to be expired and purged")
	}
}

func TestMultipleConflictKeys(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item1 := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 8, Conflict: 0},
		Value: 500,
		Cost:  1,
	}
	item2 := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 8, Conflict: 1},
		Value: 600,
		Cost:  1,
	}

	store.Set(now, item1)
	store.Set(now, item2)

	retrieved1, ok1 := store.Get(now, item1.Key)
	if !ok1 || retrieved1.Value != item1.Value {
		t.Fatalf("Expected value %d, got %d", item1.Value, retrieved1.Value)
	}

	retrieved2, ok2 := store.Get(now, item2.Key)
	if !ok2 || retrieved2.Value != item2.Value {
		t.Fatalf("Expected value %d, got %d", item2.Value, retrieved2.Value)
	}
}

func TestEmptyStoreOperations(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	_, ok := store.Get(now, cache.StoreKey{Key: 9, Conflict: 0})
	if ok {
		t.Fatalf("Expected no item to be present in empty store")
	}

	store.Remove(cache.StoreKey{Key: 9, Conflict: 0}) // Should not panic or error
}

func TestUpdateExistingItem(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 10, Conflict: 0},
		Value: 700,
		Cost:  1,
	}
	store.Set(now, item)

	updatedItem := &cache.StoreItem[int]{
		Key:   item.Key,
		Value: 800,
		Cost:  2,
	}
	store.Set(now, updatedItem)

	retrieved, ok := store.Get(now, updatedItem.Key)
	if !ok || retrieved.Value != updatedItem.Value {
		t.Fatalf("Expected updated value %d, got %d", updatedItem.Value, retrieved.Value)
	}

	expectedCost := updatedItem.Cost
	if store.Cost() != expectedCost {
		t.Fatalf("Expected cost %d, got %d", expectedCost, store.Cost())
	}
}

func TestDifferentBucketSizes(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Second, nil)

	item := &cache.StoreItem[int]{
		Key:        cache.StoreKey{Key: 11, Conflict: 0},
		Value:      900,
		Cost:       1,
		Expiration: now.Add(time.Second),
	}
	store.Set(now, item)

	now = now.Add(2 * time.Second)
	store.PurgeExpired(now)

	_, ok := store.Get(now, item.Key)
	if ok {
		t.Fatalf("Expected item to be purged due to expiration")
	}
}

func TestCostAfterRemoval(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item := &cache.StoreItem[int]{
		Key:   cache.StoreKey{Key: 12, Conflict: 0},
		Value: 1000,
		Cost:  10,
	}
	store.Set(now, item)

	store.Remove(item.Key)

	if store.Cost() != 0 {
		t.Fatalf("Expected cost 0 after removal, got %d", store.Cost())
	}
}

func TestClearStore(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, time.Minute, nil)

	item1 := &cache.StoreItem[int]{
		Key:        cache.StoreKey{Key: 13, Conflict: 0},
		Value:      1100,
		Cost:       10,
		Expiration: now.Add(time.Minute),
	}
	item2 := &cache.StoreItem[int]{
		Key:        cache.StoreKey{Key: 14, Conflict: 0},
		Value:      1200,
		Cost:       20,
		Expiration: now.Add(time.Minute),
	}

	store.Set(now, item1)
	store.Set(now, item2)

	store.PurgeExpired(now.Add(time.Hour)) // Simulate clearing the store

	if store.Cost() != 0 {
		t.Fatalf("Expected cost 0 after clearing store, got %d", store.Cost())
	}
}

func TestStoreExpiration(t *testing.T) {
	now := fixedTime()
	store := cache.NewStore[int](now, 500*time.Millisecond, nil)
	item := &cache.StoreItem[int]{
		Key:        cache.StoreKey{Key: 269, Conflict: 161},
		Value:      161,
		Cost:       1,
		Expiration: now.Add(5 * time.Second),
	}
	store.Set(now, item)
	retrieved, ok := store.Get(now, item.Key)
	if !ok {
		t.Fatalf("Expected item to be present")
	}
	if retrieved.Value != item.Value {
		t.Fatalf("Expected value %d, got %d", item.Value, retrieved.Value)
	}
	retrieved, ok = store.Get(now.Add(time.Second), item.Key)
	if !ok {
		t.Fatalf("Expected item to be present")
	}
	if retrieved.Value != item.Value {
		t.Fatalf("Expected value %d, got %d", item.Value, retrieved.Value)
	}
	retrieved, ok = store.Get(now.Add(3*time.Second), item.Key)
	if !ok {
		t.Fatalf("Expected item to be present")
	}
	if retrieved.Value != item.Value {
		t.Fatalf("Expected value %d, got %d", item.Value, retrieved.Value)
	}
	_, ok = store.Get(now.Add(5*time.Second), item.Key)
	if ok {
		t.Fatalf("Expected item to be purged")
	}
}
