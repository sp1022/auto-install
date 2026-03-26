package topology

import (
	"strings"
	"testing"

	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/logger"
)

func TestGenerateEtcdConfig_UsesYAMLAndClusterEndpoints(t *testing.T) {
	cfg := &config.Config{
		Groups: []*config.GroupConfig{
			{Nodes: []*config.NodeConfig{{Host: "10.0.0.1"}, {Host: "10.0.0.2"}, {Host: "10.0.0.3"}}},
		},
	}

	mgr := NewPatroniManager(cfg, nil, logger.NewDefault())
	got := mgr.generateEtcdConfig(&config.NodeConfig{Host: "10.0.0.1"}, 0, 3)

	if !strings.Contains(got, `name: "etcd0"`) {
		t.Fatalf("expected quoted member name in etcd config: %s", got)
	}
	if !strings.Contains(got, `initial-cluster: "etcd0=http://10.0.0.1:2380,etcd1=http://10.0.0.2:2380,etcd2=http://10.0.0.3:2380"`) {
		t.Fatalf("expected initial cluster list in etcd config: %s", got)
	}
	if !strings.Contains(got, `initial-cluster-state: "new"`) {
		t.Fatalf("expected quoted initial-cluster-state in etcd config: %s", got)
	}
	if !strings.Contains(got, `listen-client-urls: "http://0.0.0.0:2379"`) {
		t.Fatalf("expected single client listen address in etcd config: %s", got)
	}
	if strings.Contains(got, "127.0.0.1:2379") {
		t.Fatalf("expected etcd config to avoid duplicate client bind addresses: %s", got)
	}
	if strings.Contains(got, "ETCD_NAME=") {
		t.Fatalf("expected YAML config, got env-style config: %s", got)
	}
}

func TestGeneratePatroniConfig_UsesUniqueRestPortAndEtcdHostString(t *testing.T) {
	cfg := &config.Config{
		PGSoftDir: "/usr/local/pgsql",
		Groups: []*config.GroupConfig{
			{
				Nodes: []*config.NodeConfig{
					{Name: "node1", Host: "10.0.0.1", Port: 5432, DataDir: "/data/node1"},
					{Name: "node2", Host: "10.0.0.1", Port: 5433, DataDir: "/data/node2"},
				},
			},
		},
	}

	mgr := NewPatroniManager(cfg, nil, logger.NewDefault())
	mgr.etcdEndpoints = []string{"10.0.0.1:2379", "10.0.0.2:2379"}

	got, err := mgr.GeneratePatroniConfig(cfg.Groups[0].Nodes[1])
	if err != nil {
		t.Fatalf("GeneratePatroniConfig failed: %v", err)
	}

	if !strings.Contains(got, "listen: 0.0.0.0:6433") {
		t.Fatalf("expected rest port derived from PG port: %s", got)
	}
	if !strings.Contains(got, "bin_dir: /usr/local/pgsql/bin") {
		t.Fatalf("expected PG bin dir in Patroni config: %s", got)
	}
	if strings.Contains(got, "shared_preload_libraries: 'patroni'") {
		t.Fatalf("unexpected patroni preload library in config: %s", got)
	}
	if !strings.Contains(got, `hosts: "10.0.0.1:2379,10.0.0.2:2379"`) {
		t.Fatalf("expected etcd hosts rendered as a single string: %s", got)
	}
	if !strings.Contains(got, `protocol: "http"`) {
		t.Fatalf("expected etcd protocol in Patroni config: %s", got)
	}
	if !strings.Contains(got, `unix_socket_directories: "/data/node2"`) {
		t.Fatalf("expected unix socket directory in Patroni config: %s", got)
	}
}

func TestGetClusterHealth_TreatsLeaderCaseInsensitively(t *testing.T) {
	members := &PatroniClusterMembers{
		Members: []ClusterMember{
			{Name: "pg00", Role: "Primary", State: "running"},
			{Name: "pg01", Role: "Replica", State: "running"},
		},
	}

	health := &PatroniHealth{
		TotalMembers:   len(members.Members),
		HealthyMembers: 0,
		Leader:         "",
	}
	for _, member := range members.Members {
		if member.State == "running" || member.State == "streaming" {
			health.HealthyMembers++
		}
		if isLeaderRole(member.Role) {
			health.Leader = member.Name
		}
	}

	if health.Leader != "pg00" {
		t.Fatalf("expected pg00 as leader, got %q", health.Leader)
	}
}

func TestIsLeaderRole_AcceptsPatroniLeaderAliases(t *testing.T) {
	for _, role := range []string{"leader", "Leader", "primary", "master", "standby_leader"} {
		if !isLeaderRole(role) {
			t.Fatalf("expected %q to be recognized as leader role", role)
		}
	}
}
