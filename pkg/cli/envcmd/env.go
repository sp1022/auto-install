package envcmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/example/pg-deploy/pkg/cli/common"
	"github.com/example/pg-deploy/pkg/config"
	"github.com/example/pg-deploy/pkg/executor"
	"github.com/example/pg-deploy/pkg/logger"
	"github.com/spf13/cobra"
)

func NewCommand(log *logger.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "管理多环境",
	}

	cmd.AddCommand(newListCommand(log))
	cmd.AddCommand(newDestroyCommand(log))

	return cmd
}

func newListCommand(log *logger.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出当前配置对应的环境信息",
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile, _ := cmd.Flags().GetString("config")
			if configFile == "" {
				return fmt.Errorf("请指定配置文件 (-c)")
			}
			if _, err := os.Stat(configFile); os.IsNotExist(err) {
				return fmt.Errorf("配置文件不存在: %s", configFile)
			}

			cfg, err := common.LoadConfig(configFile)
			if err != nil {
				return err
			}

			exec, err := common.BuildExecutor(cfg, log)
			if err != nil {
				return err
			}

			printEnvironmentSummary(cfg, collectEnvironmentStatus(cfg, exec))
			return nil
		},
	}
}

func newDestroyCommand(log *logger.Logger) *cobra.Command {
	var force bool
	var keepBinaries bool
	var keepData bool
	var keepLogs bool

	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "清理当前配置对应的环境",
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile, _ := cmd.Flags().GetString("config")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			if configFile == "" {
				return fmt.Errorf("请指定配置文件 (-c)")
			}
			if _, err := os.Stat(configFile); os.IsNotExist(err) {
				return fmt.Errorf("配置文件不存在: %s", configFile)
			}

			cfg, err := common.LoadConfig(configFile)
			if err != nil {
				return err
			}

			if !force && !dryRun {
				return fmt.Errorf("destroy-env 是破坏性操作，请显式传入 --yes")
			}

			exec, err := common.BuildExecutor(cfg, log)
			if err != nil {
				return err
			}

			plan := buildDestroyPlan(cfg, destroyOptions{
				KeepBinaries: keepBinaries,
				KeepData:     keepData,
				KeepLogs:     keepLogs,
			})
			printDestroyPlan(cfg, plan, dryRun)
			if dryRun {
				return nil
			}

			return executeDestroyPlan(cfg, exec, log, plan)
		},
	}

	cmd.Flags().BoolVar(&force, "yes", false, "确认执行清理")
	cmd.Flags().BoolVar(&keepBinaries, "keep-binaries", false, "保留 PostgreSQL 安装目录")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "保留数据目录和 WAL 目录")
	cmd.Flags().BoolVar(&keepLogs, "keep-logs", false, "保留日志目录")
	return cmd
}

type destroyPlan struct {
	Hosts           map[string][]string
	PatroniServices map[string][]string
	PatroniFiles    map[string][]string
}

type destroyOptions struct {
	KeepBinaries bool
	KeepData     bool
	KeepLogs     bool
}

func buildDestroyPlan(cfg *config.Config, opts destroyOptions) *destroyPlan {
	plan := &destroyPlan{
		Hosts:           make(map[string][]string),
		PatroniServices: make(map[string][]string),
		PatroniFiles:    make(map[string][]string),
	}

	addPath := func(host, path string) {
		if path == "" {
			return
		}
		plan.Hosts[host] = appendUnique(plan.Hosts[host], path)
	}

	addService := func(host, service string) {
		if service == "" {
			return
		}
		plan.PatroniServices[host] = appendUnique(plan.PatroniServices[host], service)
	}

	addFile := func(host, file string) {
		if file == "" {
			return
		}
		plan.PatroniFiles[host] = appendUnique(plan.PatroniFiles[host], file)
	}

	for _, node := range cfg.GetAllNodes() {
		if !opts.KeepData {
			addPath(node.Host, node.DataDir)
			addPath(node.Host, node.WALDir)
		}
		if !opts.KeepLogs {
			addPath(node.Host, node.PGLogDir)
		}
		if cfg.DeployMode == config.ModePatroni {
			addService(node.Host, fmt.Sprintf("patroni-%s", node.Name))
			addFile(node.Host, fmt.Sprintf("/etc/systemd/system/patroni-%s.service", node.Name))
			addFile(node.Host, fmt.Sprintf("/etc/patroni/%s.yml", node.Name))
		}
	}

	if !opts.KeepBinaries {
		for _, host := range uniqueHosts(cfg) {
			addPath(host, cfg.PGSoftDir)
		}
	}

	return plan
}

