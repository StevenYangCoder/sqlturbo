package system

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"sqlturbo/internal/domain/history"
)

// CollectHostSnapshot 收集本机 IP、MAC 和公网 IP 信息。
func CollectHostSnapshot(ctx context.Context, logger *slog.Logger) (history.Snapshot, error) {
	snapshot := history.Snapshot{}

	// 先收集本机网卡信息。
	ifaces, err := net.Interfaces()
	if err != nil {
		return snapshot, fmt.Errorf("读取网卡信息失败：%w", err)
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		if hardware := strings.TrimSpace(iface.HardwareAddr.String()); hardware != "" {
			snapshot.MACs = append(snapshot.MACs, hardware)
		}

		addresses, err := iface.Addrs()
		if err != nil {
			logger.Warn("读取网卡地址失败", "网卡", iface.Name, "错误", err)
			continue
		}

		for _, address := range addresses {
			ip := extractIP(address)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}

			if ip.To4() != nil {
				snapshot.LocalIPv4 = append(snapshot.LocalIPv4, ip.String())
				continue
			}

			snapshot.LocalIPv6 = append(snapshot.LocalIPv6, ip.String())
		}
	}

	// 再尝试查询公网 IPv4。
	if ip, err := lookupPublicIP(ctx, "https://api.ipify.org"); err == nil {
		snapshot.PublicIPv4 = append(snapshot.PublicIPv4, ip)
	} else {
		logger.Warn("获取公网IPv4失败", "错误", err)
	}

	// 再尝试查询公网 IPv6。
	if ip, err := lookupPublicIP(ctx, "https://api64.ipify.org"); err == nil {
		parsedIP := net.ParseIP(ip)
		switch {
		case parsedIP == nil:
			logger.Warn("公网IP返回值无法解析", "值", ip)
		case parsedIP.To4() != nil:
			snapshot.PublicIPv4 = append(snapshot.PublicIPv4, ip)
		default:
			snapshot.PublicIPv6 = append(snapshot.PublicIPv6, ip)
		}
	} else {
		logger.Warn("获取公网IPv6失败", "错误", err)
	}

	snapshot.Normalize()
	return snapshot, nil
}

// extractIP 从 net.Addr 中提取 IP。
func extractIP(address net.Addr) net.IP {
	switch value := address.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}

// lookupPublicIP 调用公网服务获取当前出口 IP。
func lookupPublicIP(ctx context.Context, url string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("构建公网IP请求失败：%w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("请求公网IP失败：%w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 256))
	if err != nil {
		return "", fmt.Errorf("读取公网IP响应失败：%w", err)
	}

	value := strings.TrimSpace(string(body))
	if value == "" {
		return "", fmt.Errorf("公网IP响应为空")
	}
	return value, nil
}
