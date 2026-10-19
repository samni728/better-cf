package main

import (
	"fmt"
	"net/netip"
	"strings"
)

func requiredHTTPSPortsForFamily(settings Settings, ipVersion int) []int {
	if ipVersion == 6 {
		return settings.TrueConnectionRequiredHTTPSPortsIPv6
	}
	if ipVersion == 4 {
		if settings.TrueConnectionIPv4FullHTTPS {
			return append([]int(nil), trueConnectionHTTPSPorts...)
		}
		return settings.TrueConnectionRequiredHTTPSPortsIPv4
	}
	return nil
}

func validateRequiredHTTPSSettings(settings Settings) error {
	if err := validateIPv4MatrixSettings(settings); err != nil {
		return err
	}
	for _, family := range []int{4, 6} {
		required := requiredHTTPSPortsForFamily(settings, family)
		if len(required) == 0 {
			continue
		}
		if !trueConnectionEnabledForFamily(settings, family) || !settings.TrueConnectionHTTPS {
			return fmt.Errorf("IPv%d 配置了必须通过的 HTTPS 端口，必须同时开启该地址族真连接和 HTTPS 测试", family)
		}
		if err := validateSelectedTrueConnectionPorts("HTTPS 必须通过", required, trueConnectionHTTPSPorts); err != nil {
			return fmt.Errorf("IPv%d: %w", family, err)
		}
		selected := effectiveTrueConnectionPorts(settings.TrueConnectionHTTPSPorts, trueConnectionHTTPSPorts)
		for _, port := range required {
			if !connectionGateContainsPort(selected, port) {
				return fmt.Errorf("IPv%d 必须通过的 HTTPS 端口 %d 未包含在已选测试端口中", family, port)
			}
		}
	}
	return nil
}

func connectionGateContainsPort(ports []int, port int) bool {
	for _, selected := range ports {
		if selected == port {
			return true
		}
	}
	return false
}

// TrueConnectionPorts contains only successful tunnel probes. Match both the
// scheme and the currently selected ports so stale or unrelated probes cannot
// satisfy a changed admission policy.
func validateResultTrueConnection(settings Settings, result IPTestResult) error {
	address, err := netip.ParseAddr(strings.TrimSpace(result.IP))
	if err != nil {
		return fmt.Errorf("真连接结果 IP %q 无效", result.IP)
	}
	family := 6
	if address.Unmap().Is4() {
		family = 4
	}
	if result.IPVersion != family {
		return fmt.Errorf("IP %s 的结果地址族 IPv%d 与实际 IPv%d 不一致", result.IP, result.IPVersion, family)
	}
	required := requiredHTTPSPortsForFamily(settings, family)
	if len(required) > 0 {
		if err := validateRequiredHTTPSSettings(settings); err != nil {
			return err
		}
	}
	if !trueConnectionEnabledForFamily(settings, family) {
		return nil
	}
	if family == 4 && settings.TrueConnectionIPv4FullHTTPS {
		if !result.IPv4DirectIngressVerified || result.IPv4MatrixFingerprint != ipv4MatrixFingerprint(settings) {
			return fmt.Errorf("IP %s 缺少当前 IPv4 HTTPS 全端口模式的直连验证凭据，禁止准入", result.IP)
		}
	}
	if !result.TrueConnectionTested {
		return fmt.Errorf("IP %s 未完成真连接验证，禁止准入", result.IP)
	}
	hasSuccess := false
	httpsSuccess := make(map[int]bool)
	for _, probe := range result.TrueConnectionPorts {
		switch {
		case strings.EqualFold(probe.Scheme, "HTTP") && settings.TrueConnectionHTTP:
			if connectionGateContainsPort(effectiveTrueConnectionPorts(settings.TrueConnectionHTTPPorts, trueConnectionHTTPPorts), probe.Port) {
				hasSuccess = true
			}
		case strings.EqualFold(probe.Scheme, "HTTPS") && settings.TrueConnectionHTTPS:
			if connectionGateContainsPort(effectiveTrueConnectionPorts(settings.TrueConnectionHTTPSPorts, trueConnectionHTTPSPorts), probe.Port) {
				hasSuccess = true
				httpsSuccess[probe.Port] = true
			}
		}
	}
	if !hasSuccess {
		return fmt.Errorf("IP %s 没有通过已启用协议及所选端口的真连接验证", result.IP)
	}
	for _, port := range required {
		if !httpsSuccess[port] {
			return fmt.Errorf("IP %s 未通过必须的 HTTPS 端口 %d，禁止准入", result.IP, port)
		}
	}
	return nil
}

func validateDNSConnectionResults(settings Settings, results []IPTestResult) error {
	if err := validateRequiredHTTPSSettings(settings); err != nil {
		return err
	}
	for _, result := range results {
		if err := validateResultTrueConnection(settings, result); err != nil {
			return err
		}
	}
	return nil
}
