package hivepanel

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"os"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
)

// dockerHost is the Docker host's address on the container's network (the
// default route's gateway), or "" outside Docker.
func dockerHost() string {
	if !config.InContainer {
		return ""
	}
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	return gatewayOf(string(b))
}

// gatewayOf reads the default route's gateway from /proc/net/route text
// (addresses are hex, little-endian).
func gatewayOf(route string) string {
	lines := strings.Split(route, "\n")
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, binary.LittleEndian.Uint32(raw))
		if !ip.IsUnspecified() {
			return ip.String()
		}
	}
	return ""
}
