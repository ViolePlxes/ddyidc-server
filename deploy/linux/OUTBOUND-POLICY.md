# Supplier HTTP / remote console connection policy

`Outbound` in the root YAML config controls supplier HTTP and VNC websocket
destinations. Defaults allow public unicast addresses on ports 80 and 443 only.
The transport ignores environment proxies. TLS continues validating the original
hostname; resolving and dialing a validated IP does not disable certificate checks.

```yaml
Outbound:
  Ports: [80, 443]
  Hosts: []
  PrivateCIDRs: []
```

If an isolated development proxy synthesizes `198.18.0.0/15` DNS answers, those
answers are still denied. Configure an authenticated DNS-over-TLS resolver instead
of allowing the reserved range or disabling TLS validation:

```yaml
  DNSTLSAddress: "1.1.1.1:853"
  DNSTLSServerName: "one.one.one.one"
```

This optional resolver requires a public literal IP, port 853, system CA validation
and the explicit TLS server name; it does not fall back to plaintext after failure.
It only changes this application's supplier/VNC resolver, not the operating
system DNS. Official protocol reference: https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-tls/

Empty Hosts means any public destination, not private access. A nonempty Hosts
list is an exact hostname/IP allowlist, without wildcard or URL syntax. Operators
must include both configured supplier API names and documented VNC endpoint names.
For a supplier using a nonstandard VNC port, add that specific port after verifying
its actual endpoint. Never infer a port or silently allow all ports in production.

An intentional private supplier needs an explicit narrow RFC1918/ULA CIDR (or
loopback for a local service), plus its port. Broad prefixes extending beyond the
private ranges are rejected. Link-local/metadata, multicast, unspecified, reserved
and documentation addresses stay forbidden even if explicitly configured.

Every new connection resolves once, rejects the entire result when any answer is
forbidden, and dials only one of the validated IP literals. HTTP redirects allow
at most three same-origin read-only hops; cross-origin redirects, HTTPS downgrade,
URL credentials and redirects of write operations are rejected. Credentials are
never forwarded to the redirected origin. Websocket connections use the same
address and DNS gate and do not follow redirects.

Supplier HTTP has 16 connections per host, a 15-second response-header deadline,
1 MiB header limit and existing total request deadlines. ZJMF responses exceeding
20 MiB are rejected rather than silently truncated. Existing bounded GET retries
remain; POST writes are not automatically retried.

Tests use isolated loopback servers, fake DNS/dial functions and explicit test-only
loopback/ephemeral-port exceptions in the test binaries. They do not probe cloud
metadata or private network services. A real supplier readonly call and an actual
supported VNC session are separate acceptance checks; isolated tests do not prove
either external integration.
