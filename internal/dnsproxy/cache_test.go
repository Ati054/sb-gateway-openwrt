package dnsproxy

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/resources"
)

func TestCacheLRUEvictsColdEntryWithinByteBudget(t *testing.T) {
	response := cacheableDoHReply(dnsQuery("example.test"), 42)
	s := &server{cacheBudget: 4096}
	s.remember("a", response)
	s.cacheBudget = 3 * s.cacheBytes
	s.remember("b", response)
	s.remember("c", response)
	if s.cached("a", []byte{1, 2}) == nil {
		t.Fatal("hot entry lost")
	}
	s.remember("d", response)
	if len(s.cache) != 3 || s.cache["b"] != nil || s.cache["a"] == nil || s.cacheBytes > s.cacheBudget || s.cacheEvictions != 1 {
		t.Fatal("LRU or RAM budget violated")
	}
	s.remember("a", response)
	if len(s.cache) != 3 || s.cacheBytes > s.cacheBudget {
		t.Fatal("replacement leaked cache accounting")
	}
	oldBytes := s.cacheBytes
	s.cache["a"].expires = time.Now().Add(-time.Second)
	if s.cached("a", []byte{1, 2}) != nil || s.cache["a"] != nil || s.cacheBytes >= oldBytes {
		t.Fatal("expired entry retained")
	}
	s.clearCacheLocked()
	if len(s.cache) != 0 || s.cacheBytes != 0 || s.cacheHead != nil || s.cacheTail != nil {
		t.Fatal("retired generation retained LRU references")
	}
}

func TestCacheIsNotLimitedTo512AndPreservesTTL(t *testing.T) {
	s := &server{cacheBudget: dnsCacheBudget(256 * resources.MiB)}
	query := dnsQuery("ttl.test")
	response := cacheableDoHReply(query, 42)
	binary.BigEndian.PutUint32(response[len(query)+6:len(query)+10], 300)
	for i := 0; i < 2048; i++ {
		s.remember(fmt.Sprint(i), response)
	}
	if len(s.cache) != 2048 || s.cacheBytes > s.cacheBudget {
		t.Fatal("fixed entry-count limit or RAM budget violation")
	}
	if time.Until(s.cache["0"].expires) < 290*time.Second {
		t.Fatal("upstream TTL was shortened")
	}
	if s.cached("0", []byte{1, 2}) == nil {
		t.Fatal("valid cached answer lost")
	}
}

func TestCacheAgesAuthorityAndPreservesEDNSFlags(t *testing.T) {
	query := dnsQuery("ttl.test")
	response := cacheableDoHReply(query, 42)
	binary.BigEndian.PutUint32(response[len(query)+6:len(query)+10], 300)
	binary.BigEndian.PutUint16(response[8:10], 1)
	binary.BigEndian.PutUint16(response[10:12], 1)
	// Authority NS record, lifetime 60 seconds, compressed name and target.
	response = append(response, 0xc0, 0x0c, 0, 2, 0, 1, 0, 0, 0, 60, 0, 2, 0xc0, 0x0c)
	// EDNS DO flag must stay intact and must not shorten the cache lifetime.
	response = append(response, 0, 0, 41, 16, 0, 0, 0, 0x80, 0, 0, 0)
	s := &server{cacheBudget: 4096}
	s.remember("all-sections", response)
	entry := s.cache["all-sections"]
	if entry == nil || len(entry.TTLs) != 2 || time.Until(entry.expires) < 55*time.Second || time.Until(entry.expires) > 61*time.Second {
		t.Fatal("wrong complete-response cache lifetime")
	}
	entry.stored = entry.stored.Add(-30 * time.Second)
	got := s.cached("all-sections", query[:2])
	if binary.BigEndian.Uint32(got[entry.TTLs[0].offset:]) != 270 || binary.BigEndian.Uint32(got[entry.TTLs[1].offset:]) != 30 {
		t.Fatal("resource records did not age together")
	}
	if binary.BigEndian.Uint32(got[len(got)-6:len(got)-2]) != 0x8000 {
		t.Fatal("EDNS flags changed")
	}
}
