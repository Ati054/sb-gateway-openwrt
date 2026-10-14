package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"time"

	"github.com/Ati054/sb-gateway-openwrt/internal/settings"
)

func refreshSignature(sources []settings.Subscription, id string) (string, error) {
	selected := sources
	if id != "*" {
		selected = nil
		for _, source := range sources {
			if source.ID == id {
				selected = []settings.Subscription{source}
				break
			}
		}
		if len(selected) == 0 {
			return "", errors.New("unknown subscription source")
		}
	}
	body, err := json.Marshal(selected)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:]), nil
}

func refreshAcceptance(sources []settings.Subscription, id, signature string) reloadResult {
	current, err := refreshSignature(sources, id)
	if err != nil || signature == "" || current != signature {
		return reloadResult{Status: "pending"}
	}
	return reloadResult{Status: "applied"}
}

// Wait for the daemon to accept the committed source settings. A request sent
// while the previous instance is stopping must not disappear during startup.
func RequestRefresh(ctx context.Context, paths Paths, sources []settings.Subscription, id string) error {
	signature, err := refreshSignature(sources, id)
	if err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return errors.New("subscription service did not become ready")
		}
		result, err := sendRefresh(ctx, paths, id, signature)
		if err == nil && result.Status == "applied" {
			return nil
		}
		if err == nil && result.Status != "pending" {
			return errors.New(result.Error)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("subscription service did not become ready")
		case <-timer.C:
		}
	}
}

func sendRefresh(ctx context.Context, paths Paths, id, signature string) (reloadResult, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(paths.RunRoot, "reload.sock"))
	if err != nil {
		return reloadResult{}, err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(25 * time.Second)
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return reloadResult{}, err
	}
	message := struct {
		Operation string    `json:"operation"`
		Source    string    `json:"source"`
		Signature string    `json:"signature"`
		Deadline  time.Time `json:"deadline"`
	}{"refresh", id, signature, deadline}
	if err = json.NewEncoder(conn).Encode(message); err != nil {
		return reloadResult{}, err
	}
	var result reloadResult
	err = json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&result)
	return result, err
}
