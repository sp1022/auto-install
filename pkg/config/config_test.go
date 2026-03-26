package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.conf")

	// 创建一个假的源码包
	fakeSource := filepath.Join(tmpDir, "postgresql.tar.gz")
	if err := os.WriteFile(fakeSource, []byte("fake content"), 0644); err != nil {
		t.Fatalf("failed to create fake source file: %v", err)
	}

	configContent := fmt.Sprintf(`
# Test configuration
ssh_user: root
ssh_password: testpass
deploy_mode: patroni
build_mode: compile
pg_source: %s
pg_soft_dir: /usr/local/pgsql
extensions: citus,pg_stat_statements
group_0: 0|pg0|coordinator|192.168.1.10:5432:/data/pgdata::::1,1|pg1|coordinator|192.168.1.11:5432:/data/pgdata::::0
`, fakeSource)

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// 加载配置
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 验证基本配置
	if cfg.SSHUser != "root" {
		t.Errorf("expected SSHUser 'root', got '%s'", cfg.SSHUser)
	}

	if cfg.SSHPassword != "testpass" {
		t.Errorf("expected SSHPassword 'testpass', got '%s'", cfg.SSHPassword)
	}

	if cfg.DeployMode != ModePatroni {
		t.Errorf("expected DeployMode 'patroni', got '%s'", cfg.DeployMode)
	}

	if cfg.BuildMode != BuildCompile {
		t.Errorf("expected BuildMode 'compile', got '%s'", cfg.BuildMode)
	}

	if len(cfg.Extensions) != 2 {
		t.Errorf("expected 2 extensions, got %d", len(cfg.Extensions))
	}

	// 验证组配置
	if len(cfg.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(cfg.Groups))
	}

	group := cfg.Groups[0]
	if group.ID != 0 {
		t.Errorf("expected group ID 0, got %d", group.ID)
	}

	if len(group.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(group.Nodes))
	}

	// 验证主节点
	node1 := group.Nodes[0]
	if node1.Host != "192.168.1.10" {
		t.Errorf("expected host '192.168.1.10', got '%s'", node1.Host)
	}

	if node1.Port != 5432 {
		t.Errorf("expected port 5432, got %d", node1.Port)
	}

	if !node1.IsMaster {
		t.Error("expected node1 to be master")
	}

	// 验证从节点
	node2 := group.Nodes[1]
	if node2.Host != "192.168.1.11" {
		t.Errorf("expected host '192.168.1.11', got '%s'", node2.Host)
	}

	if node2.IsMaster {
		t.Error("expected node2 to be slave")
	}
}

