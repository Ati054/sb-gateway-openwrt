# Source attribution

The gateway includes code adapted from SB Gateway under GNU AGPL version 3.
Source notices in the adapted files identify their origin and version. The
complete license is in LICENSE. Local modifications implement OpenWrt lifecycle,
transparent routing, configuration, bounded RAM history and the LuCI interface.

The embedded Xray source is licensed under MPL-2.0 and pinned to XTLS/Xray-core
commit 0888beb38a9ba45440557175e71ce21cf8ba3b13.
See third_party/xray-core/LICENSE and third_party/xray-core/RESPROXY-PATCHES.md.
The complete source and local patch are included. Command entry points adapted
from Xray retain their MPL-2.0 notices. Upstream source:
https://github.com/XTLS/Xray-core/tree/0888beb38a9ba45440557175e71ce21cf8ba3b13.

Country category seeds derive from v2fly/domain-list-community commit
2d71342eea1720da57261b55663c9d55619e9b1c under MIT. See
files/domain-list-community.LICENSE. Runtime updates use its public rule data.
Source: https://github.com/v2fly/domain-list-community.

Pinned dependencies and their license notices are included in vendor.
Copies of dependency licenses installed on the router are in
files/go-dependency-licenses. Module versions are recorded in go.mod/go.sum
and vendor/modules.txt. Preserve these licenses when distributing this package.