func executeDestroyPlan(cfg *config.Config, exec *executor.Executor, log *logger.Logger, plan *destroyPlan) error {
	hosts := sortedKeys(plan.Hosts)
	for _, host := range hosts {
		node := &executor.Node{
			ID:       host,
			Host:     host,
			Port:     22,
			User:     cfg.SSHUser,
			Password: cfg.SSHPassword,
		}

		if cfg.DeployMode == config.ModePatroni {
			for _, service := range plan.PatroniServices[host] {
				cmd := fmt.Sprintf("systemctl stop %s 2>/dev/null || true && systemctl disable %s 2>/dev/null || true", service, service)
				if result := exec.RunOnNode(node, cmd, true, false); result.Error != nil {
					log.Warn("Failed to stop Patroni service", logger.Fields{"host": host, "service": service, "error": result.Error})
				}
			}
			if len(plan.PatroniFiles[host]) > 0 {
				cmd := fmt.Sprintf("rm -f %s && systemctl daemon-reload || true", shellJoin(plan.PatroniFiles[host]))
				if result := exec.RunOnNode(node, cmd, true, false); result.Error != nil {
					log.Warn("Failed to remove Patroni files", logger.Fields{"host": host, "error": result.Error})
				}
			}
		}

		for _, path := range plan.Hosts[host] {
			cmd := fmt.Sprintf("test -e %s && rm -rf %s || true", shellQuote(path), shellQuote(path))
			if result := exec.RunOnNode(node, cmd, true, false); result.Error != nil {
				return fmt.Errorf("failed to remove %s on %s: %w", path, host, result.Error)
			}
		}
	}

	return nil
}

type environmentStatus struct {
	Nodes map[string]nodeStatus
	Hosts map[string]hostStatus
}

type nodeStatus struct {
	PostgreSQL string
	Patroni    string
}

type hostStatus struct {
	Etcd string
}

func printEnvironmentSummary(cfg *config.Config, status *environmentStatus) {
	fmt.Printf("Environment: %s\n", valueOrDefault(cfg.EnvironmentName, "(not set)"))
	fmt.Printf("Prefix: %s\n", valueOrDefault(cfg.EnvironmentPrefix, "(not set)"))
	fmt.Printf("Mode: %s\n", cfg.DeployMode)
	fmt.Printf("Build: %s\n", cfg.BuildMode)
	fmt.Printf("PGSoftDir: %s\n", cfg.PGSoftDir)
	fmt.Printf("Nodes: %d\n", len(cfg.GetAllNodes()))

	for _, group := range cfg.Groups {
		fmt.Printf("\nGroup %d: %s (%s)\n", group.ID, group.Name, group.Role)
		for _, node := range group.Nodes {
			fmt.Printf("  - %s %s:%d data=%s", node.Name, node.Host, node.Port, node.DataDir)
			if node.WALDir != "" {
				fmt.Printf(" wal=%s", node.WALDir)
			}
			if node.PGLogDir != "" {
				fmt.Printf(" log=%s", node.PGLogDir)
			}
			if cfg.DeployMode == config.ModePatroni {
				fmt.Printf(" rest=%d", node.Port+1000)
			}
			if status != nil {
				if nodeState, ok := status.Nodes[node.Name]; ok {
					fmt.Printf(" pg=%s", nodeState.PostgreSQL)
					if cfg.DeployMode == config.ModePatroni {
						fmt.Printf(" patroni=%s", nodeState.Patroni)
					}
				}
			}
			fmt.Println()
		}
	}

	if status != nil {
		fmt.Printf("\nHost Status:\n")
		for _, host := range uniqueHosts(cfg) {
			hostState := status.Hosts[host]
			fmt.Printf("  - %s", host)
			if cfg.DeployMode == config.ModePatroni {
				fmt.Printf(" etcd=%s", hostState.Etcd)
			}
			fmt.Println()
		}
	}
}

