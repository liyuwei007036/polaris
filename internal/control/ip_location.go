package control

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
	"github.com/oschwald/geoip2-golang"
)

//go:embed ipdb/ip2region_v4.xdb
var ipRegionV4 []byte

//go:embed ipdb/ip2region_v6.xdb
var ipRegionV6 []byte

//go:embed ipdb/china_cities.json
var chinaCitiesJSON []byte

// LocationInfo represents rich, structured geolocation and network information for an IP address.
type LocationInfo struct {
	IP          string  `json:"ip"`
	Location    string  `json:"location"`
	City        string  `json:"city"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"country_code,omitempty"`
	Province    string  `json:"province,omitempty"`
	ISP         string  `json:"isp,omitempty"`
	ASN         string  `json:"asn,omitempty"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	HasCoords   bool    `json:"has_coords"`
	Source      string  `json:"source"` // "mmdb", "ip2region", "system"
	IsAnycast   bool    `json:"is_anycast"`
	IsDomestic  bool    `json:"is_domestic"`
}

type ipLocator struct {
	mmdbMu   sync.RWMutex
	mmdb     *geoip2.Reader
	mmdbPath string

	xdbMu sync.Mutex
	v4    *xdb.Searcher
	v6    *xdb.Searcher

	chinaCities map[string][2]float64

	cache      sync.Map // string (IP) -> LocationInfo
	searchDirs []string
}

func normalizeRegionName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, "市")
	name = strings.TrimSuffix(name, "区")
	name = strings.TrimSuffix(name, "县")
	name = strings.TrimSuffix(name, "特别行政区")
	name = strings.TrimSuffix(name, "自治州")
	name = strings.TrimSuffix(name, "地区")
	name = strings.TrimSuffix(name, "盟")
	return name
}

func loadChinaCities(embedded []byte, searchDirs ...string) map[string][2]float64 {
	out := make(map[string][2]float64, 500)
	applyJSON := func(data []byte) {
		if len(data) == 0 {
			return
		}
		var raw map[string][2]float64
		if err := json.Unmarshal(data, &raw); err != nil {
			return
		}
		for k, v := range raw {
			out[k] = v
			norm := normalizeRegionName(k)
			if norm != "" && norm != k {
				out[norm] = v
			}
		}
	}
	applyJSON(embedded)

	candidates := []string{
		"data/china_cities.json",
		"china_cities.json",
		filepath.Join("data", "china_cities.json"),
		filepath.Join("ipdb", "china_cities.json"),
	}
	for _, dir := range searchDirs {
		if dir != "" {
			candidates = append(candidates, filepath.Join(dir, "china_cities.json"))
			candidates = append(candidates, filepath.Join(dir, "data", "china_cities.json"))
			candidates = append(candidates, filepath.Join(dir, "ipdb", "china_cities.json"))
		}
	}
	for _, cand := range candidates {
		if b, err := os.ReadFile(cand); err == nil && len(b) > 0 {
			applyJSON(b)
			break
		}
	}
	return out
}

func (l *ipLocator) coordsOfChinaCity(city, province string) (float64, float64, bool) {
	if l.chinaCities == nil {
		return 0, 0, false
	}
	check := func(name string) (float64, float64, bool) {
		name = strings.TrimSpace(name)
		if name == "" || name == "0" {
			return 0, 0, false
		}
		if coords, ok := l.chinaCities[name]; ok {
			return coords[0], coords[1], true
		}
		norm := normalizeRegionName(name)
		if norm != "" {
			if coords, ok := l.chinaCities[norm]; ok {
				return coords[0], coords[1], true
			}
		}
		return 0, 0, false
	}

	if lat, lon, ok := check(city); ok {
		return lat, lon, true
	}
	if lat, lon, ok := check(province); ok {
		return lat, lon, true
	}
	return 0, 0, false
}

func newIPLocator(searchDirs ...string) (*ipLocator, error) {
	v4, err := xdb.NewWithBuffer(xdb.IPv4, ipRegionV4)
	if err != nil {
		return nil, fmt.Errorf("ip2region v4: %w", err)
	}
	v6, err := xdb.NewWithBuffer(xdb.IPv6, ipRegionV6)
	if err != nil {
		return nil, fmt.Errorf("ip2region v6: %w", err)
	}

	locator := &ipLocator{
		v4:          v4,
		v6:          v6,
		chinaCities: loadChinaCities(chinaCitiesJSON, searchDirs...),
		searchDirs:  searchDirs,
	}

	// Auto-discover standard MMDB files in search paths
	_ = locator.autoLoadMMDB(searchDirs...)

	return locator, nil
}

// autoLoadMMDB scans standard paths for a MaxMind / IPinfo .mmdb file and loads the first valid one found.
func (l *ipLocator) autoLoadMMDB(dirs ...string) error {
	candidates := make([]string, 0, len(dirs)*4+8)
	names := []string{"GeoLite2-City.mmdb", "ipinfo.mmdb", "GeoIP2-City.mmdb", "Country.mmdb", "geoip.mmdb", "cz88.mmdb", "qqwry.mmdb"}

	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, name := range names {
			candidates = append(candidates, filepath.Join(dir, name))
			candidates = append(candidates, filepath.Join(dir, "ipdb", name))
		}
	}

	for _, name := range names {
		candidates = append(candidates, name)
		candidates = append(candidates, filepath.Join("ipdb", name))
		candidates = append(candidates, filepath.Join("data", name))
		candidates = append(candidates, filepath.Join("/var/lib/polaris", name))
		candidates = append(candidates, filepath.Join("/etc/polaris", name))
	}

	for _, path := range candidates {
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			if err := l.LoadMMDB(path); err == nil {
				return nil
			}
		}
	}
	return os.ErrNotExist
}

// LoadMMDB loads or hot-reloads a MaxMind DB (.mmdb) file.
func (l *ipLocator) LoadMMDB(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("mmdb path is empty")
	}
	reader, err := geoip2.Open(path)
	if err != nil {
		return fmt.Errorf("open mmdb %s: %w", path, err)
	}

	l.mmdbMu.Lock()
	oldReader := l.mmdb
	l.mmdb = reader
	l.mmdbPath = path
	l.mmdbMu.Unlock()

	if oldReader != nil {
		_ = oldReader.Close()
	}

	l.ClearCache()
	return nil
}

// Close releases resources held by the locator.
func (l *ipLocator) Close() error {
	l.mmdbMu.Lock()
	defer l.mmdbMu.Unlock()
	if l.mmdb != nil {
		err := l.mmdb.Close()
		l.mmdb = nil
		l.mmdbPath = ""
		return err
	}
	return nil
}

// MMDBStatus returns whether an MMDB reader is currently active, its path, and database type.
func (l *ipLocator) MMDBStatus() (loaded bool, path string, dbType string) {
	l.mmdbMu.RLock()
	defer l.mmdbMu.RUnlock()
	if l.mmdb != nil {
		return true, l.mmdbPath, l.mmdb.Metadata().DatabaseType
	}
	return false, "", ""
}

// ClearCache flushes the in-memory lookup cache.
func (l *ipLocator) ClearCache() {
	l.cache.Range(func(key, _ any) bool {
		l.cache.Delete(key)
		return true
	})
}

// Reload re-scans configured and default directories for an MMDB database file.
func (l *ipLocator) Reload(searchDirs ...string) error {
	l.mmdbMu.RLock()
	currentPath := l.mmdbPath
	l.mmdbMu.RUnlock()

	if currentPath != "" {
		if err := l.LoadMMDB(currentPath); err == nil {
			return nil
		}
	}

	allDirs := append([]string{}, l.searchDirs...)
	allDirs = append(allDirs, searchDirs...)
	return l.autoLoadMMDB(allDirs...)
}

// Lookup resolves an IP address to complete, authoritative LocationInfo.
func (l *ipLocator) Lookup(value string) LocationInfo {
	ipStr := strings.TrimSpace(value)
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return LocationInfo{IP: ipStr, Location: "未知", Source: "unknown"}
	}
	if ip.IsLoopback() {
		return LocationInfo{IP: ipStr, Location: "本机", City: "本机", Source: "system"}
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return LocationInfo{IP: ipStr, Location: "内网", City: "内网", Source: "system"}
	}
	if ip.IsUnspecified() {
		return LocationInfo{IP: ipStr, Location: "未指定地址", Source: "system"}
	}
	if ip.IsMulticast() {
		return LocationInfo{IP: ipStr, Location: "组播地址", Source: "system"}
	}

	// Fast lock-free path: check in-memory cache
	if cached, ok := l.cache.Load(ipStr); ok {
		return cached.(LocationInfo)
	}

	// Dual-engine query:
	// 1. ip2region (embedded, authoritative for Mainland China provinces, cities, and domestic carriers)
	ip2regRes := l.lookupIP2Region(ipStr, ip)

	// 2. MMDB (MaxMind / IPinfo, authoritative for global/international geography, BGP Anycast, and ASN)
	l.mmdbMu.RLock()
	mmdbReader := l.mmdb
	l.mmdbMu.RUnlock()

	var mmdbRes LocationInfo
	var mmdbMatched bool
	if mmdbReader != nil {
		mmdbRes, mmdbMatched = l.lookupMMDB(mmdbReader, ip)
	}

	var res LocationInfo

	// Intelligent Domestic / International Dual-Engine Synergy:
	isDomestic := ip2regRes.IsDomestic || (mmdbMatched && mmdbRes.IsDomestic)

	if isDomestic {
		// Mainland China: ip2region is the gold standard for domestic provinces, cities, and carriers.
		res = ip2regRes
		if mmdbMatched {
			if res.City == "" && mmdbRes.City != "" {
				res.City = mmdbRes.City
			}
			if res.Province == "" && mmdbRes.Province != "" {
				res.Province = mmdbRes.Province
			}
			if mmdbRes.ASN != "" {
				res.ASN = mmdbRes.ASN
			}
			res.IsAnycast = mmdbRes.IsAnycast
			res.Source = "ip2region+mmdb"
		}
		// Coordinate resolution for China:
		if lat, lon, ok := l.coordsOfChinaCity(res.City, res.Province); ok {
			res.Latitude = lat
			res.Longitude = lon
			res.HasCoords = true
		} else if mmdbMatched && mmdbRes.HasCoords && !(mmdbRes.Latitude > 34.77 && mmdbRes.Latitude < 34.78 && mmdbRes.Longitude > 113.72 && mmdbRes.Longitude < 113.73) {
			res.Latitude = mmdbRes.Latitude
			res.Longitude = mmdbRes.Longitude
			res.HasCoords = true
		}
	} else {
		// International / Overseas: MMDB is the global authority for international geography and Anycast.
		if mmdbMatched && (mmdbRes.Country != "" || mmdbRes.City != "") {
			res = mmdbRes
			if res.ISP == "" && ip2regRes.ISP != "" {
				res.ISP = ip2regRes.ISP
			}
		} else {
			res = ip2regRes
		}
		if mmdbMatched && mmdbRes.HasCoords {
			res.Latitude = mmdbRes.Latitude
			res.Longitude = mmdbRes.Longitude
			res.HasCoords = true
		}
	}

	if res.Location == "" {
		res.Location = "未知"
	}

	l.cache.Store(ipStr, res)
	return res
}

func (l *ipLocator) lookupMMDB(reader *geoip2.Reader, ip net.IP) (LocationInfo, bool) {
	record, err := reader.City(ip)
	if err != nil {
		countryRecord, cErr := reader.Country(ip)
		if cErr != nil {
			return LocationInfo{}, false
		}
		countryName := pickLocalizedName(countryRecord.Country.Names)
		isoCode := strings.ToUpper(strings.TrimSpace(countryRecord.Country.IsoCode))
		if countryName == "" {
			countryName = isoCode
		}
		if countryName == "" {
			return LocationInfo{}, false
		}
		isDomestic := isoCode == "CN"
		return LocationInfo{
			IP:          ip.String(),
			Country:     countryName,
			CountryCode: isoCode,
			Location:    countryName,
			City:        "",
			Source:      "mmdb",
			IsDomestic:  isDomestic,
		}, true
	}

	isoCode := strings.ToUpper(strings.TrimSpace(record.Country.IsoCode))
	countryName := pickLocalizedName(record.Country.Names)
	if countryName == "" {
		countryName = isoCode
	}
	if countryName == "" && len(record.Subdivisions) == 0 && len(record.City.Names) == 0 {
		return LocationInfo{}, false
	}

	var provName string
	if len(record.Subdivisions) > 0 {
		provName = pickLocalizedName(record.Subdivisions[0].Names)
	}

	cityName := pickLocalizedName(record.City.Names)
	isDomestic := isoCode == "CN"

	parts := make([]string, 0, 3)
	if !isDomestic && countryName != "" {
		parts = append(parts, countryName)
	}
	if provName != "" && provName != countryName {
		parts = append(parts, provName)
	}
	if cityName != "" && cityName != provName && cityName != countryName {
		parts = append(parts, cityName)
	}
	locStr := strings.Join(parts, " ")
	if locStr == "" {
		locStr = countryName
	}

	asnStr := ""
	ispStr := ""
	if asnRecord, aErr := reader.ASN(ip); aErr == nil && asnRecord.AutonomousSystemNumber > 0 {
		asnStr = fmt.Sprintf("AS%d", asnRecord.AutonomousSystemNumber)
		ispStr = asnRecord.AutonomousSystemOrganization
	}

	lat := record.Location.Latitude
	lon := record.Location.Longitude
	hasCoords := lat != 0 || lon != 0

	return LocationInfo{
		IP:          ip.String(),
		Country:     countryName,
		CountryCode: isoCode,
		Province:    provName,
		City:        cityName,
		Location:    locStr,
		ISP:         ispStr,
		ASN:         asnStr,
		Latitude:    lat,
		Longitude:   lon,
		HasCoords:   hasCoords,
		Source:      "mmdb",
		IsAnycast:   record.Traits.IsAnycast,
		IsDomestic:  isDomestic,
	}, true
}

func (l *ipLocator) lookupIP2Region(ipStr string, ip net.IP) LocationInfo {
	l.xdbMu.Lock()
	searcher := l.v6
	if ip.To4() != nil {
		searcher = l.v4
	}
	region, err := searcher.Search(ipStr)
	l.xdbMu.Unlock()

	if err != nil || region == "" {
		return LocationInfo{IP: ipStr, Location: "未知", Source: "ip2region"}
	}

	parts := strings.Split(region, "|")
	var country, province, city, isp, countryCode string
	if len(parts) > 0 {
		country = parts[0]
	}
	if len(parts) > 1 {
		province = parts[1]
	}
	if len(parts) > 2 {
		city = parts[2]
	}
	if len(parts) > 3 {
		isp = parts[3]
	}
	if len(parts) > 4 {
		countryCode = parts[4]
	}

	clean := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "0" {
			return ""
		}
		return s
	}
	country = clean(country)
	province = clean(province)
	city = clean(city)
	isp = clean(isp)
	countryCode = clean(countryCode)

	isDomestic := country == "中国" &&
		!strings.Contains(region, "香港") &&
		!strings.Contains(region, "澳门") &&
		!strings.Contains(region, "台湾")

	loc := formatIPRegion(region)

	return LocationInfo{
		IP:          ipStr,
		Location:    loc,
		City:        city,
		Country:     country,
		CountryCode: countryCode,
		Province:    province,
		ISP:         isp,
		Source:      "ip2region",
		IsDomestic:  isDomestic,
	}
}

func pickLocalizedName(names map[string]string) string {
	if names == nil {
		return ""
	}
	if zh, ok := names["zh-CN"]; ok && zh != "" {
		return zh
	}
	if en, ok := names["en"]; ok && en != "" {
		return en
	}
	for _, v := range names {
		if v != "" {
			return v
		}
	}
	return ""
}

func (l *ipLocator) Locate(value string) string {
	return l.Lookup(value).Location
}

func (l *ipLocator) LocateCity(value string) string {
	return l.Lookup(value).City
}

func (l *ipLocator) LocateASN(value string) string {
	return l.Lookup(value).ASN
}

func (l *ipLocator) IsDomestic(value string) bool {
	return l.Lookup(value).IsDomestic
}

func (l *ipLocator) LocateCIDR(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "所有来源"
	}
	if ip := net.ParseIP(value); ip != nil {
		return l.Locate(ip.String())
	}
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return "未知"
	}
	ones, bits := network.Mask.Size()
	if ones == 0 {
		if bits == 32 {
			return "所有 IPv4 地址"
		}
		return "所有 IPv6 地址"
	}
	return l.Locate(network.IP.String())
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
