package main

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

type matrixPortProbe func(context.Context, string, trueConnectionNode, string, int, string) (int, error)

func applyIPv4MatrixForm(settings *Settings, form url.Values) {
	if _, ok := form["true_connection_ipv4_matrix_ui"]; !ok {
		return
	}
	settings.TrueConnectionIPv4FullHTTPS = form.Get("true_connection_ipv4_full_https") == "on"
	if settings.TrueConnectionIPv4HTTPSNodes == nil {
		settings.TrueConnectionIPv4HTTPSNodes = make(map[string]string)
	}
	for _, port := range trueConnectionHTTPSPorts {
		key := strconv.Itoa(port)
		if form.Get("true_connection_ipv4_https_clear_"+key) == "on" {
			delete(settings.TrueConnectionIPv4HTTPSNodes, key)
		}
		if node := strings.TrimSpace(form.Get("true_connection_ipv4_https_node_" + key)); node != "" {
			settings.TrueConnectionIPv4HTTPSNodes[key] = node
		}
	}
	settings.TrueConnectionIPv4MatrixFingerprint = ""
}

// Only ports passing both serial probes are returned. A failed ingress check
// rejects this candidate without stopping the search for a different address.
func runIPv4MatrixTests(ctx context.Context, settings Settings, candidateIP string, variants []trueConnectionVariant, xrayBin string, ingress func(context.Context, string) error, probe matrixPortProbe) ([]TrueConnectionPortResult, []trueConnectionPortAttempt, error) {
	results := make([]TrueConnectionPortResult, 0, len(variants))
	attempts := make([]trueConnectionPortAttempt, 0, len(variants))
	if err := ingress(ctx, candidateIP); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		for _, v := range variants {
			attempts = append(attempts, trueConnectionPortAttempt{Scheme: v.Scheme, Port: v.Port, ErrorClass: "direct_ingress_failed"})
		}
		return results, attempts, nil
	}
	for _, v := range variants {
		latency := 0
		var probeErr error
		for round := 0; round < 2; round++ {
			if ctx.Err() != nil {
				return results, attempts, ctx.Err()
			}
			var elapsed int
			elapsed, probeErr = probe(ctx, xrayBin, v.Node, candidateIP, v.Port, settings.TrueConnectionTestURL)
			if probeErr != nil {
				break
			}
			if elapsed > latency {
				latency = elapsed
			}
		}
		attempts = append(attempts, trueConnectionPortAttempt{Scheme: v.Scheme, Port: v.Port, Success: probeErr == nil, LatencyMs: latency, ErrorClass: classifyTrueConnectionError(probeErr)})
		if probeErr == nil {
			results = append(results, TrueConnectionPortResult{Scheme: v.Scheme, Port: v.Port, LatencyMs: latency})
		}
	}
	return results, attempts, ctx.Err()
}

// A resumed run must not reuse a direct-egress assertion made on an earlier
// network path. IPv6 and legacy admission keep their previous resume behavior.
func refreshIPv4MatrixSeed(ctx context.Context, settings Settings, result *IPTestResult, probe func(context.Context, Settings, string) ([]TrueConnectionPortResult, []trueConnectionPortAttempt, error)) error {
	if result.IPVersion != 4 || !settings.TrueConnectionIPv4FullHTTPS {
		return nil
	}
	result.IPv4DirectIngressVerified = false
	result.IPv4MatrixFingerprint = ""
	result.TrueConnectionTested = false
	result.TrueConnectionPorts = nil
	ports, _, err := probe(ctx, settings, result.IP)
	if err != nil {
		return err
	}
	result.TrueConnectionTested = true
	result.TrueConnectionPorts = ports
	if len(ports) == len(trueConnectionHTTPSPorts) {
		result.IPv4DirectIngressVerified = true
		result.IPv4MatrixFingerprint = ipv4MatrixFingerprint(settings)
	}
	return nil
}