func TestLoad_ResolvesOfflinePackagePathsRelativeToConfig(t *testing.T) {
	tmpDir := t.TempDir()
	softDir := filepath.Join(tmpDir, "soft")
	if err := os.MkdirAll(softDir, 0755); err != nil {
		t.Fatalf("failed to create soft dir: %v", err)
	}

	fakeSource := filepath.Join(softDir, "postgresql.tar.gz")
	fakePatroni := filepath.Join(softDir, "patroni-runtime.tar.gz")
	fakeWheelhouse := filepath.Join(softDir, "patroni-wheelhouse.tar.gz")
	fakeEtcd := filepath.Join(softDir, "etcd-runtime.tar.gz")
	for _, path := range []string{fakeSource, fakePatroni, fakeWheelhouse, fakeEtcd} {
		if err := os.WriteFile(path, []byte("fake content"), 0644); err != nil {
			t.Fatalf("failed to create fake artifact %s: %v", path, err)
		}
	}

	configPath := filepath.Join(tmpDir, "deploy.conf")
	configContent := `
ssh_user: root
deploy_mode: patroni
build_mode: compile
pg_source: soft/postgresql.tar.gz
pg_soft_dir: /usr/local/pgsql
patroni_package: soft/patroni-runtime.tar.gz
patroni_wheelhouse: soft/patroni-wheelhouse.tar.gz
etcd_package: soft/etcd-runtime.tar.gz
group_0: 0|pg0|coordinator|127.0.0.1:5432:/data/pgdata::::1
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.PGSource != fakeSource {
		t.Fatalf("expected resolved pg_source %s, got %s", fakeSource, cfg.PGSource)
	}
	if cfg.PatroniPackage != fakePatroni {
		t.Fatalf("expected resolved patroni_package %s, got %s", fakePatroni, cfg.PatroniPackage)
	}
	if cfg.PatroniWheelhouse != fakeWheelhouse {
		t.Fatalf("expected resolved patroni_wheelhouse %s, got %s", fakeWheelhouse, cfg.PatroniWheelhouse)
	}
	if cfg.EtcdPackage != fakeEtcd {
		t.Fatalf("expected resolved etcd_package %s, got %s", fakeEtcd, cfg.EtcdPackage)
	}
}

func TestLoad_AutoDetectsPatroniRuntimeDirectoriesInSoft(t *testing.T) {
	tmpDir := t.TempDir()
	softDir := filepath.Join(tmpDir, "soft")
	patroniDir := filepath.Join(softDir, "patroni-runtime", "bin")
	etcdDir := filepath.Join(softDir, "etcd-runtime", "bin")

	for _, dir := range []string{patroniDir, etcdDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create runtime dir %s: %v", dir, err)
		}
	}

	for _, path := range []string{
		filepath.Join(patroniDir, "python3"),
		filepath.Join(patroniDir, "patroni"),
		filepath.Join(patroniDir, "patronictl"),
		filepath.Join(etcdDir, "etcd"),
		filepath.Join(etcdDir, "etcdctl"),
	} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("failed to create runtime executable %s: %v", path, err)
		}
	}

	configPath := filepath.Join(tmpDir, "deploy.conf")
	configContent := `
ssh_user: root
deploy_mode: patroni
build_mode: distribute
pg_source: /tmp/postgresql.tar.gz
pg_soft_dir: /usr/local/pgsql
group_0: 0|pg0|coordinator|127.0.0.1:5432:/data/pgdata::::1
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	expectedPatroni := filepath.Join(softDir, "patroni-runtime")
	expectedEtcd := filepath.Join(softDir, "etcd-runtime")
	if cfg.PatroniPackage != expectedPatroni {
		t.Fatalf("expected auto-detected patroni runtime %s, got %s", expectedPatroni, cfg.PatroniPackage)
	}
	if cfg.EtcdPackage != expectedEtcd {
		t.Fatalf("expected auto-detected etcd runtime %s, got %s", expectedEtcd, cfg.EtcdPackage)
	}
}

func TestLoad_AutoDetectsFlatRuntimeFilesInSoft(t *testing.T) {
	tmpDir := t.TempDir()
	softDir := filepath.Join(tmpDir, "soft")
	if err := os.MkdirAll(softDir, 0755); err != nil {
		t.Fatalf("failed to create soft dir: %v", err)
	}

	for _, path := range []string{
		filepath.Join(softDir, "python3"),
		filepath.Join(softDir, "patroni"),
		filepath.Join(softDir, "patronictl"),
		filepath.Join(softDir, "etcd"),
		filepath.Join(softDir, "etcdctl"),
	} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("failed to create runtime executable %s: %v", path, err)
		}
	}

	configPath := filepath.Join(tmpDir, "deploy.conf")
	configContent := `
ssh_user: root
deploy_mode: patroni
build_mode: distribute
pg_source: /tmp/postgresql.tar.gz
pg_soft_dir: /usr/local/pgsql
group_0: 0|pg0|coordinator|127.0.0.1:5432:/data/pgdata::::1
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.PatroniPackage != softDir {
		t.Fatalf("expected auto-detected flat patroni runtime %s, got %s", softDir, cfg.PatroniPackage)
	}
	if cfg.EtcdPackage != softDir {
		t.Fatalf("expected auto-detected flat etcd runtime %s, got %s", softDir, cfg.EtcdPackage)
	}
}

func TestLoad_AutoDetectsPatroniWheelhouseInSoft(t *testing.T) {
	tmpDir := t.TempDir()
	softDir := filepath.Join(tmpDir, "soft")
	if err := os.MkdirAll(softDir, 0755); err != nil {
		t.Fatalf("failed to create soft dir: %v", err)
	}

	wheelhouse := filepath.Join(softDir, "patroni-wheelhouse-debian-amd64.tar.gz")
	etcdPkg := filepath.Join(softDir, "etcd-linux-amd64.tar.gz")
	for _, path := range []string{wheelhouse, etcdPkg} {
		if err := os.WriteFile(path, []byte("fake content"), 0644); err != nil {
			t.Fatalf("failed to create artifact %s: %v", path, err)
		}
	}

	configPath := filepath.Join(tmpDir, "deploy.conf")
	configContent := `
