package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/proxy"
)

type BrowserProfile struct {
	UserAgent          string
	AcceptLanguage     string
	SecCHUA            string
	SecCHUAMobile      string
	SecCHUAPlatform    string
	AcceptHeader       string
	AcceptEncoding     string
	HeaderOrder        []string
	ScreenResolution   string
	HardwareConcurrency int
	DeviceMemory       int
	Platform           string
	Timezone           string
	DoNotTrack         string
	WebGLVendor        string
	WebGLRenderer      string
}

func createProxyDialer(proxyURL *url.URL) (proxy.Dialer, error) {
	var auth *proxy.Auth
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		auth = &proxy.Auth{
			User:     proxyURL.User.Username(),
			Password: password,
		}
	}

	if proxyURL.Scheme == "socks5" {
		return proxy.SOCKS5("tcp", proxyURL.Host, auth, proxy.Direct)
	}

	return &httpProxyDialer{
		proxyURL: proxyURL,
		auth:     auth,
	}, nil
}

type httpProxyDialer struct {
	proxyURL *url.URL
	auth     *proxy.Auth
}

func (d *httpProxyDialer) Dial(network, addr string) (net.Conn, error) {
	// Connect to proxy
	conn, err := net.DialTimeout("tcp", d.proxyURL.Host, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func handleRequest(ctx *gin.Context) {
	path := ctx.Param("path")

	if path == "/" {
		ctx.IndentedJSON(http.StatusOK, gin.H{
			"message": "🔒 Ultra-Stealth Anonymous Proxy with Advanced Anti-Detection",
			"usage": "Go to /:url to use, or /proxy-spec/:url to use with proxy",
			"proxy_formats": []string{
				"host:port",
				"username:password@host:port",
				"host:port@username:password",
				"host:username:password:port",
				"username:password:host:port",
				"socks5://host:port",
				"socks5://username:password@host:port",
			},
		})
		ctx.Done()
		return
	}

	var requestedURL string
	var proxySpec string

	// Check if this is a proxied URL with proxy spec (format: /proxy:port/http... or /proxy:port/https...)
	// Find the first occurrence of /http or /https
	httpIndex := strings.Index(path, "/http")
	if httpIndex == -1 {
		httpsIndex := strings.Index(path, "/https")
		if httpsIndex != -1 {
			httpIndex = httpsIndex
		}
	}

	if httpIndex > 0 {
		// Found proxy spec before the URL
		proxySpec = path[1:httpIndex] // Remove leading slash
		requestedURL = path[httpIndex+1:] // Remove leading slash from URL part
	} else {
		// Check if this is a direct proxied URL (starts with /http or /https)
		if strings.HasPrefix(path, "/http") || strings.HasPrefix(path, "/https") {
			requestedURL = path[1:]
		} else {
			// This might be a relative URL from a previously proxied page
			// Try to get the original host from Referer header
			referer := ctx.Request.Header.Get("Referer")
			if referer != "" {
				// Extract the proxied URL from referer
				// Referer will be like: http://localhost:5000/https://example.com/page
				// or with proxy: http://localhost:5000/proxy:port/https://example.com/page
				if strings.Contains(referer, "://"+ctx.Request.Host+"/") {
					parts := strings.SplitN(referer, "/"+ctx.Request.Host+"/", 2)
					if len(parts) == 2 {
						refererPath := parts[1]
						var baseURL string

						// Check if referer path contains proxy spec
						httpIndex := strings.Index(refererPath, "/http")
						if httpIndex == -1 {
							httpsIndex := strings.Index(refererPath, "/https")
							if httpsIndex != -1 {
								httpIndex = httpsIndex
							}
						}

						if httpIndex > 0 {
							// Referer has proxy spec, use it for this request too
							proxySpec = refererPath[:httpIndex]
							baseURL = refererPath[httpIndex+1:] // Remove leading slash
						} else {
							// Referer has direct URL
							baseURL = refererPath
						}

						if strings.HasPrefix(baseURL, "http") {
							// Resolve relative path against the base URL
							parsedBase, err := url.Parse(baseURL)
							if err == nil {
								if strings.HasPrefix(path, "/") {
									// Absolute path on the same host
									requestedURL = parsedBase.Scheme + "://" + parsedBase.Host + path
								} else {
									// Relative path - resolve against current path
									basePath := parsedBase.Path
									if !strings.HasSuffix(basePath, "/") {
										lastSlash := strings.LastIndex(basePath, "/")
										if lastSlash >= 0 {
											basePath = basePath[:lastSlash+1]
										}
									}
									requestedURL = parsedBase.Scheme + "://" + parsedBase.Host + basePath + path
								}
							}
						}
					}
				}
			}

			if requestedURL == "" {
				ctx.IndentedJSON(http.StatusBadRequest, gin.H{
					"message": "Invalid URL or missing referer for relative path",
				})
				ctx.Done()
				return
			}

			// Validate resolved URL
			parsedURL, err := url.Parse(requestedURL)
			if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
				ctx.IndentedJSON(http.StatusBadRequest, gin.H{
					"message": "Invalid resolved URL",
				})
				ctx.Done()
				return
			}
		}
	}

	parsedURL, err := url.Parse(requestedURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		ctx.IndentedJSON(http.StatusBadRequest, gin.H{
			"message": "Invalid URL",
		})
		ctx.Done()
		return
	}

	req, _ := http.NewRequest(ctx.Request.Method, requestedURL, ctx.Request.Body)

	req.Header = ctx.Request.Header.Clone()
	req.Header.Del("origin")
	req.Header.Del("referer")

	queryValues := req.URL.Query()
	for k, v := range ctx.Request.URL.Query() {
		queryValues.Add(k, v[0])
	}
	req.URL.RawQuery = queryValues.Encode()

	var client *http.Client
	if proxySpec != "" {
		var proxyURLStr string

		// Support multiple proxy formats:
		// 1. host:port (simple proxy)
		// 2. username:password@host:port (standard)
		// 3. host:port@username:password
		// 4. host:username:password:port
		// 5. username:password:host:port
		// 6. socks5://username:password@host:port (SOCKS5 with auth)
		// 7. socks5://host:port (SOCKS5 without auth)

		// Check if it's explicitly a SOCKS5 proxy
		if strings.HasPrefix(proxySpec, "socks5://") {
			proxyURLStr = proxySpec
		} else if strings.Contains(proxySpec, "@") {
			// Could be username:password@host:port or host:port@username:password
			atIndex := strings.Index(proxySpec, "@")
			beforeAt := proxySpec[:atIndex]
			afterAt := proxySpec[atIndex+1:]

			beforeParts := strings.Split(beforeAt, ":")
			afterParts := strings.Split(afterAt, ":")

			if len(beforeParts) == 2 && len(afterParts) == 2 {
				// Could be either format, check which one makes sense
				// If beforeAt looks like host:port and afterAt looks like username:password
				if (strings.Contains(beforeParts[0], ".") || strings.Contains(beforeParts[0], "-") || net.ParseIP(beforeParts[0]) != nil) {
					// Format: host:port@username:password
					host, port := beforeParts[0], beforeParts[1]
					username, password := afterParts[0], afterParts[1]
					proxyURLStr = fmt.Sprintf("http://%s:%s@%s:%s", username, password, host, port)
				} else {
					// Format: username:password@host:port
					username, password := beforeParts[0], beforeParts[1]
					host, port := afterParts[0], afterParts[1]
					proxyURLStr = fmt.Sprintf("http://%s:%s@%s:%s", username, password, host, port)
				}
			} else {
				// Fallback to standard format
				proxyURLStr = "http://" + proxySpec
			}
		} else {
			parts := strings.Split(proxySpec, ":")
			if len(parts) == 2 {
				// Format: host:port
				host, port := parts[0], parts[1]
				proxyURLStr = fmt.Sprintf("http://%s:%s", host, port)
			} else if len(parts) == 4 {
				// Could be host:username:password:port or username:password:host:port
				// Try to detect by checking if first part looks like IP/hostname
				firstPart := parts[0]
				if strings.Contains(firstPart, ".") || strings.Contains(firstPart, "-") || net.ParseIP(firstPart) != nil {
					// Looks like host:username:password:port
					host, username, password, port := parts[0], parts[1], parts[2], parts[3]
					proxyURLStr = fmt.Sprintf("http://%s:%s@%s:%s", username, password, host, port)
				} else {
					// Looks like username:password:host:port
					username, password, host, port := parts[0], parts[1], parts[2], parts[3]
					proxyURLStr = fmt.Sprintf("http://%s:%s@%s:%s", username, password, host, port)
				}
			} else {
				ctx.IndentedJSON(http.StatusBadRequest, gin.H{
					"message": "Invalid proxy specification. Supported formats: host:port, username:password@host:port, host:port@username:password, host:username:password:port, username:password:host:port, socks5://host:port, or socks5://username:password@host:port",
				})
				ctx.Done()
				return
			}
		}

		proxyURL, err := url.Parse(proxyURLStr)
		if err != nil {
			ctx.IndentedJSON(http.StatusBadRequest, gin.H{
				"message": "Invalid proxy specification",
			})
			ctx.Done()
			return
		}

		// Create transport with DNS resolution through proxy
		transport := &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			// Use default TLS config
			TLSClientConfig: nil,
			// Force DNS through proxy by using custom dialer
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// For SOCKS5, use the proxy dialer which handles DNS
				if proxyURL.Scheme == "socks5" {
					dialer, err := createProxyDialer(proxyURL)
					if err != nil {
						return nil, err
					}
					return dialer.Dial(network, addr)
				}
				// For HTTP proxy, use standard dialer (HTTP proxy handles DNS)
				dialer := &net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}
				return dialer.DialContext(ctx, network, addr)
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}

		client = &http.Client{
			Transport: transport,
			Timeout:   60 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Allow up to 10 redirects
				if len(via) >= 10 {
					return http.ErrUseLastResponse
				}
				// Simple proxy - no header modifications for redirects
				return nil
			},
		}
	} else {
		// Default client without proxy
		transport := &http.Transport{
			TLSClientConfig: nil, // Use default TLS config
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}

		client = &http.Client{
			Transport: transport,
			Timeout:   60 * time.Second,
		}
	}

	response, err1 := client.Do(req)

	if err1 != nil {
		ctx.IndentedJSON(http.StatusInternalServerError, gin.H{
			"message": "Failed to request: " + err1.Error(),
		})
		ctx.Done()
		return
	}

	for k, v := range response.Header {
		ctx.Header(k, v[0])
	}

	responseBytes, err2 := io.ReadAll(response.Body)

	if err2 != nil {
		ctx.IndentedJSON(http.StatusInternalServerError, gin.H{
			"message": "Failed to read response: " + err2.Error(),
		})
		ctx.Done()
		return
	}

	response.Body.Close()

	ctx.Data(response.StatusCode, response.Header.Get("Content-Type"), responseBytes)
}

func main() {
	router := gin.Default()

	router.GET("*path", handleRequest)
	router.POST("*path", handleRequest)
	router.PUT("*path", handleRequest)
	router.PATCH("*path", handleRequest)
	router.DELETE("*path", handleRequest)
	router.OPTIONS("*path", handleRequest)
	router.HEAD("*path", handleRequest)

	router.Run(":5000")
}