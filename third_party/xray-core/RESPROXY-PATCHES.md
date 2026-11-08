# Embedded Xray source

Pinned upstream: XTLS/Xray-core v26.10.31, commit
a4a3dff5b20c2faae567b742c172c7b95e75c083. Licensed under MPL-2.0;
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
After changing this tree, run `python3 tools/check-xray-vendor.py --sync` from
the repository root. CI and SDK builds check the vendored copy before compiling.
The independent Xray binary in testlab is a test reference and is not installed.

Route-only TCP sniffing allows five seconds for the initial HTTP/TLS data. This
prevents a delayed ClientHello from missing domain rules under CPU contention.
UDP and destination-replacement sniffing retain the upstream 200 ms budget.

`resproxy-health-probe-close.patch` bounds internal health and refresh CONNECT
requests to eight seconds, including blocked upstream TLS/REALITY handshakes.
Probe failures release raw sockets and skip REALITY camouflage browsing after
invalid authentication. Normal client streams and their camouflage behavior
remain unchanged. Failed ordinary TCP TLS handshakes also release their socket.

Managed external port-53 listeners classify DNS before handing it to the local
backend. Other TCP/UDP protocols retain their original destination and normal
client routing. The loopback DNS relay uses bounded PROXY-v2 address metadata
to preserve client policy. Each managed UDP DNS datagram is dispatched separately
and releases its relay sockets after the reply, including requests sharing a
client source port. TCP supports pipelined requests with a ten-second idle
timeout, leaving room for the backend's five-second deadline and error reply.
Ordinary transparent listeners keep their existing classifiers and flow cache.

Partial managed TCP DNS length/header prefixes use the existing shared sniffing
deadline without consuming the two undecided read attempts. Other classifiers
remain available until a complete DNS header identifies the protocol.

Managed DNS carries fixed, local PROXY-v2 provenance metadata. It checks accepted
UDP-buffer or parsed TCP-frame bytes across relay copying; it does not claim to
verify raw TCP reassembly or kernel UDP truncation flags. Digests stay transient.
SB Gateway packages enable provenance by default. The fixed diagnostic ring is
bounded, root-only, and published in runtime storage without names or payloads.
Build-time and environment overrides remain available for controlled comparisons;
the generic Xray source default keeps the legacy relay envelope without hashing.
