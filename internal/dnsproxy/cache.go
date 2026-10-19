package dnsproxy

import (
	"encoding/binary"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
)

// Account for entries, map buckets and allocator overhead in addition to wire
// data. This is a conservative cache budget, not a promise about process RSS.
const cacheEntryOverhead int64 = 256

func dnsCacheBudget(totalRAM int64) int64 { return max(256<<10, totalRAM/128) }

// The caller holds cacheMu. A generation change releases every old route's
// cached response and LRU references in the same transaction.
func (s *server) clearCacheLocked() {
	s.cache = make(map[string]*cacheEntry)
	s.cacheHead, s.cacheTail = nil, nil
	s.cacheBytes = 0
}

func (s *server) unlinkCacheEntry(entry *cacheEntry) {
	if entry.prev != nil {
		entry.prev.next = entry.next
	} else if s.cacheHead == entry {
		s.cacheHead = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	} else if s.cacheTail == entry {
		s.cacheTail = entry.prev
	}
	entry.prev, entry.next = nil, nil
}

func (s *server) touchCacheEntry(entry *cacheEntry) {
	if s.cacheHead == entry {
		return
	}
	s.unlinkCacheEntry(entry)
	entry.next = s.cacheHead
	if s.cacheHead != nil {
		s.cacheHead.prev = entry
	} else {
		s.cacheTail = entry
	}
	s.cacheHead = entry
}

func (s *server) removeCacheEntry(entry *cacheEntry) {
	s.unlinkCacheEntry(entry)
	delete(s.cache, entry.key)
	s.cacheBytes -= entry.bytes
}

func (s *server) cached(key string, transactionID []byte) []byte {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, ok := s.cache[key]
	if !ok {
		return nil
	}
	now := time.Now()
	if !now.Before(entry.expires) {
		s.removeCacheEntry(entry)
		return nil
	}
	s.touchCacheEntry(entry)
	response := append([]byte(nil), entry.response...)
	copy(response[:2], transactionID)
	elapsed := uint32(now.Sub(entry.stored) / time.Second)
	for _, field := range entry.TTLs {
		remaining := uint32(0)
		if field.value > elapsed {
			remaining = field.value - elapsed
		}
		binary.BigEndian.PutUint32(response[field.offset:field.offset+4], remaining)
	}
	return response
}

func (s *server) remember(key string, response []byte) {
	fields, ttl, ok := answerTTLs(response)
	if !ok || ttl == 0 {
		return
	}
	// Keep the upstream TTL. Route changes explicitly retire their cached
	// answers, so a fixed 30-second expiry only creates unnecessary DNS load.
	cost := cacheEntryOverhead + int64(len(key)+len(response)+cap(fields)*16)
	now := time.Now()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cacheBudget == 0 {
		s.cacheBudget = dnsCacheBudget(resources.CurrentBudget().Total)
	}
	if cost > s.cacheBudget {
		return
	}
	if s.cache == nil {
		s.clearCacheLocked()
	}
	if old := s.cache[key]; old != nil {
		s.removeCacheEntry(old)
	}
	for s.cacheBytes+cost > s.cacheBudget && s.cacheTail != nil {
		s.removeCacheEntry(s.cacheTail)
		s.cacheEvictions++
	}
	entry := &cacheEntry{key: key, bytes: cost, response: append([]byte(nil), response...), TTLs: fields,
		stored: now, expires: now.Add(time.Duration(ttl) * time.Second)}
	s.cache[key] = entry
	s.cacheBytes += cost
	s.touchCacheEntry(entry)
}
