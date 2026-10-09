# Embedded Xray source

Pinned upstream: XTLS/Xray-core 26.9.30, commit
b26a91de4f3294e26a0ad0a970b81a386a41f789. Licensed under MPL-2.0;
see LICENSE. The complete source is retained here for reproducible builds.

The source includes the existing gateway patches for passive failure signals,
Vision/xhttp/TLS behavior and atomic routing publication with current balancer
overrides. The integrated release additionally passes RPC cancellation into
routing compilation and checks it before publishing the replacement table.
`resproxy-upstream.patch` records all changes against the pinned commit.

The command entry points in internal/xraycli are adapted from upstream main,
run and version. Their original copyright/license remain MPL-2.0. The gateway
entry point uses this CLI only when invoked through the xray compatibility
symlink. Normal gateway operation uses core.StartInstance and a persistent
local API client in the same process.

Go module dependencies are pinned by go.mod/go.sum and included in vendor.
The independent Xray binary in testlab is a test reference and is not installed.
