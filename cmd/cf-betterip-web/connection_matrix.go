package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"cf-betterip-ser/internal/searchmemory"
)

// Version the full policy so historical evidence cannot cross policy changes.
const ipv4MatrixPolicyVersion = "ipv4-https-six-ports-direct-ingress-v1"

func effectiveIPv4HTTPSNode(settings Settings, port int) string {
	if node := strings.TrimSpace(settings.TrueConnectionIPv4HTTPSNodes[strconv.Itoa(port)]); node != "" {
		return node
	}
	return strings.TrimSpace(settings.TrueConnectionHTTPSNode)
}

func ipv4MatrixFingerprint(settings Settings) string {
	if !settings.TrueConnectionIPv4FullHTTPS {
		return ""
	}
	parts := []string{ipv4MatrixPolicyVersion, strings.TrimSpace(settings.TrueConnectionTestURL)}
	for _, port := range trueConnectionHTTPSPorts {
		parts = append(parts, strconv.Itoa(port), searchmemory.SecretFingerprint(effectiveIPv4HTTPSNode(settings, port)))
	}
	encoded, _ := json.Marshal(parts)
	return searchmemory.SecretFingerprint(string(encoded))
}

func validateIPv4MatrixSettings(settings Settings) error {
	for key := range settings.TrueConnectionIPv4HTTPSNodes {
		port, err := strconv.Atoi(key)
		if err != nil || strconv.Itoa(port) != key || !connectionGateContainsPort(trueConnectionHTTPSPorts, port) {
			return fmt.Errorf("IPv4 HTTPS 端口模板含不支持的端口键")
		}
	}
	if !settings.TrueConnectionIPv4FullHTTPS {
		return nil
	}
	if !settings.TrueConnectionIPv4 || !settings.TrueConnectionHTTPS {
		return fmt.Errorf("IPv4 HTTPS 全端口模式必须开启 IPv4 真连接和 HTTPS 测试")
	}
	selected := effectiveTrueConnectionPorts(settings.TrueConnectionHTTPSPorts, trueConnectionHTTPSPorts)
	for _, port := range trueConnectionHTTPSPorts {
		if !connectionGateContainsPort(selected, port) {
			return fmt.Errorf("IPv4 HTTPS 全端口模式必须选择端口 %d", port)
		}
		if node, err := parseTrueConnectionNode(effectiveIPv4HTTPSNode(settings, port), true); err != nil || node.AllowInsecure {
			// Parser errors may contain user supplied URLs. Do not expose credentials.
			return fmt.Errorf("IPv4 HTTPS 端口 %d 缺少有效且开启证书校验的 TLS 节点模板", port)
		}
	}
	return nil
}

func buildTrueConnectionVariantsForFamily(settings Settings, family int) ([]trueConnectionVariant, error) {
	if family != 4 || !settings.TrueConnectionIPv4FullHTTPS {
		return buildTrueConnectionVariants(settings)
	}
	if err := validateIPv4MatrixSettings(settings); err != nil {
		return nil, err
	}
	variants := make([]trueConnectionVariant, 0, len(trueConnectionHTTPSPorts))
	for _, port := range trueConnectionHTTPSPorts {
		node, err := parseTrueConnectionNode(effectiveIPv4HTTPSNode(settings, port), true)
		if err != nil {
			return nil, fmt.Errorf("IPv4 HTTPS 端口 %d 节点模板不可用", port)
		}
		variants = append(variants, trueConnectionVariant{Scheme: "HTTPS", Port: port, Node: node})
	}
	return variants, nil
}
