package main

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

func TestMatrixExecutionRejectsSecondRoundFailure(t *testing.T) {
	variants := []trueConnectionVariant{{Scheme: "HTTPS", Port: 443}, {Scheme: "HTTPS", Port: 2053}}
	counts := map[int]int{}
	probe := func(_ context.Context, _ string, _ trueConnectionNode, _ string, port int, _ string) (int, error) {
		counts[port]++
		if port == 443 && counts[port] == 2 {
			return 0, errors.New("timeout")
		}
		return 10, nil
	}
	ports, attempts, err := runIPv4MatrixTests(context.Background(), Settings{}, "1.1.1.1", variants, "xray", func(context.Context, string) error { return nil }, probe)
	if err != nil || len(ports) != 1 || ports[0].Port != 2053 || len(attempts) != 2 || attempts[0].Success {
		t.Fatalf("failed repeated port admitted: ports=%v attempts=%v err=%v", ports, attempts, err)
	}
	if counts[443] != 2 || counts[2053] != 2 {
		t.Fatal("did not repeat each port")
	}
}

func TestMatrixExecutionIngressFailureSkipsNodes(t *testing.T) {
	called := false
	ports, attempts, err := runIPv4MatrixTests(context.Background(), Settings{}, "1.1.1.1", []trueConnectionVariant{{Scheme: "HTTPS", Port: 443}}, "xray", func(context.Context, string) error { return errors.New("非直连") }, func(context.Context, string, trueConnectionNode, string, int, string) (int, error) {
		called = true
		return 1, nil
	})
	if called || len(ports) != 0 || err != nil || len(attempts) != 1 || attempts[0].Success {
		t.Fatalf("ingress failure did not reject candidate: %v %v", attempts, err)
	}
}

func TestMatrixFormPreservesAndClearsTemplates(t *testing.T) {
	s := Settings{TrueConnectionIPv4HTTPSNodes: map[string]string{"443": "old", "2053": "keep"}}
	applyIPv4MatrixForm(&s, url.Values{"true_connection_ipv4_matrix_ui": {"1"}, "true_connection_ipv4_full_https": {"on"}, "true_connection_ipv4_https_clear_443": {"on"}, "true_connection_ipv4_https_node_8443": {"new"}})
	if !s.TrueConnectionIPv4FullHTTPS || s.TrueConnectionIPv4HTTPSNodes["443"] != "" || s.TrueConnectionIPv4HTTPSNodes["2053"] != "keep" || s.TrueConnectionIPv4HTTPSNodes["8443"] != "new" {
		t.Fatal("matrix form did not preserve/replace/clear")
	}
}

func TestRefreshMatrixSeedCannotReuseOldDirectProof(t *testing.T) {
	s := Settings{TrueConnectionIPv4FullHTTPS: true}
	r := IPTestResult{IP: "1.1.1.1", IPVersion: 4, IPv4DirectIngressVerified: true, IPv4MatrixFingerprint: "old"}
	calls := 0
	err := refreshIPv4MatrixSeed(context.Background(), s, &r, func(context.Context, Settings, string) ([]TrueConnectionPortResult, []trueConnectionPortAttempt, error) {
		calls++
		return nil, nil, nil
	})
	if calls != 1 || err != nil || r.IPv4DirectIngressVerified || r.IPv4MatrixFingerprint != "" {
		t.Fatal("old seed evidence retained without fresh six-port success")
	}
	r.IPVersion = 6
	if err := refreshIPv4MatrixSeed(context.Background(), s, &r, func(context.Context, Settings, string) ([]TrueConnectionPortResult, []trueConnectionPortAttempt, error) {
		t.Fatal("IPv6 was re-probed by IPv4 policy")
		return nil, nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}
