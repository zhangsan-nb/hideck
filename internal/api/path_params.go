package api

import (
	"strings"
)

func firstPathParam(c businessContext, names ...string) string {
	if c == nil {
		return ""
	}
	for _, name := range names {
		if v := strings.TrimSpace(c.Param(name)); v != "" {
			return v
		}
	}
	return ""
}

func deviceIDParam(c businessContext) string {
	return firstPathParam(c, "device_id", "id")
}

func upstreamProxyIDParam(c businessContext) string {
	return firstPathParam(c, "proxy_id", "id")
}

func countryCodeParam(c businessContext) string {
	return firstPathParam(c, "country_code", "country")
}

func proxyInstanceIDParam(c businessContext) string {
	return firstPathParam(c, "instance_id", "id")
}
