package agentport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// One immutable inventory per controller generation, shared by probe lanes.
// Handler ownership, selection and cleanup remain private to each lane.
type healthPoolCache struct {
	mu          sync.Mutex
	path        string
	info        os.FileInfo
	changeStamp string
	snapshot    healthPoolSnapshot
}
type healthPoolSnapshot struct {
	pool          healthPool
	signature     string
	fileSignature string
	mtime         int64
}

func (cache *healthPoolCache) load(path string) (healthPoolSnapshot, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return healthPoolSnapshot{}, err
	}
	stamp := fmt.Sprintf("%d|%d", info.ModTime().UnixNano(), info.Size())
	if cache.path == path && cache.snapshot.fileSignature == stamp && os.SameFile(cache.info, info) && cache.changeStamp == healthPoolChangeStamp(info) {
		return cache.snapshot, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return healthPoolSnapshot{}, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return healthPoolSnapshot{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return healthPoolSnapshot{}, errors.New("invalid health pool file or size")
	}
	body, err := io.ReadAll(io.LimitReader(file, (32<<20)+1))
	if err != nil {
		return healthPoolSnapshot{}, err
	}
	if len(body) > 32<<20 {
		return healthPoolSnapshot{}, errors.New("oversized health pool")
	}
	sum := sha256.Sum256(body)
	signature := hex.EncodeToString(sum[:])
	snapshot := cache.snapshot
	if cache.path != path || signature != snapshot.signature {
		var pool healthPool
		if err := json.Unmarshal(body, &pool); err != nil {
			return healthPoolSnapshot{}, err
		}
		if pool.Version < 3 {
			return healthPoolSnapshot{}, errors.New("health pool contract predates the 1.6.15 release baseline")
		}
		for id, contract := range pool.HealthPolicies {
			contract.Policy.LatencyMeasurement = "gstatic-head-v1"
			pool.HealthPolicies[id] = contract
		}
		snapshot.pool = pool
		snapshot.signature = signature
	}
	snapshot.fileSignature = fmt.Sprintf("%d|%d", info.ModTime().UnixNano(), info.Size())
	snapshot.mtime = info.ModTime().UnixNano()
	cache.path, cache.snapshot = path, snapshot
	cache.info, cache.changeStamp = info, healthPoolChangeStamp(info)
	return snapshot, nil
}
