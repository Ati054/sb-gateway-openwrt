// domainbench compares immutable DNS indexes in isolated process runs.
// Example: go run ./tools/domainbench -kind sorted -input geosite.dat -set CN
// Run control, map, sorted and mph with the same arguments in separate processes.
// Subtract control's retained_heap_bytes to exclude the shared query corpus.
// Only exact and suffix entries are compared; other match kinds are reported
// as skipped. The build peak is sampled and is not a strict high-water mark.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/geodata/strmatcher"
	"google.golang.org/protobuf/encoding/protowire"
)

type record struct {
	name  string
	exact bool
}

type maps struct{ exact, suffix map[string]bool }

func (m maps) Match(name string) bool {
	if m.exact[name] {
		return true
	}
	for suffix := name; suffix != ""; {
		if m.suffix[suffix] {
			return true
		}
		_, next, ok := strings.Cut(suffix, ".")
		if !ok {
			break
		}
		suffix = next
	}
	return false
}

func wireFields(data []byte, visit func(protowire.Number, []byte)) {
	for len(data) != 0 {
		n, t, used := protowire.ConsumeTag(data)
		if used < 0 {
			panic("invalid protobuf tag")
		}
		data = data[used:]
		if t == protowire.BytesType {
			b, size := protowire.ConsumeBytes(data)
			if size < 0 {
				panic("invalid protobuf bytes")
			}
			visit(n, b)
			data = data[size:]
		} else {
			size := protowire.ConsumeFieldValue(n, t, data)
			if size < 0 {
				panic("invalid protobuf field")
			}
			data = data[size:]
		}
	}
}

func load(path, code string, count int) ([]record, int) {
	if path == "" {
		r := make([]record, count)
		for i := range r {
			r[i] = record{fmt.Sprintf("d%06d.p%d.example.test", i, i%37), i%5 == 0}
		}
		return r, 0
	}
	body, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var result []record
	skipped := 0
	wireFields(body, func(number protowire.Number, entry []byte) {
		if number != 1 {
			return
		}
		selected := false
		wireFields(entry, func(n protowire.Number, b []byte) {
			if n == 1 && strings.EqualFold(string(b), code) {
				selected = true
			}
		})
		if !selected {
			return
		}
		wireFields(entry, func(n protowire.Number, b []byte) {
			if n != 2 {
				return
			}
			var typ uint64
			var value string
			for len(b) > 0 {
				num, tag, used := protowire.ConsumeTag(b)
				if used < 0 {
					panic("invalid domain")
				}
				b = b[used:]
				size := 0
				if num == 1 && tag == protowire.VarintType {
					typ, size = protowire.ConsumeVarint(b)
				} else if num == 2 && tag == protowire.BytesType {
					var v []byte
					v, size = protowire.ConsumeBytes(b)
					value = strings.ToLower(string(v))
				} else {
					size = protowire.ConsumeFieldValue(num, tag, b)
				}
				if size < 0 {
					panic("invalid domain value")
				}
				b = b[size:]
			}
			if typ == 2 || typ == 3 {
				result = append(result, record{value, typ == 3})
			} else {
				skipped++
			}
		})
	})
	if len(result) == 0 {
		panic("empty selected domain set")
	}
	return result, skipped
}

func scanBytes() uint64 {
	s := []metrics.Sample{{Name: "/gc/scan/heap:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

func main() {
	kind := flag.String("kind", "sorted", "map, sorted, mph, control")
	path := flag.String("input", "", "optional GeoSite protobuf file")
	code := flag.String("set", "CN", "GeoSite code")
	count := flag.Int("count", 120000, "synthetic domains when input is absent")
	flag.Parse()
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	scanBase := scanBytes()
	records, skipped := load(*path, *code, *count)
	queries := make([]string, 0, 4096)
	for i := 0; i < 1024; i++ {
		name := records[i*len(records)/1024].name
		queries = append(queries, name, "www."+name, "no-boundary"+name, "missing.invalid")
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	peak := before.HeapAlloc
	done, stopped := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	go func() {
		defer close(stopped)
		t := time.NewTicker(time.Millisecond)
		defer t.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			mu.Lock()
			peak = max(peak, m.HeapAlloc)
			mu.Unlock()
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	started := time.Now()
	var match func(string) bool
	var keep any
	switch *kind {
	case "control":
		match = func(string) bool { return false }
	case "map":
		m := maps{make(map[string]bool), make(map[string]bool)}
		for _, r := range records {
			if r.exact {
				m.exact[r.name] = true
			} else {
				m.suffix[r.name] = true
			}
		}
		match, keep = m.Match, m
	case "sorted":
		var b Builder
		for _, r := range records {
			if r.exact {
				b.AddExact(r.name)
			} else {
				b.AddSuffix(r.name)
			}
		}
		m := b.Build()
		match, keep = m.Match, m
	case "mph":
		m := strmatcher.NewMphMatcherGroup()
		for _, r := range records {
			if r.exact {
				m.AddFullMatcher(strmatcher.FullMatcher(r.name), 0)
			} else {
				m.AddDomainMatcher(strmatcher.DomainMatcher(r.name), 0)
			}
		}
		if err := m.Build(); err != nil {
			panic(err)
		}
		match, keep = m.MatchAny, m
	default:
		panic("unknown kind")
	}
	build := time.Since(started)
	var built runtime.MemStats
	runtime.ReadMemStats(&built)
	close(done)
	<-stopped
	peak = max(peak, built.HeapAlloc)
	n := len(records)
	records = nil
	runtime.GC()
	runtime.GC()
	var retained runtime.MemStats
	runtime.ReadMemStats(&retained)
	scan := scanBytes()
	durations := make([]float64, 5)
	hits := 0
	for pass := range durations {
		start := time.Now()
		for i := 0; i < 200000; i++ {
			if match(queries[i%len(queries)]) {
				hits++
			}
		}
		durations[pass] = float64(time.Since(start).Nanoseconds()) / 200000
	}
	sort.Float64s(durations)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(keep)
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"kind": *kind, "set": *code, "domains": n, "skipped_keyword_regex": skipped,
		"retained_heap_bytes":           int64(retained.HeapAlloc) - int64(base.HeapAlloc),
		"scan_heap_bytes":               int64(scan) - int64(scanBase),
		"sampled_build_peak_heap_bytes": int64(peak) - int64(base.HeapAlloc),
		"build_alloc_bytes":             built.TotalAlloc - before.TotalAlloc, "build_ms": float64(build.Microseconds()) / 1000,
		"query_median_ns": durations[2], "query_alloc_bytes": after.TotalAlloc - retained.TotalAlloc,
		"hits": hits,
	}); err != nil {
		panic(err)
	}
}
