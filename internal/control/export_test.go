package control

// SetCloudflareAPIBaseForTest points the Cloudflare client at a test server.
// Compiled only for tests.
func SetCloudflareAPIBaseForTest(url string) { cloudflareAPI = url }
