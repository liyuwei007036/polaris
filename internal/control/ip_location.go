package control

import (
	_ "embed"
	"net"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

//go:embed ipdb/ip2region_v4.xdb
var ipRegionV4 []byte

//go:embed ipdb/ip2region_v6.xdb
var ipRegionV6 []byte

type ipLocator struct {
	mu sync.Mutex
	v4 *xdb.Searcher
	v6 *xdb.Searcher
}

func newIPLocator() (*ipLocator, error) {
	v4, err := xdb.NewWithBuffer(xdb.IPv4, ipRegionV4)
	if err != nil {
		return nil, err
	}
	v6, err := xdb.NewWithBuffer(xdb.IPv6, ipRegionV6)
	if err != nil {
		return nil, err
	}
	return &ipLocator{v4: v4, v6: v6}, nil
}

func (l *ipLocator) Locate(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return "未知"
	}
	if ip.IsLoopback() {
		return "本机"
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return "内网"
	}
	if ip.IsUnspecified() {
		return "未指定地址"
	}
	if ip.IsMulticast() {
		return "组播地址"
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	searcher := l.v6
	if ip.To4() != nil {
		searcher = l.v4
	}
	region, err := searcher.Search(value)
	if err != nil || region == "" {
		return "未知"
	}
	return formatIPRegion(region)
}

func formatIPRegion(region string) string {
	parts := strings.Split(region, "|")
	if len(parts) > 4 {
		parts = parts[:4]
	}
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "0" || part == "中国" && len(parts) > 1 {
			continue
		}
		if len(result) == 0 || result[len(result)-1] != part {
			result = append(result, part)
		}
	}
	if len(result) == 0 {
		return "未知"
	}
	return strings.Join(result, " ")
}

func addressIP(address string) string {
	address = strings.TrimSpace(address)
	if host, _, err := net.SplitHostPort(address); err == nil {
		return strings.Trim(host, "[]")
	}
	if net.ParseIP(address) != nil {
		return address
	}
	if index := strings.LastIndex(address, ":"); index > 0 && net.ParseIP(address[:index]) != nil {
		return address[:index]
	}
	return ""
}

func requestIP(raddr string, headers map[string]string) string {
	for _, key := range []string{"CF-Connecting-IP", "X-Real-IP", "X-Forwarded-For"} {
		value := strings.TrimSpace(headers[key])
		if key == "X-Forwarded-For" {
			value = strings.TrimSpace(strings.Split(value, ",")[0])
		}
		if net.ParseIP(value) != nil {
			return value
		}
	}
	return addressIP(raddr)
}
