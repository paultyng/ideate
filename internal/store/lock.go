package store

import "sync"

// slugLockManager hands out a per-slug *sync.Mutex used to serialize
// in-process read-modify-write callers against the same idea's
// on-disk artifacts (idea.md, backlog.json, sessions/*.json).
//
// Atomic temp+rename writes (atomicfile.Write) already protect readers
// against torn writes, so reads do not take the lock; the lock exists
// only to prevent concurrent writers from clobbering each other (e.g.
// two MCP callers both running List → mutate → write on backlog.json).
//
// Cleanup: the map grows monotonically. For a single-user personal app
// at O(active ideas) it isn't worth the bookkeeping to evict entries
// on archive/delete.
type slugLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newSlugLockManager() *slugLockManager {
	return &slugLockManager{locks: map[string]*sync.Mutex{}}
}

// Lock acquires the mutex for the given slug and returns the unlock
// function. Always `defer unlock()` immediately after the call:
//
//	unlock := s.locks.Lock(slug)
//	defer unlock()
//
// The per-slug mutex is not reentrant — methods that hold the lock
// must not call other methods that also call Lock on the same slug.
// Use unlocked helpers internally (e.g. updateUnlocked) when one
// write path needs to invoke another.
func (m *slugLockManager) Lock(slug string) func() {
	m.mu.Lock()
	lock, ok := m.locks[slug]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[slug] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}
