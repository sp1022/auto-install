package envcmd

import (
	"strings"
	"testing"

	"github.com/example/pg-deploy/pkg/config"
)

func TestBuildDestroyPlan_PatroniIncludesEtcdAssets(t *testing.T) {
	cfg := &config.Config{
		DeployMode: config.ModePatroni,
		PGSoftDir:  "/usr/local/pgsql",
		Groups: []*config.GroupConfig{
			{
				ID: 0,
				Nodes: []*config.NodeConfig{
					{
						Name:     "pg1",
						Host:     "10.0.0.1",
						Port:     5432,
						DataDir:  "/data/pg1",
						WALDir:   "/wal/pg1",
						PGLogDir: "/log/pg1",
					},
				},
			},
		},
	}

	plan := BuildDestroyPlan(cfg, DestroyOptions{})

	if len(plan.EtcdServices["10.0.0.1"]) != 1 || plan.EtcdServices["10.0.0.1"][0] != "etcd" {
		t.Fatalf("expected etcd service to be included, got %#v", plan.EtcdServices["10.0.0.1"])
	}
	assertContains(t, plan.EtcdFiles["10.0.0.1"], "/etc/systemd/system/etcd.service")
	assertContains(t, plan.EtcdFiles["10.0.0.1"], "/etc/etcd/etcd.yml")
	assertContains(t, plan.Hosts["10.0.0.1"], "/var/lib/etcd")
}

func TestBuildDestroyPlan_KeepDataOmitsEtcdDataDir(t *testing.T) {
	cfg := &config.Config{
		DeployMode: config.ModePatroni,
		PGSoftDir:  "/usr/local/pgsql",
		Groups: []*config.GroupConfig{
			{
				ID: 0,
				Nodes: []*config.NodeConfig{
					{
						Name:     "pg1",
						Host:     "10.0.0.1",
						Port:     5432,
						DataDir:  "/data/pg1",
						WALDir:   "/wal/pg1",
						PGLogDir: "/log/pg1",
					},
				},
			},
		},
	}

	plan := BuildDestroyPlan(cfg, DestroyOptions{KeepData: true})

	assertNotContains(t, plan.Hosts["10.0.0.1"], "/var/lib/etcd")
	assertNotContains(t, plan.Hosts["10.0.0.1"], "/data/pg1")
	assertNotContains(t, plan.Hosts["10.0.0.1"], "/wal/pg1")
}

func TestPatroniPauseCommand_UsesNodeSpecificConfig(t *testing.T) {
	cmd := patroniPauseCommand("pg17-dev-pg00")

	if !strings.Contains(cmd, "patronictl -c '/etc/patroni/pg17-dev-pg00.yml' pause --wait") {
		t.Fatalf("expected node-specific patroni config, got %q", cmd)
	}
}

func TestSystemctlStopDisableCommand_BatchesAndSortsServices(t *testing.T) {
	cmd := systemctlStopDisableCommand([]string{"patroni-pg01", "patroni-pg00"})

	expected := "systemctl stop patroni-pg00 patroni-pg01 2>/dev/null || true && systemctl disable patroni-pg00 patroni-pg01 2>/dev/null || true"
	if cmd != expected {
		t.Fatalf("expected %q, got %q", expected, cmd)
	}
}

func TestPatroniDCSCleanupCommand_DeletesScopePrefix(t *testing.T) {
	cmd := patroniDCSCleanupCommand()

	expected := "ETCDCTL_API=3 etcdctl del /service/pg-cluster --prefix"
	if !strings.Contains(cmd, expected) {
		t.Fatalf("expected %q in %q", expected, cmd)
	}
}

func TestBuildArtifactDiscoveryCommand_HandlesEmptyLists(t *testing.T) {
	cmd := buildArtifactDiscoveryCommand(nil, nil, nil, nil)

	if !strings.Contains(cmd, "for svc in ''; do") {
		t.Fatalf("expected empty shell loop token in %q", cmd)
	}
}

func TestApplyDiscoveredArtifacts_AddsRuntimeAndDataPaths(t *testing.T) {
	plan := &DestroyPlan{
		Hosts:           map[string][]string{"10.0.0.1": {}},
		PatroniServices: map[string][]string{"10.0.0.1": {"patroni-pg00"}},
		PatroniFiles:    map[string][]string{"10.0.0.1": {"/etc/patroni/pg00.yml"}},
		EtcdServices:    map[string][]string{"10.0.0.1": {"etcd"}},
		EtcdFiles:       map[string][]string{"10.0.0.1": {"/etc/etcd/etcd.yml"}},
	}

	output := strings.Join([]string{
		"PATH\t/custom/etcd-data",
		"PATH\t/custom/patroni-venv",
		"FILE\t/etc/systemd/system/patroni-pg00.service",
		"PATRONI_CFG\t/custom/patroni/pg00.yml",
		"ETCD_CFG\t/custom/etcd/etcd.yml",
	}, "\n")

	applyDiscoveredArtifacts(plan, "10.0.0.1", output)

	assertContains(t, plan.Hosts["10.0.0.1"], "/custom/etcd-data")
	assertContains(t, plan.Hosts["10.0.0.1"], "/custom/patroni-venv")
	assertContains(t, plan.PatroniFiles["10.0.0.1"], "/etc/systemd/system/patroni-pg00.service")
	assertContains(t, plan.PatroniFiles["10.0.0.1"], "/custom/patroni/pg00.yml")
	assertContains(t, plan.EtcdFiles["10.0.0.1"], "/custom/etcd/etcd.yml")
}

func assertContains(t *testing.T, items []string, expected string) {
	t.Helper()
	for _, item := range items {
		if item == expected {
			return
		}
	}
	t.Fatalf("expected %q in %#v", expected, items)
}

func assertNotContains(t *testing.T, items []string, unexpected string) {
	t.Helper()
	for _, item := range items {
		if item == unexpected {
			t.Fatalf("did not expect %q in %#v", unexpected, items)
		}
	}
}
