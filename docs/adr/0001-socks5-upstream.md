# 0001: SOCKS5 as upstream proxy protocol

**Date:** 2026-06-30
**Status:** Accepted

## Context

RandProxy needs to forward HTTPS requests from Downstream clients to the Target
(`api.literouter.com:443`). The forwarding goes through a pool of free proxies
fetched from public sources. The proxy server exposes an HTTP CONNECT
interface for Downstream clients (curl, browsers, etc.).

The question was: which protocol to use for the **Upstream** connection (from
RandProxy's proxy server to the free proxies in the pool)?

Two options existed:

1. **HTTP CONNECT upstream** — The proxy server receives an HTTP CONNECT
   from the client, then issues another HTTP CONNECT to the free proxy.
   Both legs speak the same protocol.
2. **SOCKS5 upstream** — The proxy server receives HTTP CONNECT from the
   client, then establishes a SOCKS5 tunnel through the free proxy to the
   Target. Two different protocols bridged internally.

HTTP CONNECT is the default assumption for an HTTP proxy, requires no
extra dependencies, and is simpler to reason about.

## Decision

Use **SOCKS5** for upstream proxy connections.

## Consequences

### Positive

- **74% pass rate on real HTTPS traffic** — tested across 5 free proxy
  sources and 13 rounds. SOCKS5 proxies from 66daili reliably complete
  TLS handshakes to the target. HTTP CONNECT proxies from every tested
  source (scdn.io, goodips, 89ip, ihuan) fail at TLS handshake with
  `SSLEOFError` — they accept the CONNECT but cannot complete the
  tunnel.
- **Lower overhead** — SOCKS5 is a lighter handshake than HTTP CONNECT
  (3 bytes → 2 bytes vs. full HTTP header exchange), though the
  difference is marginal in practice.

### Negative

- **Mixed-protocol architecture** — the proxy server bridges HTTP
  CONNECT (downstream) and SOCKS5 (upstream). Debugging requires
  understanding both protocols.
- **Dependency on `golang.org/x/net/proxy`** — adds one external Go
  module. HTTP CONNECT can be implemented with stdlib alone.
- **Only 66daili works** — no other free source provides reliable
  SOCKS5 proxies. If 66daili goes down, the pool dries up until another
  SOCKS5-capable source is added.

## Alternatives Considered

### HTTP CONNECT upstream (rejected)
Empirically proven non-viable with free proxy sources. Every HTTP proxy
tested across 5 sources failed HTTPS CONNECT tunneling. The proxies
return `200 Connection established` but drop the connection during TLS
ClientHello. Would only be viable if the Target had an HTTP (non-TLS)
endpoint.

### Direct connection (no upstream proxy, rejected)
Does not solve the original problem — the 30-request-per-IP limit on
`api.literouter.com` requires IP rotation through proxies.