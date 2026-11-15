# DNS diagnostics

[Back to setup](../README.md#setup) · [Русский](DNS-DIAGNOSTICS.md)

The **Errors** counter in LuCI covers failed DNS resolution and DNS routing. Input rejections have a separate counter. For example, a query with two questions or an incomplete section receives a format error. These events do not mean that the connection to the DNS provider has failed.

## Data for a problem report

Run these commands in the router's SSH console:

```sh
resproxyd version
cat /var/run/resproxy/dns-status.json
cat /var/run/resproxy/dns-upstream-errors.json
```

Mention whether the errors appeared after startup or continue to increase during normal use. To investigate input rejections, add:

```sh
jsonfilter -i /var/run/resproxy/dns-rejected.json -e '@.recent_samples'
```

These files contain counters, error causes, client addresses, and numeric header fields. They do not contain domains, subscription links, or packet contents. You can redact local IP addresses before posting them.

## Counters

`dns-status.json` contains totals for queries, errors, and input rejections. In `dns-upstream-errors.json`, the `proxy` and `wan` sections describe failed resolver exchanges for each path.

| Field | Meaning |
|---|---|
| `dns_failures` | Client queries with DNS or routing failures |
| `failure_invalid_query` | Rejected input packets |
| `failed_exchanges` | Failed requests to a resolver |
| `recent_samples` in `dns-upstream-errors.json` | The last eight failed exchanges for each path |
| `questionless_failed_exchanges` | Failed exchanges without a DNS question; included in the total |
| `unsupported_opcode` | Requests with an unsupported operation that received a local response |

Several identical client queries can share one resolver exchange, so client failure counts can differ from `failed_exchanges`. A local `NOTIMP` response to the obsolete IQUERY operation counts toward `unsupported_opcode`.

If XRoute cannot read WAN status, it retries with increasing delays. Retries stop after a successful read. A confirmed WAN disconnection prevents use of the old resolver.
