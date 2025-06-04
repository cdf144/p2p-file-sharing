package corepeer

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
)

// DetermineMachineIP tries to get the public IP, falling back to an outbound local IP.
func DetermineMachineIP() (netip.Addr, error) {
	ipAddr, err := GetPublicIP()
	if err != nil {
		// TODO: Log a warning about falling back to outbound IP
		ipAddr, err = GetOutboundIP()
		if err != nil {
			return netip.Addr{}, fmt.Errorf("failed to get public or outbound IP: %w", err)
		}
	}
	return ipAddr, nil
}

// GetPublicIP attempts to get the public IP address from an external service.
func GetPublicIP() (netip.Addr, error) {
	resp, err := http.Get("https://api.ipify.org?format=text")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("HTTP request to api.ipify.org failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("api.ipify.org returned status %s", resp.Status)
	}

	ipAddrBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("failed to read response from api.ipify.org: %w", err)
	}

	ipAddr, err := netip.ParseAddr(string(ipAddrBytes))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("failed to parse IP address from api.ipify.org response: %w", err)
	}
	return ipAddr, nil
}

// GetOutboundIP gets the preferred outbound IP of this machine.
// This does NOT get the machine's public IP if behind NAT,
// just the IP on its current network interface used for default outbound traffic.
func GetOutboundIP() (netip.Addr, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80") // Google's public DNS server
	if err != nil {
		return netip.Addr{}, fmt.Errorf("failed to dial UDP to 8.8.8.8:80: %w", err)
	}
	defer conn.Close()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, fmt.Errorf("could not cast local address to *net.UDPAddr")
	}

	addr, ok := netip.AddrFromSlice(localAddr.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("failed to convert IP to netip.Addr")
	}
	return addr, nil
}
