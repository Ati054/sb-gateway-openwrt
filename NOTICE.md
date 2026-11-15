# Source attribution

XRoute for OpenWrt includes code adapted from SB Gateway under GNU AGPL version 3.
Source notices in the adapted files identify their origin and version. The
complete license is in LICENSE. Local modifications implement OpenWrt lifecycle,
transparent routing, configuration, bounded RAM history and the LuCI interface.

The indexed subscription catalogue and storage-space checks in
internal/subscriptioncatalog and internal/storagebudget are adapted from the
MikroTik edition of SB Gateway under the same AGPL-3.0 license. OpenWrt adds
streaming import, legacy-generation reading and lazy health-pool integration.

The URLTest cadence history is adapted from SB Gateway v1.6.94 under the same
AGPL-3.0 license, with OpenWrt timestamp, monitoring and lifecycle integration.

The embedded Xray source is licensed under MPL-2.0 and pinned to XTLS/Xray-core
commit 676eaf66f8f05d4d558ee7a74b4736623d3feeb9.
See third_party/xray-core/LICENSE and third_party/xray-core/RESPROXY-PATCHES.md.
The complete source and local patch are included. Command entry points adapted
from Xray retain their MPL-2.0 notices. Upstream source:
https://github.com/XTLS/Xray-core/tree/676eaf66f8f05d4d558ee7a74b4736623d3feeb9.

Country category seeds derive from v2fly/domain-list-community commit
2d71342eea1720da57261b55663c9d55619e9b1c under MIT. See
files/domain-list-community.LICENSE. Runtime updates use its public rule data.
Source: https://github.com/v2fly/domain-list-community.

Pinned dependencies and their license notices are included in vendor.
Copies of dependency licenses installed on the router are in
files/go-dependency-licenses. Module versions are recorded in go.mod/go.sum
and vendor/modules.txt. Preserve these licenses when distributing this package.