ssh_user: root
deploy_mode: patroni
build_mode: distribute
pg_source: /tmp/postgresql.tar.gz
pg_soft_dir: /usr/local/pgsql
group_0: 0|pg0|coordinator|127.0.0.1:5432:/data/pgdata::::1
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.PatroniWheelhouse != wheelhouse {
		t.Fatalf("expected auto-detected patroni wheelhouse %s, got %s", wheelhouse, cfg.PatroniWheelhouse)
	}
	if cfg.EtcdPackage != etcdPkg {
		t.Fatalf("expected auto-detected etcd package %s, got %s", etcdPkg, cfg.EtcdPackage)
	}
}

func TestValidate_MissingRequiredFields(t *testing.T) {
	cfg := &Config{}

	// 缺少 ssh_user
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for missing ssh_user")
	}

	// 添加 ssh_user，但缺少 deploy_mode
	cfg.SSHUser = "root"
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for missing deploy_mode")
	}

	// 添加 deploy_mode，但缺少 build_mode
	cfg.DeployMode = ModeStandalone
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for missing build_mode")
	}

	// 添加 build_mode，但缺少 pg_soft_dir
	cfg.BuildMode = BuildCompile
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for missing pg_soft_dir")
	}

	// 添加 pg_soft_dir，但缺少组
	cfg.PGSoftDir = "/usr/local/pgsql"
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for missing groups")
	}
}

func TestGetMasterNodes(t *testing.T) {
	cfg := &Config{
		Groups: []*GroupConfig{
			{
				ID:   0,
				Name: "group0",
				Nodes: []*NodeConfig{
					{ID: 0, Host: "host1", Port: 5432, IsMaster: true},
					{ID: 1, Host: "host2", Port: 5432, IsMaster: false},
				},
			},
			{
				ID:   1,
				Name: "group1",
				Nodes: []*NodeConfig{
					{ID: 2, Host: "host3", Port: 5432, IsMaster: true},
					{ID: 3, Host: "host4", Port: 5432, IsMaster: false},
				},
			},
		},
	}

	masters := cfg.GetMasterNodes()
	if len(masters) != 2 {
		t.Errorf("expected 2 master nodes, got %d", len(masters))
	}

	for _, master := range masters {
		if !master.IsMaster {
			t.Errorf("expected node %d to be master", master.ID)
		}
	}
}

func TestGetAllNodes(t *testing.T) {
	cfg := &Config{
		Groups: []*GroupConfig{
			{
				ID:   0,
				Name: "group0",
				Nodes: []*NodeConfig{
					{ID: 0, Host: "host1", Port: 5432},
					{ID: 1, Host: "host2", Port: 5432},
				},
			},
			{
				ID:   1,
				Name: "group1",
				Nodes: []*NodeConfig{
					{ID: 2, Host: "host3", Port: 5432},
				},
			},
		},
	}

	allNodes := cfg.GetAllNodes()
	if len(allNodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(allNodes))
	}
}

