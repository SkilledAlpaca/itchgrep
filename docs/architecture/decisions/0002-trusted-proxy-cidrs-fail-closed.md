# 0002: `CF-Connecting-IP` trusted only from `TRUSTED_PROXY_CIDRS`, and fails closed

Status: Accepted (implemented)

## Context

The webserver identifies a client (for rate limiting and for the access log) either from the raw TCP connection's remote address, or — when `TRUST_PROXY_HEADERS=true` — from the `CF-Connecting-IP` or `X-Forwarded-For` header Cloudflare sets. Those headers are, by themselves, just request content: any client that can reach the origin at all can set them to anything. Trusting them unconditionally the moment `TRUST_PROXY_HEADERS` is on would let a single visitor mint a fresh claimed identity on every request, which defeats both the rate limiter (a new bucket every time) and the access log (an unaccountable stream of claimed IPs).

The nuance is that this project runs in two shapes: a plain local `docker compose up` with no Cloudflare in front of it at all (where the header must never be trusted), and the production deployment behind Cloudflare Tunnel (where it must be, since every real request there does legitimately arrive with a Cloudflare-set header). One codebase has to be safe in both.

## Decision

`internal/web/clientip.go`'s `ClientIP` only honors `CF-Connecting-IP`/`X-Forwarded-For` when **both** conditions hold: `TRUST_PROXY_HEADERS` is true, **and** the request's own socket address (`r.RemoteAddr`, which a header can never spoof) falls inside one of the networks named by `TRUSTED_PROXY_CIDRS`. `TrustedProxyCIDRs` is empty by default — nothing is trusted until an operator explicitly names a network — and if `TRUST_PROXY_HEADERS` is true while `TRUSTED_PROXY_CIDRS` names no valid network, that is logged as a warning rather than silently trusting the header from anywhere: **turning header trust on without configuring who it's extended to does nothing, rather than the dangerous thing.**

In `docker-compose.yml`, the compose network's subnet is pinned explicitly (`networks.default.ipam.config`, `172.30.99.0/24`) and `cloudflared` is pinned to `172.30.99.10` inside it, so `TRUSTED_PROXY_CIDRS` can default to that single address, `172.30.99.10/32` — the one peer `cloudflared` actually connects from when the `public` profile is in use. The whole `/24` is deliberately not trusted: Docker's userland proxy can deliver LAN traffic to the published port from the bridge gateway, which sits inside it.

## Alternatives considered

- **Trust the header whenever `TRUST_PROXY_HEADERS` is true, with no peer check.** Simpler, but means the moment an operator turns this on for the production deployment, the same binary run locally with `docker compose up` (no Cloudflare in front of it, if the flag were left on by habit) would trust an arbitrary client-supplied identity. Fail-open is the wrong default for a feature whose entire purpose is deciding who to believe.
- **Always derive identity from the raw socket address, never the header.** This is the correct behavior with no proxy in front, but behind Cloudflare Tunnel it would mean every visitor is seen as the same address (the tunnel's), collapsing the rate limiter into one shared bucket for the entire internet and making the access log useless for distinguishing traffic.

## Consequences

- `TRUSTED_PROXY_CIDRS` must be configured correctly for the deployment topology, or header trust silently does nothing (logged, not fatal) rather than degrading into something exploitable. See [configuration.md](../../configuration.md) for the variable's default and how to set it.
- The rate limiter (`internal/web/ratelimit.go`), the access logger (`internal/web/logmiddleware.go`), and the probe filter (`internal/web/probes.go`) all read `TrustProxyHeaders()`/`TrustedProxyCIDRs()` — both `sync.OnceValue`-cached reads of the same environment variables — specifically so the three can never independently disagree about where a client's identity comes from.
- If the network topology changes (a different reverse proxy, a different compose network layout), `TRUSTED_PROXY_CIDRS` has to change with it, or requests will either be wrongly attributed to the proxy's own address (if left too narrow) or, if broadened carelessly, become spoofable again.
