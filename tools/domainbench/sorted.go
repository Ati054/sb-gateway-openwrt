// This alternative sorted index is used only by the comparison tool.
// The arena and numeric index contain no pointers for the GC to scan.
package main

import (
	"bytes"
	"math"
	"slices"
	"strings"
)

const suffixFlag uint32 = 1 << 31
const offsetMask = suffixFlag - 1

type entry struct {
	offset, length uint32
	suffix         bool
}

// Builder retains no references to the supplied strings. Build consumes it.
type Builder struct {
	data    []byte
	entries []entry
}

func (b *Builder) AddExact(name string)  { b.add(name, false) }
func (b *Builder) AddSuffix(name string) { b.add(name, true) }

func (b *Builder) add(name string, suffix bool) {
	name = strings.ToLower(name)
	if uint64(len(b.data))+uint64(len(name)) > math.MaxInt32 {
		panic("domainindex: domain arena exceeds 2 GiB")
	}
	e := entry{offset: uint32(len(b.data)), length: uint32(len(name)), suffix: suffix}
	for i := len(name); i > 0; i-- {
		b.data = append(b.data, name[i-1])
	}
	b.entries = append(b.entries, e)
}

// Index is immutable and can be shared by concurrent DNS readers. Keys contain
// reversed bytes. The top offset bit marks suffix rules; unmarked keys match
// only the complete name.
type Index struct {
	data    []byte
	offsets []uint32 // one per distinct key, followed by the arena end
}

func (b *Builder) Build() Index {
	if len(b.entries) == 0 {
		*b = Builder{}
		return Index{}
	}
	key := func(e entry) []byte { return b.data[e.offset : e.offset+e.length] }
	slices.SortFunc(b.entries, func(a, c entry) int { return bytes.Compare(key(a), key(c)) })
	var size, count int
	for i, e := range b.entries {
		if i == 0 || !bytes.Equal(key(e), key(b.entries[i-1])) {
			size += int(e.length)
			count++
		}
	}
	result := Index{data: make([]byte, 0, size), offsets: make([]uint32, 0, count+1)}
	for i := 0; i < len(b.entries); {
		e := b.entries[i]
		k := key(e)
		suffix := e.suffix
		i++
		for i < len(b.entries) && bytes.Equal(k, key(b.entries[i])) {
			suffix = suffix || b.entries[i].suffix
			i++
		}
		offset := uint32(len(result.data))
		if suffix {
			offset |= suffixFlag
		}
		result.offsets = append(result.offsets, offset)
		result.data = append(result.data, k...)
	}
	result.offsets = append(result.offsets, uint32(len(result.data)))
	*b = Builder{}
	return result
}

// Match expects a normalized lower-case DNS name, as the DNS parser supplies.
// It performs no allocation and respects label boundaries: example.com never
// matches notexample.com. Exact names are not inherited by subdomains.
func (m Index) Match(name string) bool {
	var stack [256]byte
	reversed := stack[:0]
	if len(name) > len(stack) {
		reversed = make([]byte, 0, len(name))
	}
	for i := len(name); i > 0; i-- {
		reversed = append(reversed, name[i-1])
	}
	complete := true
	for {
		if at, ok := m.find(reversed); ok && (complete || m.offsets[at]&suffixFlag != 0) {
			return true
		}
		end := bytes.LastIndexByte(reversed, '.')
		if end <= 0 {
			return false
		}
		reversed, complete = reversed[:end], false
	}
}

func (m Index) find(name []byte) (int, bool) {
	lo, hi := 0, len(m.offsets)-1
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if m.compare(mid, name) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, lo < len(m.offsets)-1 && m.compare(lo, name) == 0
}

func (m Index) compare(at int, name []byte) int {
	key := m.data[m.offsets[at]&offsetMask : m.offsets[at+1]&offsetMask]
	return bytes.Compare(key, name)
}

// Bytes reports the retained arena and index capacity, excluding slice headers.
func (m Index) Bytes() int { return cap(m.data) + 4*cap(m.offsets) }
