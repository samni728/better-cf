package main

import (
	"fmt"
	"strings"
)

func requiredHTTPSPlanText(settings Settings) string {
	var parts []string
	for _, family := range []int{4, 6} {
		ports := requiredHTTPSPortsForFamily(settings, family)
		if len(ports) > 0 {
			parts = append(parts, fmt.Sprintf("IPv%d 必须通过 HTTPS:%s", family, trueConnectionPortList(ports, nil)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "；" + strings.Join(parts, "；")
}

// A historical snapshot must not silently bypass the user's current port policy.
func validateResumeConnectionPolicy(snapshot, current Settings) error {
	if snapshot.TrueConnectionIPv4FullHTTPS != current.TrueConnectionIPv4FullHTTPS {
		return fmt.Errorf("IPv4 HTTPS 全端口模式与原任务不同，请新建任务以使用当前准入规则")
	}
	if current.TrueConnectionIPv4FullHTTPS && snapshot.TrueConnectionIPv4MatrixFingerprint != ipv4MatrixFingerprint(current) {
		return fmt.Errorf("IPv4 HTTPS 端口模板或验证规则与原任务不同，请新建任务以使用当前准入规则")
	}
	for _, family := range []int{4, 6} {
		if trueConnectionEnabledForFamily(snapshot, family) != trueConnectionEnabledForFamily(current, family) {
			return fmt.Errorf("IPv%d 真连接开关与原任务不同，请新建任务以使用当前准入规则", family)
		}
		if trueConnectionEnabledForFamily(current, family) {
			if snapshot.TrueConnectionHTTP != current.TrueConnectionHTTP || snapshot.TrueConnectionHTTPS != current.TrueConnectionHTTPS {
				return fmt.Errorf("IPv%d 真连接协议与原任务不同，请新建任务以使用当前准入规则", family)
			}
			if current.TrueConnectionHTTP && !sameConnectionPortSet(effectiveTrueConnectionPorts(snapshot.TrueConnectionHTTPPorts, trueConnectionHTTPPorts), effectiveTrueConnectionPorts(current.TrueConnectionHTTPPorts, trueConnectionHTTPPorts)) {
				return fmt.Errorf("IPv%d HTTP 测试端口与原任务不同，请新建任务以使用当前准入规则", family)
			}
			if current.TrueConnectionHTTPS && !sameConnectionPortSet(effectiveTrueConnectionPorts(snapshot.TrueConnectionHTTPSPorts, trueConnectionHTTPSPorts), effectiveTrueConnectionPorts(current.TrueConnectionHTTPSPorts, trueConnectionHTTPSPorts)) {
				return fmt.Errorf("IPv%d HTTPS 测试端口与原任务不同，请新建任务以使用当前准入规则", family)
			}
		}
		old := requiredHTTPSPortsForFamily(snapshot, family)
		next := requiredHTTPSPortsForFamily(current, family)
		if len(old) != len(next) {
			return fmt.Errorf("IPv%d 必需端口与原任务不同，请新建任务以使用当前准入规则", family)
		}
		selected := make(map[int]bool, len(old))
		for _, port := range old {
			selected[port] = true
		}
		for _, port := range next {
			if !selected[port] {
				return fmt.Errorf("IPv%d 必需端口与原任务不同，请新建任务以使用当前准入规则", family)
			}
		}
	}
	return nil
}

func sameConnectionPortSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	ports := make(map[int]bool, len(a))
	for _, p := range a {
		ports[p] = true
	}
	for _, p := range b {
		if !ports[p] {
			return false
		}
	}
	return true
}
