package main

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

func rewriteURLs(content, baseURL, proxyBase string) string {
	// Parse the base URL
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return content
	}
	baseHost := parsedURL.Scheme + "://" + parsedURL.Host

	// Function to rewrite a single URL
	rewriteURL := func(originalURL string) string {
		// Skip if already proxied, data URLs, anchors, etc.
		if strings.HasPrefix(originalURL, proxyBase+"/") ||
		   strings.HasPrefix(originalURL, "data:") ||
		   strings.HasPrefix(originalURL, "#") ||
		   strings.HasPrefix(originalURL, "javascript:") ||
		   strings.HasPrefix(originalURL, "mailto:") ||
		   strings.HasPrefix(originalURL, "tel:") ||
		   strings.HasPrefix(originalURL, "ftp:") ||
		   originalURL == "" {
			return originalURL
		}

		if strings.HasPrefix(originalURL, "http://") || strings.HasPrefix(originalURL, "https://") {
			// Absolute URL - proxy it
			return proxyBase + "/" + originalURL
		} else if strings.HasPrefix(originalURL, "//") {
			// Protocol-relative URL
			return proxyBase + "/https:" + originalURL
		} else if strings.HasPrefix(originalURL, "/") {
			// Absolute path
			return proxyBase + "/" + baseHost + originalURL
		} else {
			// Relative path - resolve relative to current path
			basePath := parsedURL.Path
			if basePath != "" && !strings.HasSuffix(basePath, "/") {
				lastSlash := strings.LastIndex(basePath, "/")
				if lastSlash >= 0 {
					basePath = basePath[:lastSlash+1]
				} else {
					basePath = "/"
				}
			} else if basePath == "" {
				basePath = "/"
			}
			resolvedURL := baseHost + basePath + originalURL
			return proxyBase + "/" + resolvedURL
		}
	}

	// Patterns for different types of URLs in content
	patterns := []struct {
		regex    *regexp.Regexp
		groupIdx int
	}{
		// HTML attributes: href="...", src="...", etc.
		{regexp.MustCompile(`(href|src|action|data-src|data-url|data-href|data-original|data-lazy-src|poster|formaction)=["']([^"']+)["']`), 2},
		// CSS url() declarations
		{regexp.MustCompile(`url\(["']?([^"'\)]+)["']?\)`), 1},
		// JavaScript strings that look like URLs
		{regexp.MustCompile(`["']((?:https?:)?//[^"'\s]+)["']`), 1},
		// Meta refresh URLs
		{regexp.MustCompile(`url=([^"'\s>]+)`), 1},
	}

	result := content
	for _, pattern := range patterns {
		result = pattern.regex.ReplaceAllStringFunc(result, func(match string) string {
			submatches := pattern.regex.FindStringSubmatch(match)
			if len(submatches) > pattern.groupIdx {
				originalURL := submatches[pattern.groupIdx]
				rewrittenURL := rewriteURL(originalURL)
				if rewrittenURL != originalURL {
					return strings.Replace(match, originalURL, rewrittenURL, 1)
				}
			}
			return match
		})
	}

	return result
}

func handleRequest(ctx *gin.Context) {
	path := ctx.Param("path")

	if path == "/" {
		ctx.IndentedJSON(http.StatusOK, gin.H{
			"message": "CORS Proxy. Just go to /:url to use",
		})
		ctx.Done()
		return
	}

	var requestedURL string

	// Check if this is a proxied URL (starts with /http or /https)
	if strings.HasPrefix(path, "/http") {
		requestedURL = path[1:]

		// Validate URL
		parsedURL, err := url.Parse(requestedURL)
		if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
			ctx.IndentedJSON(http.StatusBadRequest, gin.H{
				"message": "Invalid URL",
			})
			ctx.Done()
			return
		}
	} else {
		// This might be a relative URL from a previously proxied page
		// Try to get the original host from Referer header
		referer := ctx.Request.Header.Get("Referer")
		if referer != "" {
			// Extract the proxied URL from referer
			// Referer will be like: http://localhost:5000/https://example.com/page
			if strings.Contains(referer, "://"+ctx.Request.Host+"/") {
				parts := strings.SplitN(referer, "/"+ctx.Request.Host+"/", 2)
				if len(parts) == 2 {
					baseURL := parts[1]
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

	req, _ := http.NewRequest(ctx.Request.Method, requestedURL, ctx.Request.Body)

	req.Header = ctx.Request.Header.Clone()
	req.Header.Del("origin")
	req.Header.Del("referer")

	queryValues := req.URL.Query()
	for k, v := range ctx.Request.URL.Query() {
		queryValues.Add(k, v[0])
	}
	req.URL.RawQuery = queryValues.Encode()

	response, err1 := http.DefaultClient.Do(req)

	if err1 != nil {
		ctx.IndentedJSON(http.StatusInternalServerError, gin.H{
			"message": "Failed to request: " + err1.Error(),
		})
		ctx.Done()
		return
	}

	for k, v := range response.Header.Clone() {
		ctx.Header(k, v[0])
	}

	ctx.Header("Access-Control-Allow-Origin", "*")
	ctx.Header("Access-Control-Allow-Methods", "*")
	ctx.Header("Access-Control-Allow-Headers", "*")

	responseBytes, err2 := io.ReadAll(response.Body)

	if err2 != nil {
		ctx.IndentedJSON(http.StatusInternalServerError, gin.H{
			"message": "Failed to read response: " + err2.Error(),
		})
		ctx.Done()
		return
	}

	// Check if response contains text content that might have URLs and rewrite them
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	shouldRewrite := strings.Contains(contentType, "text/html") ||
					 strings.Contains(contentType, "text/css") ||
					 strings.Contains(contentType, "application/javascript") ||
					 strings.Contains(contentType, "application/x-javascript") ||
					 strings.Contains(contentType, "text/javascript") ||
					 strings.Contains(contentType, "text/xml") ||
					 strings.Contains(contentType, "application/xml") ||
					 strings.Contains(contentType, "text/plain")

	if shouldRewrite {
		proxyBase := "http://" + ctx.Request.Host
		contentStr := string(responseBytes)
		rewrittenContent := rewriteURLs(contentStr, requestedURL, proxyBase)
		responseBytes = []byte(rewrittenContent)
	}

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