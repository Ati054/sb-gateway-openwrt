package outbound

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/session"
)

// A failed dial to a resproxy-managed proxy endpoint is only a hint to check
// the selected route early. It is not a health verdict and never switches a
// route inside Xray. The daemon's private Unix datagram socket is optional;
// a stock standalone Xray keeps working if the receiver is absent.
var passiveSocketPath = os.Getenv("RESPROXY_HEALTH_SOCKET")
var passiveLastSent atomic.Int64

func reportPassiveDialFailure(ctx context.Context, tag string) {
	if passiveSocketPath == "" || !strings.HasPrefix(tag, "resproxy-") || len(tag) > 128 {
		return
	}
	inbound := session.InboundFromContext(ctx)
	if inbound == nil || (inbound.Tag != "resproxy-transparent" &&
		inbound.Tag != "resproxy-transparent6" && inbound.Tag != "resproxy-http") {
		return
	}
	now := time.Now().UnixNano()
	for {
		previous := passiveLastSent.Load()
		if now-previous < int64(250*time.Millisecond) {
			return
		}
		if passiveLastSent.CompareAndSwap(previous, now) {
			break
		}
	}
	connection, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: passiveSocketPath, Net: "unixgram"})
	if err != nil {
		return
	}
	defer connection.Close()
	_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	_, _ = connection.Write([]byte(strconv.Itoa(os.Getpid()) + "\tdial\t" + tag))
}
