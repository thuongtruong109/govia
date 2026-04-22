package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/proxy"
)

var (
	hopByHop = map[string]struct{}{
		"Connection":          {},
		"Proxy-Connection":    {},
		"Keep-Alive":          {},
		"TE":                  {},
		"Trailer":             {},
		"Transfer-Encoding":   {},
		"Upgrade":             {},
		"Proxy-Authenticate":  {},
		"Proxy-Authorization": {},
	}

	refererMap sync.Map // map[string]*RouteContext
)

type RouteContext struct {
	DestURL   string
	ProxySpec string
}

// ====================== SSRF protection ======================

func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	ip = ip.To16()
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	privateCIDRs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidr := range privateCIDRs {
		_, block, _ := net.ParseCIDR(cidr)
		if block != nil && block.Contains(ip) {
			return true
		}
	}
	return false
}

func blockSSRF(u *url.URL) error {
	if u == nil {
		return errors.New("nil url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("blocked scheme: %s (only http/https allowed)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("empty host")
	}

	lh := strings.ToLower(host)
	if lh == "localhost" || lh == "localhost.localdomain" {
		return fmt.Errorf("blocked host: %s", host)
	}

	// If IP literal
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("blocked private/local ip: %s", ip.String())
		}
		return nil
	}

	// Resolve DNS and block private/local
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup failed: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no ip for host: %s", host)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("blocked private/local resolved ip: %s", ip.String())
		}
	}
	return nil
}

// ====================== Header sanitize ======================

