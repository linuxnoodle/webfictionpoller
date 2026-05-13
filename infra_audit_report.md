# Infrastructure Security Audit Report - demonstrated.dev

**Date:** 2026-05-12  
**Auditor:** Authorized infrastructure-level audit  
**Scope:** wp.demonstrated.dev, mc.demonstrated.dev, demonstrated.dev (apex)  
**Tools:** nmap 7.99, gobuster 3.8.2, nikto 2.6.0, sqlmap 1.10.4, curl, dig, whatweb  

---

## Executive Summary

The audit identified **2 critical**, **3 medium**, and **5 low/informational** findings. The most urgent issues are an **open recursive DNS resolver** on mc.demonstrated.dev (abusable for DDoS amplification) and an **exposed copyparty WebDAV service** on the previously-unknown subdomain cp.demonstrated.dev. The Go web application at wp.demonstrated.dev is well-hardened at the application layer; remaining issues are infrastructure-level.

---

## 1. Open Ports and Services

### wp.demonstrated.dev (Cloudflare-proxied: 104.21.72.178, 172.67.153.87)

| Port | State | Service | Notes |
|------|-------|---------|-------|
| 80/tcp | open | HTTP | 301 redirect to HTTPS |
| 443/tcp | open | HTTPS | Cloudflare edge, Go backend |

Only standard web ports exposed. Cloudflare masks the origin IP effectively.

### mc.demonstrated.dev (Direct: 99.53.73.216, no Cloudflare)

| Port | State | Service | Version | Notes |
|------|-------|---------|---------|-------|
| 53/tcp | **open** | DNS | dnsmasq 2.89 | **OPEN RECURSIVE RESOLVER** |
| 80/tcp | open/filtered | HTTP | tcpwrapped | Accepts TCP but no valid HTTP response |
| 443/tcp | open/filtered | HTTPS | tcpwrapped | Accepts TCP but SSL handshake fails |
| 25565/tcp | **open** | Minecraft | tcpwrapped | Game server port |
| 111/tcp | filtered | rpcbind | — | Firewalled |
| 7547/tcp | filtered | CWMP | — | Firewalled |
| 51001-61001 | filtered | unknown | — | Multiple filtered high ports |

### cp.demonstrated.dev (Cloudflare-proxied: same IPs as wp)

| Port | State | Service | Notes |
|------|-------|---------|-------|
| 80/tcp | open | HTTP | Cloudflare proxy |
| 443/tcp | open | HTTPS | Cloudflare proxy |
| 8080/tcp | open | HTTP | Cloudflare proxy (additional) |
| 8443/tcp | open | HTTPS | Cloudflare proxy (additional) |

### demonstrated.dev (apex)

- **No A/AAAA record.** Apex domain does not resolve. Only subdomains (wp, mc, cp) are configured.

---

## 2. Critical Findings

### CRITICAL-01: Open Recursive DNS Resolver on mc.demonstrated.dev

- **Host:** mc.demonstrated.dev (99.53.73.216)
- **Port:** 53/tcp (dnsmasq 2.89)
- **Detail:** The DNS resolver answers recursive queries for arbitrary external domains (confirmed: `dig @99.53.73.216 google.com` returns full resolution). This makes the server usable as a **DNS amplification attack reflector** for DDoS attacks.
- **Version Disclosure:** `dnsmasq-2.89` is leaked via `dns-nsid` query.
- **Recommendation:** Restrict dnsmasq to listen only on localhost or internal interfaces. Add `listen-address=127.0.0.1` to dnsmasq.conf. Block port 53 at the firewall for external access. If DNS is needed for the Minecraft server, use `--bind-interfaces` with an internal-only bind address.

### CRITICAL-02: Exposed WebDAV Service on cp.demonstrated.dev

- **Host:** cp.demonstrated.dev (Cloudflare-proxied)
- **Service:** copyparty (file sharing) - title: "copyparty @ pve" (implies Proxmox VE host)
- **WebDAV Enabled:** Full WebDAV 1,2 with methods: `GET, HEAD, POST, PUT, DELETE, OPTIONS, PROPFIND, PROPPATCH, LOCK, UNLOCK, MKCOL, COPY, MOVE`
- **Authentication:** PROPFIND returns 401 with Basic auth prompt (realm "a"). Unauthenticated GET returns `howdy stranger (you're not logged in)`.
- **Risk:** If authentication is bypassed or credentials are weak, an attacker could upload/delete files via WebDAV. The service also exposes 4 Cloudflare-proxied ports (80, 443, 8080, 8443).
- **Leaked Info:** `X-Served-By: cp.demonstrated.dev` header; `Server: cloudflare` with copyparty underneath.
- **Recommendation:** If cp.demonstrated.dev is not intended to be public, restrict it at the Cloudflare level (Zero Trust / Access policy). Review copyparty authentication configuration. Consider disabling WebDAV methods if not needed. Restrict Cloudflare to only proxy necessary ports.

