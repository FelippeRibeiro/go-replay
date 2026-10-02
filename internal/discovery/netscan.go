package discovery

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// MaxHosts limita quantos endereços um prefixo pode gerar, para evitar que um
// /8 digitado por engano vire milhões de conexões TCP.
const MaxHosts = 8192

// LocalPrefixes devolve as sub-redes IPv4 das interfaces ativas da máquina,
// que é o palpite padrão de onde as câmeras estão. Redes maiores que MaxHosts
// são descartadas: na prática são bridges de container, não a LAN das câmeras.
func LocalPrefixes() ([]netip.Prefix, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var out []netip.Prefix
	seen := make(map[netip.Prefix]bool)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipnet.IP.To4())
			if !ok {
				continue
			}
			ones, _ := ipnet.Mask.Size()
			prefix := netip.PrefixFrom(ip, ones).Masked()
			if ip.IsLinkLocalUnicast() || seen[prefix] {
				continue
			}
			if 1<<(32-prefix.Bits()) > MaxHosts {
				continue
			}
			seen[prefix] = true
			out = append(out, prefix)
		}
	}
	return out, nil
}

// Hosts expande um prefixo nos endereços que valem a pena sondar, descartando
// o endereço de rede e o de broadcast.
func Hosts(prefix netip.Prefix) ([]netip.Addr, error) {
	prefix = prefix.Masked()
	if !prefix.Addr().Is4() {
		return nil, fmt.Errorf("%s: apenas IPv4 é suportado", prefix)
	}
	total := 1 << (32 - prefix.Bits())
	if total > MaxHosts {
		return nil, fmt.Errorf("%s tem %d endereços, acima do limite de %d", prefix, total, MaxHosts)
	}

	addrs := make([]netip.Addr, 0, total)
	for addr := prefix.Addr(); prefix.Contains(addr); addr = addr.Next() {
		addrs = append(addrs, addr)
	}
	// Em /31 e /32 todos os endereços são utilizáveis.
	if len(addrs) > 2 {
		addrs = addrs[1 : len(addrs)-1]
	}
	return addrs, nil
}

// ResolvePrefixes interpreta uma lista de CIDRs separados por vírgula.
// Lista vazia usa as redes das interfaces locais.
func ResolvePrefixes(list string) ([]netip.Prefix, error) {
	if strings.TrimSpace(list) == "" {
		prefixes, err := LocalPrefixes()
		if err != nil {
			return nil, err
		}
		if len(prefixes) == 0 {
			return nil, fmt.Errorf("nenhuma rede local encontrada; informe o CIDR")
		}
		return prefixes, nil
	}
	return ParseCIDRs(list)
}

// ParseCIDRs lê uma lista "192.168.1.0/24,10.0.0.0/24".
func ParseCIDRs(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, raw := range strings.Split(list, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("cidr inválido %q: %w", raw, err)
		}
		if !prefix.Addr().Is4() {
			return nil, fmt.Errorf("%s: apenas IPv4 é suportado", prefix)
		}
		out = append(out, prefix.Masked())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nenhum CIDR informado")
	}
	return out, nil
}

// ParsePorts lê uma lista "554,8554".
func ParsePorts(list string) ([]int, error) {
	var out []int
	for _, raw := range strings.Split(list, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("porta inválida %q", raw)
		}
		out = append(out, port)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nenhuma porta informada")
	}
	return out, nil
}
