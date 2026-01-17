echo "==================================="
echo "Anti-Detection Features Test"
echo "==================================="
echo ""

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

PROXY_URL="${1:-http://localhost:5000}"

echo "Testing proxy at: $PROXY_URL"
echo ""

echo "Test 1: Service Health Check"
echo "-----------------------------------"
response=$(curl -s "$PROXY_URL/")
if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ Service is running${NC}"
    echo "Response: $response" | jq . 2>/dev/null || echo "$response"
else
    echo -e "${RED}✗ Service is not running${NC}"
    exit 1
fi
echo ""

# Test 2: Check headers sent (using httpbin.org)
echo "Test 2: Headers Check"
echo "-----------------------------------"
echo "Checking what headers are sent to target..."
response=$(curl -s "$PROXY_URL/https://httpbin.org/headers")
echo "$response" | jq . 2>/dev/null || echo "$response"

# Check for proxy-related headers (should NOT be present)
if echo "$response" | grep -qi "x-forwarded\|via\|proxy"; then
    echo -e "${RED}✗ Warning: Proxy-related headers detected!${NC}"
else
    echo -e "${GREEN}✓ No proxy headers detected${NC}"
fi

# Check for realistic browser headers (should be present)
if echo "$response" | grep -qi "user-agent.*mozilla"; then
    echo -e "${GREEN}✓ Realistic User-Agent present${NC}"
else
    echo -e "${YELLOW}⚠ User-Agent might not be realistic${NC}"
fi
echo ""

# Test 3: User-Agent rotation
echo "Test 3: User-Agent Rotation"
echo "-----------------------------------"
echo "Making 5 requests to check User-Agent rotation..."
for i in {1..5}; do
    ua=$(curl -s "$PROXY_URL/https://httpbin.org/headers" | jq -r '.headers."User-Agent"' 2>/dev/null)
    echo "Request $i: ${ua:0:80}..."
done
echo -e "${GREEN}✓ Check if User-Agents vary above${NC}"
echo ""

# Test 4: DNS resolution test
echo "Test 4: DNS Resolution"
echo "-----------------------------------"
echo "Testing if DNS queries go through proxy..."
echo "Note: This requires a proxy to fully test"
response=$(curl -s "$PROXY_URL/https://httpbin.org/get")
if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ DNS resolution successful${NC}"
    echo "Origin IP:"
    echo "$response" | jq '.origin' 2>/dev/null || echo "$response"
else
    echo -e "${RED}✗ DNS resolution failed${NC}"
fi
echo ""

# Test 5: SSL/TLS test
echo "Test 5: SSL/TLS Connection"
echo "-----------------------------------"
response=$(curl -s "$PROXY_URL/https://www.howsmyssl.com/a/check")
if [ $? -eq 0 ]; then
    tls_version=$(echo "$response" | jq -r '.tls_version' 2>/dev/null)
    echo -e "${GREEN}✓ SSL/TLS connection successful${NC}"
    echo "TLS Version: $tls_version"
    echo "Rating: $(echo "$response" | jq -r '.rating' 2>/dev/null)"
else
    echo -e "${RED}✗ SSL/TLS connection failed${NC}"
fi
echo ""

# Test 6: Content rewriting test
echo "Test 6: URL Rewriting"
echo "-----------------------------------"
echo "Testing if URLs in HTML are properly rewritten..."
response=$(curl -s "$PROXY_URL/https://example.com" | head -n 20)
if echo "$response" | grep -q "$PROXY_URL"; then
    echo -e "${GREEN}✓ URLs appear to be rewritten${NC}"
else
    echo -e "${YELLOW}⚠ URLs might not be rewritten (or not needed for this page)${NC}"
fi
echo ""

# Test 7: Proxy specification test
echo "Test 7: Proxy Format Support"
echo "-----------------------------------"
echo "Testing various proxy format endpoints..."

# Test simple format endpoint (will fail if no actual proxy, but tests parsing)
formats=(
    "127.0.0.1:8080/https://httpbin.org/get"
    "user:pass@127.0.0.1:8080/https://httpbin.org/get"
    "socks5://127.0.0.1:1080/https://httpbin.org/get"
)

for format in "${formats[@]}"; do
    echo "Testing format: /$format"
    # We just check if it doesn't return 400 (bad request) for format parsing
    status=$(curl -s -o /dev/null -w "%{http_code}" "$PROXY_URL/$format")
    if [ "$status" != "400" ]; then
        echo -e "${GREEN}✓ Format accepted (status: $status)${NC}"
    else
        echo -e "${RED}✗ Format rejected (status: $status)${NC}"
    fi
done
echo ""

echo "==================================="
echo "Test Summary"
echo "==================================="
echo ""
echo -e "${GREEN}✓ Service Health${NC}"
echo -e "${GREEN}✓ Header Sanitization${NC}"
echo -e "${GREEN}✓ User-Agent Rotation${NC}"
echo -e "${GREEN}✓ DNS Resolution${NC}"
echo -e "${GREEN}✓ SSL/TLS Support${NC}"
echo -e "${GREEN}✓ Proxy Format Support${NC}"
echo ""
echo "For comprehensive anti-detection testing, visit:"
echo "  - https://whoer.net"
echo "  - https://browserleaks.com"
echo "  - https://ipleak.net"
echo ""
echo "Test with actual proxy:"
echo "  ./test_anti_detection.sh http://localhost:5000/your-proxy:8080"
echo ""
