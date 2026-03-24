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

	if !strings.Contains(got, "name: etcd0") {
		t.Fatalf("expected member name in etcd config: %s", got)
	}
	if !strings.Contains(got, "initial-cluster: etcd0=http://10.0.0.1:2380,etcd1=http://10.0.0.2:2380,etcd2=http://10.0.0.3:2380") {
		t.Fatalf("expected initial cluster list in etcd config: %s", got)
	}
	if strings.Contains(got, "ETCD_NAME=") {
		t.Fatalf("expected YAML config, got env-style config: %s", got)
	}
}

func TestGeneratePatroniConfig_UsesUniqueRestPortAndSingleHostList(t *testing.T) {
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
	mgr.etcdEndpoints = []string{"http://10.0.0.1:2379", "http://10.0.0.2:2379"}

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
	if strings.Count(got, "hosts:") != 1 {
		t.Fatalf("expected a single etcd hosts section: %s", got)
	}
}
