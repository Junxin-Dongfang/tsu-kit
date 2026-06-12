package metrics

import (
	"net"
	"strings"
)

// PortFromAddr extracts the TCP port from ":9091" or "host:9091".
func PortFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "9091"
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return strings.TrimPrefix(addr, ":")
}