---

## 3. Medium Findings

### MED-01: TLS 1.0 and TLS 1.1 Still Enabled (wp.demonstrated.dev)

- **Detail:** Cloudflare edge accepts TLS 1.0 and TLS 1.1 connections with CBC cipher suites:
  - TLS 1.0/1.1: `TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA`, `TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA`
- **Risk:** These legacy protocols have known vulnerabilities (BEAST, POODLE variants) and are deprecated per RFC 8996 (TLS 1.0/1.1) and PCI DSS.
- **All cipher suites are rated A** individually, but the protocol versions themselves are outdated.
- **TLS 1.2 and 1.3** are properly configured with strong cipher suites (AES-GCM, CHACHA20-POLY1305).
- **TLS 1.3** uses post-quantum key exchange (X25519MLKEM768) - excellent.
- **Recommendation:** In Cloudflare dashboard, set minimum TLS version to 1.2 under SSL/TLS > Edge Certificates.

### MED-02: CSRF Cookie Missing Secure and HttpOnly Flags

- **Host:** wp.demonstrated.dev
- **Detail:** The `csrf_token` cookie is set as: `Set-Cookie: csrf_token=<value>; Max-Age=31536000`
- **Missing:** `Secure` flag (cookie transmitted over unencrypted connections if HTTP is accessed), `HttpOnly` flag (cookie accessible to JavaScript), `SameSite` attribute.
- **Impact:** Without `Secure`, the cookie could be leaked over HTTP. Without `SameSite`, the cookie is sent with cross-site requests.
- **Note:** The site forces HTTPS via 301/HSTS, which mitigates the Secure flag issue in practice.
- **Recommendation:** Set cookie with `Secure; HttpOnly; SameSite=Lax` attributes. In Go: use `http.Cookie` with these fields set.

### MED-03: No DMARC or DKIM Email Authentication Records

- **Domain:** demonstrated.dev
- **Detail:** 
  - No MX records (no inbound mail)
  - No DMARC record at `_dmarc.demonstrated.dev`
  - No DKIM record at `default._domainkey.demonstrated.dev`
  - No SPF record in TXT
- **Risk:** Without these records, attackers can spoof `@demonstrated.dev` email addresses. Even if you don't send email, a DMARC `v=DMARC1; p=reject;` record prevents spoofing.
- **Recommendation:** Add at minimum: `TXT "_dmarc.demonstrated.dev" "v=DMARC1; p=reject; rua=mailto:admin@demonstrated.dev"` and an SPF `v=spf1 -all` TXT record on the apex domain.

---

## 4. Low / Informational Findings

### LOW-01: X-Served-By Header Leaks Internal Hostnames

- **Affected:** wp.demonstrated.dev (`X-Served-By: wp.demonstrated.dev`), cp.demonstrated.dev (`X-Served-By: cp.demonstrated.dev`)
- **Risk:** Reveals internal service naming/hostname structure.
- **Recommendation:** Remove or genericize the `X-Served-By` header in the Go application.

### LOW-02: rDNS Record Leaks ISP Information for mc.demonstrated.dev

- **IP:** 99.53.73.216
- **rDNS:** `99-53-73-216.lightspeed.lsvlky.sbcglobal.net`
- **Detail:** Reveals the ISP (AT&T/SBCGlobal, residential/business connection type "Lightspeed"), geographic region (Louisville, KY - "lsvlky"), and the full IP address in the hostname.
- **Risk:** Helps attackers profile the infrastructure as a residential/small-business connection, identifies the ISP for social engineering.
- **Recommendation:** Contact AT&T to set a generic rDNS entry, or use a VPS with neutral rDNS for public-facing services.

### LOW-03: No security.txt

- **URL:** `https://wp.demonstrated.dev/.well-known/security.txt` returns 404.
- **Recommendation:** Create a security.txt per RFC 9116 to provide vulnerability disclosure contact information.

### LOW-04: robots.txt Contains Only AI Content Signals

- **URL:** `https://wp.demonstrated.dev/robots.txt` contains content signals for AI training (search, ai-input, ai-train) but no traditional `User-agent` / `Disallow` directives.
- **Detail:** No sensitive paths are hidden from crawlers via robots.txt, but this is acceptable since directory enumeration found minimal surface (only /login, /logout, /setup, /robots.txt).

### LOW-05: dnsmasq Version Disclosure on mc.demonstrated.dev

- **Port:** 53/tcp
- **Version:** dnsmasq 2.89
- **Risk:** Exact version disclosure enables targeted exploit searches.
- **Recommendation:** Add `bogus-priv` and disable version reporting if possible with dnsmasq configuration.

