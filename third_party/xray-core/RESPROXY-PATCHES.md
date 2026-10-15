# Embedded Xray source

Pinned upstream: XTLS/Xray-core main as of 2026-10-14, commit
0888beb38a9ba45440557175e71ce21cf8ba3b13. Licensed under MPL-2.0;
see LICENSE. The complete source is retained here for reproducible builds.

The source includes the existing gateway patches for passive failure signals,
Vision/xhttp/TLS behavior and atomic routing publication with current balancer
overrides. The integrated release additionally passes RPC cancellation into
routing compilation and checks it before publishing the replacement table.
`resproxy-upstream.patch` records the initial changes against the previous upstream commit
b26a91de4f3294e26a0ad0a970b81a386a41f789. `resproxy-current.patch`
records the complete gateway delta against the current pinned commit.
The full delta includes the TCP sniffing correction, health-probe cleanup,
client-session counters and shared Linux domain matchers. The smaller patches
are retained as historical change records; do not apply them again on top.

The command entry points in internal/xraycli are adapted from upstream main,
run and version. Their original copyright/license remain MPL-2.0. The gateway
entry point uses this CLI only when invoked through the xray compatibility
symlink. Normal gateway operation runs this executable as a separate core process
and uses a persistent local API client.

Go module dependencies are pinned by go.mod/go.sum and included in vendor.
The independent Xray binary in testlab is a test reference and is not installed.

Route-only TCP sniffing allows five seconds for the initial HTTP/TLS data. This
prevents a delayed ClientHello from missing domain rules under CPU contention.
UDP and destination-replacement sniffing retain the upstream 200 ms budget.

`resproxy-health-probe-close.patch` bounds internal health and refresh CONNECT
requests to eight seconds, including blocked upstream TLS/REALITY handshakes.
Probe failures release raw sockets and skip REALITY camouflage browsing after
invalid authentication. Normal client streams and their camouflage behavior
remain unchanged. Failed ordinary TCP TLS handshakes also release their socket.
