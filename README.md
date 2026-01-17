# Govia - Safe Forward Proxy

A secure HTTP proxy service that forwards requests with built-in security protections against SSRF attacks, header injection, and session tracking.

## Features

- 🔒 **SSRF Protection** - Blocks localhost/private IPs, only allows HTTP/HTTPS schemes
- 🛡️ **Header Sanitization** - Allowlist approach: removes hop-by-hop, auth, cookie, and forwarded headers
- 🚫 **No Session Tracking** - Does not forward Set-Cookie headers to prevent client-target coupling
- 🔄 **Redirect Sanitization** - Cleans sensitive headers on redirects
- 🧦 **SOCKS5 Support** - Uses dialer directly (no Transport.Proxy) for better security
- 📡 **Proxy Support** - HTTP/HTTPS/SOCKS5 proxies with authentication
- ⚡ **High Performance** - HTTP/2, connection pooling, keep-alive enabled

## Security Features

### SSRF Protection

- Blocks localhost, localhost.localdomain
- Blocks private IP ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, etc.)
- Only allows HTTP and HTTPS schemes
- DNS resolution checking for private IPs

### Header Security

**Removed Headers (Request/Response):**

- Hop-by-hop: Connection, Proxy-Connection, Keep-Alive, TE, Trailer, Transfer-Encoding, Upgrade, Proxy-Authenticate, Proxy-Authorization
- Sensitive: Cookie, Authorization, Forwarded, X-Forwarded-For, X-Real-IP, Via, X-Proxy-Id, True-Client-IP, CF-Connecting-IP, Referer, Origin

**Response Headers:**

- Set-Cookie is explicitly removed to prevent session tracking between client and target

### Redirect Protection

Headers are sanitized on each redirect to prevent header injection attacks.

## Usage

### Basic Usage

```
http://localhost:5000/:url
```

### With Proxy

```
http://localhost:5000/proxy-spec/:url
```

### Proxy Formats Supported

- `host:port` - Simple HTTP proxy
- `user:pass@host:port` - HTTP proxy with authentication
- `socks5://host:port` - SOCKS5 proxy
- `socks5://user:pass@host:port` - SOCKS5 proxy with authentication

### Examples

```bash
# Direct access
curl http://localhost:5000/https://example.com

# Using HTTP proxy
curl http://localhost:5000/user:pass@proxy.com:8080/https://example.com

# Using SOCKS5 proxy
curl http://localhost:5000/socks5://proxy.com:1080/https://example.com
```

## Installation

```bash
go build -o govia .
./govia
```

Server runs on `http://localhost:5000` (configurable via BIND_ADDR env var)

## API

### GET /

Returns basic information about the proxy service.

```json
{
  "message": "Safe forward proxy (passthrough body + keep Content-Encoding)",
  "usage": [
    "/https://example.com/path",
    "/http://example.com/path",
    "/<proxySpec>/https://example.com/path"
  ]
}
```

### All Methods /\*path

Proxies the request to the specified URL.

- `path` should start with `http://` or `https://`
- Optional proxy specification before the URL
- Headers are sanitized (allowlist approach)
- Request body is forwarded as-is
- Response headers are sanitized (no Set-Cookie)
- Supports GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD

## Configuration

### Environment Variables

```bash
# Bind address (default: :5000)
export BIND_ADDR=:5000
```

## Security Considerations

This proxy is designed to be safe for public deployment:

- ✅ Prevents SSRF attacks
- ✅ Prevents header injection
- ✅ Prevents session coupling via cookies
- ✅ Sanitizes redirects
- ✅ Uses secure TLS config (TLS 1.2+)
- ✅ Timeout protections (60s total, 30s dial)

## Testing

Run the included test script:

```bash
./test.sh
```

## Deployment

### Run Locally

```bash
go run main.go
```

### Docker

```bash
docker build -t govia .
docker run -p 5000:5000 govia
```

## License

MIT License