---

## 5. TLS/SSL Cipher Analysis (wp.demonstrated.dev)

| Protocol | Cipher Suites | Rating | Notes |
|----------|---------------|--------|-------|
| TLS 1.0 | AES-128-CBC-SHA, AES-256-CBC-SHA | A (per-cipher) | **Deprecated protocol** |
| TLS 1.1 | AES-128-CBC-SHA, AES-256-CBC-SHA | A (per-cipher) | **Deprecated protocol** |
| TLS 1.2 | AES-128/256-CBC/GCM, CHACHA20-POLY1305 | A | Strong, well-configured |
| TLS 1.3 | AES-128/256-GCM, CHACHA20-POLY1305 | A | With post-quantum X25519MLKEM768 |

- **Certificate:** Issued by Google Trust Services (WE1), CN=demonstrated.dev, SAN includes `*.demonstrated.dev`
- **Valid:** 2026-03-15 to 2026-06-13 (90-day Let's Encrypt-style cert via Cloudflare)
- **Cipher preference:** Server-selected for TLS 1.0-1.1, client-selected for TLS 1.2-1.3 (Cloudflare default)
- **Overall grade:** A with caveat of TLS 1.0/1.1 enabled

---

## 6. DNS and Subdomain Exposure

### Active Subdomains

| Subdomain | IP(s) | Via Cloudflare | Service |
|-----------|-------|----------------|---------|
| wp.demonstrated.dev | 104.21.72.178, 172.67.153.87 | Yes | Go web app (WebFiction Tracker) |
| mc.demonstrated.dev | 99.53.73.216 | **No** | Minecraft server + dnsmasq |
| cp.demonstrated.dev | 104.21.72.178, 172.67.153.87 | Yes | copyparty file sharing (WebDAV) |
| demonstrated.dev (apex) | No A/AAAA record | N/A | Does not resolve |

### DNS Records

| Type | Value |
|------|-------|
| NS | joselyn.ns.cloudflare.com, sullivan.ns.cloudflare.com |
| SOA | joselyn.ns.cloudflare.com / dns.cloudflare.com (serial: 2403940890) |
| TXT | "RFC8482" (refuses ANY queries per RFC 8482) |
| MX | None |
| DMARC | None |
| DKIM | None |
| SPF | None |

### Subdomain Enumeration Results

Gobuster DNS with common.txt wordlist found **3 subdomains**: wp, mc, cp. No other subdomains were discovered with this wordlist.

---

## 7. Web Server Configuration Analysis (wp.demonstrated.dev)

### Security Headers Present

| Header | Value | Assessment |
|--------|-------|------------|
| Strict-Transport-Security | `max-age=31536000; includeSubDomains` | Good (1 year, includes subdomains) |
| X-Frame-Options | `DENY` | Good |
| X-Content-Type-Options | `nosniff` | Good |
| Content-Security-Policy | `default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'` | Present (note: `unsafe-inline` and `unsafe-eval` in script-src weaken CSP) |
| Referrer-Policy | `strict-origin-when-cross-origin` | Good |

### Missing Headers

| Header | Status | Impact |
|--------|--------|--------|
| Permissions-Policy | Not set | Browser features (camera, mic, geolocation) not explicitly restricted |
| Cross-Origin-Opener-Policy | Not set | Not critical for non-API site |
| Cross-Origin-Resource-Policy | Not set | Not critical |

### HTTP Methods

| Endpoint | Allowed Methods | Assessment |
|----------|----------------|------------|
| / (root) | GET only | Good - 303 redirect to /login |
| /login | GET, POST | Appropriate |
| /api/version | GET only | Good - returns 405 for other methods |
| /logout | Returns 405 for GET | Proper (POST-only expected) |
| /static/* | GET, with caching | Cache-Control: max-age=28491 (~8 hours) |

### Nikto Findings Summary

- Cloudflare detected (limits Nikto effectiveness)
- Cookie csrf_token missing Secure and HttpOnly flags
- Scan terminated early due to Cloudflare TLS fingerprinting (20 SSL errors)
- No CGI directories found
- Server: cloudflare (origin hidden)

### SQLMap Results

- **No injection vulnerabilities found** at /login
- Crawler found no additional forms to test at depth 1
- Parameterized queries confirmed effective

### Directory Enumeration Results

Only 4 endpoints discovered:

| Path | Status | Notes |
|------|--------|-------|
| /login | 200 | Login page (HTML5, password field) |
| /logout | 405 | POST-only endpoint |
| /setup | 303 | Redirects to /login |
| /robots.txt | 200 | Content signals only |

Minimal attack surface. No exposed admin panels, debug endpoints, or config files.

---

## 8. Cloudflare Bypass Assessment

### mc.demonstrated.dev (Direct-exposed, NO Cloudflare)

- **Origin IP:** 99.53.73.216 (publicly visible in DNS)
- **Not behind Cloudflare proxy** - DNS record points directly to origin
- This is by design for Minecraft (UDP/TCP game traffic doesn't work well through HTTP proxies)
- **Consequences:** Full port scanning, direct IP attacks, no DDoS mitigation, no WAF

### wp.demonstrated.dev (Cloudflare-protected)

- Origin IP is not directly discoverable from DNS (resolves to Cloudflare IPs)
- `X-Served-By: wp.demonstrated.dev` leaks the internal hostname but not the IP
- No origin IP found through common bypass techniques (MX records, SPF, historical DNS)
- **Cloudflare bypass difficulty:** High - origin IP not leaked

### cp.demonstrated.dev (Cloudflare-protected)

- Same Cloudflare IPs as wp
- Ports 8080/8443 additionally exposed through Cloudflare - these are non-standard and expand the attack surface
- Title "copyparty @ pve" suggests a Proxmox VE host - if this shares the same origin IP as wp, it could be a bypass vector

---

## 9. mc.demonstrated.dev Attack Surface (No Cloudflare Protection)

| Risk | Detail |
|------|--------|
| **Direct IP exposure** | 99.53.73.216 visible in DNS, no proxy |
| **Open DNS resolver** | dnsmasq 2.89 on port 53 accepts recursive queries from any source |
| **Version disclosure** | dnsmasq version leaked, ISP identity leaked via rDNS |
| **Minecraft port 25565** | Open, accessible to all. Minecraft servers are frequent DDoS targets. No proxy protection. |
| **Ports 80/443** | TCP connections accepted but services return empty/invalid responses (possible misconfigured reverse proxy or service not fully bound) |
| **No WAF** | All traffic reaches origin directly |
| **No DDoS mitigation** | No Cloudflare or similar protection layer |
| **Residential ISP** | rDNS suggests AT&T residential connection with limited bandwidth - vulnerable to volumetric attacks |

---

## 10. Information Disclosure Summary

| Item | Location | Detail |
|------|----------|--------|
| Server software | wp HTTP headers | `Server: cloudflare` (Cloudflare masks origin) |
| Internal hostname | wp/cp HTTP headers | `X-Served-By: wp.demonstrated.dev`, `X-Served-By: cp.demonstrated.dev` |
| Application name | wp /login title | "Login - WebFiction Tracker" |
| DNS resolver version | mc port 53 | `dnsmasq-2.89` |
| ISP identity | mc rDNS | AT&T Lightspeed, Louisville KY |
| WebDAV service | cp OPTIONS response | `Allow: GET, HEAD, POST, PUT, DELETE, OPTIONS, PROPFIND, PROPPATCH, LOCK, UNLOCK, MKCOL, COPY, MOVE`, `DAV: 1, 2` |
| Infrastructure type | cp page title | "copyparty @ pve" (Proxmox VE) |
| Cloudflare Ray ID | All responses | `CF-RAY` header (normal, not exploitable) |
| No security.txt | wp .well-known | 404 returned |

---

## 11. Recommendations Summary (Priority Order)

| Priority | Finding | Action |
|----------|---------|--------|
| **CRITICAL** | Open DNS resolver (mc) | Restrict dnsmasq to localhost only, firewall port 53 externally |
| **CRITICAL** | Exposed WebDAV on cp.demonstrated.dev | Add Cloudflare Access policy, review auth, disable WebDAV if unused |
| **HIGH** | mc.demonstrated.dev has no DDoS protection | Consider TCP Shield or similar Minecraft-specific proxy service |
| **MEDIUM** | TLS 1.0/1.1 enabled | Set minimum TLS version to 1.2 in Cloudflare |
| **MEDIUM** | Cookie flags missing | Add Secure, HttpOnly, SameSite=Lax to csrf_token |
| **MEDIUM** | No DMARC/DKIM/SPF | Add DMARC p=reject, SPF v=spf1 -all |
| **LOW** | X-Served-By header | Remove or genericize |
| **LOW** | No security.txt | Create per RFC 9116 |
| **LOW** | Ports 8080/8443 on cp via Cloudflare | Restrict to 443 only if 8080/8443 not needed |
| **LOW** | dnsmasq version disclosure | Configure version hiding |

---

## 12. Scans Not Completed

| Scan | Reason |
|------|--------|
| UDP port scan on mc.demonstrated.dev | Requires root privileges (sudo not available without password) |
| Minecraft-info nmap script | Script not installed in this nmap version (7.99) |
| DNS subdomain enum with dnsmap.txt | Wordlist not available on this system; used dirb/common.txt instead |

---

*Report generated 2026-05-12 by automated infrastructure audit.*