func printDestroyPlan(cfg *config.Config, plan *destroyPlan, dryRun bool) {
	action := "Destroy plan"
	if dryRun {
		action = "Destroy dry-run"
	}
	fmt.Printf("%s for environment %s\n", action, valueOrDefault(cfg.EnvironmentName, "(not set)"))

	for _, host := range sortedKeys(plan.Hosts) {
		fmt.Printf("\nHost: %s\n", host)
		if len(plan.PatroniServices[host]) > 0 {
			fmt.Printf("  Patroni services: %s\n", strings.Join(plan.PatroniServices[host], ", "))
		}
		if len(plan.PatroniFiles[host]) > 0 {
			fmt.Printf("  Patroni files: %s\n", strings.Join(plan.PatroniFiles[host], ", "))
		}
		fmt.Printf("  Paths: %s\n", strings.Join(plan.Hosts[host], ", "))
	}
}

func sortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func valueOrDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func shellJoin(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, shellQuote(value))
	}
	return strings.Join(quoted, " ")
}

func uniqueHosts(cfg *config.Config) []string {
	seen := make(map[string]bool)
	hosts := make([]string, 0)
	for _, node := range cfg.GetAllNodes() {
		if seen[node.Host] {
			continue
		}
		seen[node.Host] = true
		hosts = append(hosts, node.Host)
	}
	sort.Strings(hosts)
	return hosts
}

func collectEnvironmentStatus(cfg *config.Config, exec *executor.Executor) *environmentStatus {
	status := &environmentStatus{
		Nodes: make(map[string]nodeStatus),
		Hosts: make(map[string]hostStatus),
	}

	for _, node := range cfg.GetAllNodes() {
		execNode := &executor.Node{
			ID:       node.Host,
			Host:     node.Host,
			Port:     22,
			User:     cfg.SSHUser,
			Password: cfg.SSHPassword,
		}

		nodeState := nodeStatus{
			PostgreSQL: probePostgreSQL(cfg, exec, execNode, node),
			Patroni:    "n/a",
		}
		if cfg.DeployMode == config.ModePatroni {
			nodeState.Patroni = probePatroni(exec, execNode, node)
		}
		status.Nodes[node.Name] = nodeState
	}

	for _, host := range uniqueHosts(cfg) {
		hostState := hostStatus{Etcd: "n/a"}
		if cfg.DeployMode == config.ModePatroni {
			execNode := &executor.Node{
				ID:       host,
				Host:     host,
				Port:     22,
				User:     cfg.SSHUser,
				Password: cfg.SSHPassword,
			}
			hostState.Etcd = probeEtcd(exec, execNode)
		}
		status.Hosts[host] = hostState
	}

	return status
}

func probePostgreSQL(cfg *config.Config, exec *executor.Executor, execNode *executor.Node, node *config.NodeConfig) string {
	cmd := fmt.Sprintf("%s/bin/pg_isready -h localhost -p %d", cfg.PGSoftDir, node.Port)
	result := exec.RunOnNode(execNode, cmd, false, true)
	if result.Error != nil {
		return "down"
	}
	if strings.Contains(result.Output, "accepting connections") {
		return "up"
	}
	return "unknown"
}

func probePatroni(exec *executor.Executor, execNode *executor.Node, node *config.NodeConfig) string {
	cmd := fmt.Sprintf("systemctl is-active patroni-%s", node.Name)
	result := exec.RunOnNode(execNode, cmd, false, true)
	if result.Error != nil {
		return "down"
	}
	if strings.Contains(result.Output, "active") {
		return "active"
	}
	return "unknown"
}

func probeEtcd(exec *executor.Executor, execNode *executor.Node) string {
	cmd := "ETCDCTL_API=3 $(command -v etcdctl) endpoint health --endpoints=http://127.0.0.1:2379"
	result := exec.RunOnNode(execNode, cmd, false, true)
	if result.Error != nil {
		return "down"
	}
	if strings.Contains(result.Output, "is healthy") {
		return "healthy"
	}
	return "unknown"
}