func TestSave(t *testing.T) {
	cfg := &Config{
		SSHUser:     "root",
		SSHPassword: "testpass",
		DeployMode:  ModePatroni,
		BuildMode:   BuildDistribute,
		PGSoftDir:   "/usr/local/pgsql",
		Extensions:  []string{"citus", "pg_stat_statements"},
		Groups: []*GroupConfig{
			{
				ID:   0,
				Name: "pg0",
				Role: "coordinator",
				Nodes: []*NodeConfig{
					{
						ID:       0,
						Name:     "node0",
						Role:     "coordinator",
						Host:     "192.168.1.10",
						Port:     5432,
						DataDir:  "/data/pgdata",
						WALDir:   "/data/pgwal",
						PGLogDir: "/var/log/pglog",
						IsMaster: true,
					},
				},
			},
		},
	}

	tmpDir := t.TempDir()
	savePath := filepath.Join(tmpDir, "saved.conf")

	// 保存配置
	if err := cfg.Save(savePath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 重新加载
	loaded, err := Load(savePath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 验证
	if loaded.SSHUser != cfg.SSHUser {
		t.Errorf("expected SSHUser '%s', got '%s'", cfg.SSHUser, loaded.SSHUser)
	}

	if len(loaded.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(loaded.Groups))
	}

	if loaded.SSHPassword != "" {
		t.Fatalf("expected SSH password not to be persisted")
	}
}

func TestLoad_UsesEnvPasswordWhenConfigOmitsIt(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.conf")

	originalPassword := os.Getenv("SSH_PASSWORD")
	defer os.Setenv("SSH_PASSWORD", originalPassword)
	os.Setenv("SSH_PASSWORD", "env-secret")

	configContent := `
ssh_user: root
deploy_mode: standalone
build_mode: distribute
pg_soft_dir: /usr/local/pgsql
group_0: 0|pg0|primary|192.168.1.10:5432:/data/pgdata::::1
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.SSHPassword != "env-secret" {
		t.Fatalf("expected env password to be loaded, got %q", cfg.SSHPassword)
	}
}

func TestValidate_RejectsDangerousPGSoftDir(t *testing.T) {
	cfg := &Config{
		SSHUser:    "root",
		DeployMode: ModeStandalone,
		BuildMode:  BuildDistribute,
		PGSoftDir:  "/",
		Groups: []*GroupConfig{
			{
				ID: 0,
				Nodes: []*NodeConfig{
					{ID: 0, Name: "pg0", Host: "127.0.0.1", Port: 5432, DataDir: "/data/pgdata", IsMaster: true},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation to reject dangerous pg_soft_dir")
	}
}

func TestApplyEnvironment_RewritesTemplatesAndNames(t *testing.T) {
	cfg := &Config{
		EnvironmentName: "pg15-dev",
		PGSoftDir:       "/srv/postgres/{env}/binroot",
		Groups: []*GroupConfig{
			{
				ID:   0,
				Name: "primary",
				Nodes: []*NodeConfig{
					{
						ID:       0,
						Name:     "node0",
						Host:     "127.0.0.1",
						Port:     5432,
						DataDir:  "/data/{env}/main",
						PGLogDir: "/log/{prefix}",
						IsMaster: true,
					},
				},
			},
		},
	}

	cfg.ApplyEnvironment()

	if cfg.EnvironmentPrefix != "pg15-dev" {
		t.Fatalf("expected env prefix default to env name, got %q", cfg.EnvironmentPrefix)
	}
	if cfg.PGSoftDir != "/srv/postgres/pg15-dev/binroot" {
		t.Fatalf("unexpected PGSoftDir: %q", cfg.PGSoftDir)
	}
	if cfg.Groups[0].Name != "pg15-dev-primary" {
		t.Fatalf("unexpected group name: %q", cfg.Groups[0].Name)
	}
	if cfg.Groups[0].Nodes[0].Name != "pg15-dev-node0" {
		t.Fatalf("unexpected node name: %q", cfg.Groups[0].Nodes[0].Name)
	}
	if cfg.Groups[0].Nodes[0].DataDir != "/data/pg15-dev/main" {
		t.Fatalf("unexpected data dir: %q", cfg.Groups[0].Nodes[0].DataDir)
	}
	if cfg.Groups[0].Nodes[0].PGLogDir != "/log/pg15-dev" {
		t.Fatalf("unexpected pg log dir: %q", cfg.Groups[0].Nodes[0].PGLogDir)
	}
}

func TestLoad_SupportsShorthandNodesAfterFirstFullNode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.conf")

	configContent := `
ssh_user: root
deploy_mode: master-slave
build_mode: distribute
pg_soft_dir: /usr/local/pgsql
group_0: 0|pgtest|primary|10.0.0.1:5432:/data/master::::1,10.0.0.2:5432:/data/slave1::::0,10.0.0.3:5432:/data/slave2::::0
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.Groups) != 1 || len(cfg.Groups[0].Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %+v", cfg.Groups)
	}
	if cfg.Groups[0].Nodes[1].Role != "standby" {
		t.Fatalf("expected shorthand slave role to be standby, got %q", cfg.Groups[0].Nodes[1].Role)
	}
	if cfg.Groups[0].Nodes[2].Name != "group0node2" && cfg.Groups[0].Nodes[2].Name != "pgtest2" {
		t.Fatalf("expected shorthand node name derived from group, got %q", cfg.Groups[0].Nodes[2].Name)
	}
}