func copySafeRequestHeaders(dst, src http.Header) {
	for k, vals := range src {
		// Skip hop-by-hop headers
		if _, ok := hopByHop[k]; ok {
			continue
		}

		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func copySafeResponseHeaders(w gin.ResponseWriter, src http.Header) {
	// Clean existing headers first (avoid mixing)
	h := w.Header()
	for k := range h {
		h.Del(k)
	}

	for k, vals := range src {
		// Skip hop-by-hop
		if _, ok := hopByHop[k]; ok {
			continue
		}

		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
}

// ====================== Path parsing ======================

// Accept:
// /https://example.com/path
// /http://example.com/path
// /<proxySpec>/https://example.com/path
// /<proxySpec>/http://example.com/path
func parseIncomingPath(fullPath string) (requestedURL string, proxySpec string, err error) {
	p := strings.TrimPrefix(fullPath, "/")
	if p == "" {
		return "", "", errors.New("empty path")
	}

	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p, "", nil
	}

	i := strings.Index(p, "/http://")
	j := strings.Index(p, "/https://")

	idx := -1
	if i >= 0 && j >= 0 {
		idx = min(i, j)
	} else if i >= 0 {
		idx = i
	} else if j >= 0 {
		idx = j
	}

	if idx <= 0 {
		return "", "", errors.New("invalid proxy path; expected /proxy/<spec>/http(s)://...")
	}

	proxySpec = p[:idx]
	requestedURL = p[idx+1:]
	return requestedURL, proxySpec, nil
}

func parseProxySpec(proxySpec string) (*url.URL, error) {
	ps := strings.TrimSpace(proxySpec)
	if ps == "" {
		return nil, nil
	}
	if !strings.Contains(ps, "://") {
		ps = "http://" + ps
	}
	pu, err := url.Parse(ps)
	if err != nil {
		return nil, err
	}
	switch pu.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", pu.Scheme)
	}
	if pu.Host == "" {
		return nil, errors.New("proxy host empty")
	}
	return pu, nil
}

func createSOCKS5Dialer(pu *url.URL) (proxy.Dialer, error) {
	var auth *proxy.Auth
	if pu.User != nil {
		pw, _ := pu.User.Password()
		auth = &proxy.Auth{User: pu.User.Username(), Password: pw}
	}
	return proxy.SOCKS5("tcp", pu.Host, auth, proxy.Direct)
}

func newHTTPClient(proxySpec string) (*http.Client, error) {
	pu, err := parseProxySpec(proxySpec)
	if err != nil {
		return nil, err
	}

	baseTransport := &http.Transport{
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

	if pu == nil {
		baseTransport.DialContext = dialer.DialContext
		return &http.Client{
			Transport: baseTransport,
			Timeout:   60 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}, nil
	}

	switch pu.Scheme {
	case "socks5":
		// SOCKS5: do NOT set Transport.Proxy
		sd, err := createSOCKS5Dialer(pu)
		if err != nil {
			return nil, err
		}
		baseTransport.Proxy = nil
		baseTransport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			// socks5 Dialer doesn't take ctx; best-effort
			type result struct {
				c   net.Conn
				err error
			}
			ch := make(chan result, 1)
			go func() {
				c, e := sd.Dial(network, addr)
				ch <- result{c: c, err: e}
			}()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case r := <-ch:
				return r.c, r.err
			}
		}

	case "http", "https":
		baseTransport.Proxy = http.ProxyURL(pu)
		baseTransport.DialContext = dialer.DialContext

	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", pu.Scheme)
	}

	return &http.Client{
		Transport: baseTransport,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func handleRequest(c *gin.Context) {
	if c.Param("path") == "/" {
		c.IndentedJSON(http.StatusOK, gin.H{
			"message": "Safe forward proxy (passthrough body + keep Content-Encoding)",
			"usage": []string{
				"/https://example.com/path",
				"/http://example.com/path",
				"/proxy/<proxySpec>/https://example.com/path",
			},
		})
		return
	}

	requestedURL, proxySpec, err := parseIncomingPath(c.Param("path"))
	clientIP := c.ClientIP()

	if err != nil {
		// Try Referer fallback for implicit asset paths
		referer := c.Request.Header.Get("Referer")
		if referer != "" {
			if refURL, pErr := url.Parse(referer); pErr == nil {
				if rcIntf, ok := refererMap.Load(clientIP + "|" + refURL.Path); ok {
					rc := rcIntf.(*RouteContext)
					baseDestURL, _ := url.Parse(rc.DestURL)
					resolvedURL := baseDestURL.ResolveReference(c.Request.URL)

					requestedURL = resolvedURL.String()
					proxySpec = rc.ProxySpec
					err = nil
				}
			}
		}
	}

	if err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	parsedURL, err := url.Parse(requestedURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "Invalid URL"})
		return
	}

	// Store successful mapping for subsequent relative asset loads
	refererMap.Store(clientIP+"|"+c.Request.URL.Path, &RouteContext{
		DestURL:   parsedURL.String(),
		ProxySpec: proxySpec,
	})

	if err := blockSSRF(parsedURL); err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}

	outReq, err := http.NewRequestWithContext(
		c.Request.Context(),
		c.Request.Method,
		parsedURL.String(),
		c.Request.Body,
	)
	if err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "Failed to create request"})
		return
	}

	outReq.Header = make(http.Header)
	copySafeRequestHeaders(outReq.Header, c.Request.Header)

	// Stealth Headers Rewrite
	if outReq.Header.Get("Referer") != "" {
		ref := c.Request.Header.Get("Referer")
		if refURL, pErr := url.Parse(ref); pErr == nil {
			if rcIntf, ok := refererMap.Load(clientIP + "|" + refURL.Path); ok {
				outReq.Header.Set("Referer", rcIntf.(*RouteContext).DestURL)
			}
		}
	}
	if outReq.Header.Get("Origin") != "" {
		outReq.Header.Set("Origin", parsedURL.Scheme+"://"+parsedURL.Host)
	}

	outReq.URL.RawQuery = c.Request.URL.RawQuery

	client, err := newHTTPClient(proxySpec)
	if err != nil {
		c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "Invalid proxy: " + err.Error()})
		return
	}

	resp, err := client.Do(outReq)
	if err != nil {
		c.IndentedJSON(http.StatusBadGateway, gin.H{"message": "Upstream request failed: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	copySafeResponseHeaders(c.Writer, resp.Header)
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

func main() {
	addr := ":5000"
	if v := strings.TrimSpace(os.Getenv("BIND_ADDR")); v != "" {
		addr = v
	}

	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/*path", handleRequest)
	r.POST("/*path", handleRequest)
	r.PUT("/*path", handleRequest)
	r.PATCH("/*path", handleRequest)
	r.DELETE("/*path", handleRequest)
	r.OPTIONS("/*path", handleRequest)
	r.HEAD("/*path", handleRequest)

	_ = r.Run(addr)
}