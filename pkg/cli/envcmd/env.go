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
		Short: "查看或清理当前配置对应的环境",
		Long: `env 子命令面向已经存在的目标环境。

常见用途：
  - 查看当前配置对应节点、服务和路径摘要
  - 预演销毁计划
  - 在重建前单独清理旧环境

Patroni 模式下，env destroy 会优先处理 Patroni 服务、etcd 服务、DCS 键以及探测到的真实路径。`,
	}

	cmd.AddCommand(newListCommand(log))
	cmd.AddCommand(newDestroyCommand(log))

	return cmd
}

func newListCommand(log *logger.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出当前配置对应的环境信息",
		Long: `根据当前配置文件，汇总环境名称、节点、服务和关键路径。

这条命令不会修改远端环境，适合在 destroy 或 deploy 前确认当前配置到底会作用到哪些主机和实例。`,
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
		Long: `按当前配置生成销毁计划，并删除该环境对应的服务、配置、数据和运行时目录。

默认会删除：
  - PostgreSQL 数据目录、WAL 目录、日志目录
  - PostgreSQL / Patroni / etcd 安装目录
  - Patroni systemd 服务与 YAML
  - etcd systemd 服务、配置和数据目录
  - Patroni DCS 键

Patroni 模式下，命令会在远端探测真实路径，再执行删除，避免只按固定默认路径清理。`,
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
				return fmt.Errorf("env destroy 是破坏性操作，请显式传入 --yes")
			}

			exec, err := common.BuildExecutor(cfg, log)
			if err != nil {
				return err
			}

			plan := BuildDestroyPlan(cfg, DestroyOptions{
				KeepBinaries: keepBinaries,
				KeepData:     keepData,
				KeepLogs:     keepLogs,
			})
			PrintDestroyPlan(cfg, plan, dryRun)
			if dryRun {
				return nil
			}

			return ExecuteDestroyPlan(cfg, exec, log, plan)
		},
	}

	cmd.Flags().BoolVar(&force, "yes", false, "确认执行破坏性清理")
	cmd.Flags().BoolVar(&keepBinaries, "keep-binaries", false, "保留 PostgreSQL / Patroni / etcd 安装目录")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "保留 PostgreSQL 数据目录和 WAL 目录")
	cmd.Flags().BoolVar(&keepLogs, "keep-logs", false, "保留 PostgreSQL / Patroni / etcd 日志目录")
	return cmd
}

type DestroyPlan struct {
	Hosts           map[string][]string
	PatroniServices map[string][]string
	PatroniFiles    map[string][]string
	EtcdServices    map[string][]string
	EtcdFiles       map[string][]string
}

type DestroyOptions struct {
	KeepBinaries bool
	KeepData     bool
	KeepLogs     bool
}

func BuildDestroyPlan(cfg *config.Config, opts DestroyOptions) *DestroyPlan {
	plan := &DestroyPlan{
		Hosts:           make(map[string][]string),
		PatroniServices: make(map[string][]string),
		PatroniFiles:    make(map[string][]string),
		EtcdServices:    make(map[string][]string),
		EtcdFiles:       make(map[string][]string),
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

	addEtcdService := func(host, service string) {
		if service == "" {
			return
		}
		plan.EtcdServices[host] = appendUnique(plan.EtcdServices[host], service)
	}

	addEtcdFile := func(host, file string) {
		if file == "" {
			return
		}
		plan.EtcdFiles[host] = appendUnique(plan.EtcdFiles[host], file)
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
			if cfg.DeployMode == config.ModePatroni {
				addPath(host, "/opt/pg-deploy/patroni-runtime")
				addPath(host, "/opt/pg-deploy/patroni-wheelhouse")
				addPath(host, "/opt/pg-deploy/etcd-runtime")
				addEtcdFile(host, "/usr/local/bin/etcd")
				addEtcdFile(host, "/usr/local/bin/etcdctl")
				addFile(host, "/usr/local/bin/patroni")
				addFile(host, "/usr/local/bin/patronictl")
			}
		}
	}

	if cfg.DeployMode == config.ModePatroni {
		for _, host := range uniqueHosts(cfg) {
			addEtcdService(host, "etcd")
			addEtcdFile(host, "/etc/systemd/system/etcd.service")
			addEtcdFile(host, "/etc/etcd/etcd.yml")
			if !opts.KeepData {
				addPath(host, "/var/lib/etcd")
			}
		}
	}

	return plan
}

func ExecuteDestroyPlan(cfg *config.Config, exec *executor.Executor, log *logger.Logger, plan *DestroyPlan) error {
	if err := enrichDestroyPlanWithRemoteDiscovery(cfg, exec, log, plan); err != nil {
		log.Warn("Failed to enrich destroy plan from remote state", logger.Fields{"error": err})
	}

	if cfg.DeployMode == config.ModePatroni {
		if err := pausePatroniCluster(cfg, exec, log); err != nil {
			log.Warn("Failed to pause Patroni cluster before destroy", logger.Fields{"error": err})
		}
		if err := clearPatroniDCS(cfg, exec, log); err != nil {
			log.Warn("Failed to clear Patroni DCS state before destroy", logger.Fields{"error": err})
		}
	}

	hosts := sortedKeys(plan.Hosts)
	for _, host := range hosts {
		node := destroyPlanExecutorNode(cfg, host)

		if cfg.DeployMode == config.ModePatroni && len(plan.PatroniServices[host]) > 0 {
			cmd := systemctlStopDisableCommand(plan.PatroniServices[host])
			if result := exec.RunOnNode(node, cmd, true, false); result.Error != nil {
				log.Warn("Failed to stop Patroni services", logger.Fields{"host": host, "services": plan.PatroniServices[host], "error": result.Error})
			}
		}
	}

	for _, host := range hosts {
		node := destroyPlanExecutorNode(cfg, host)

		if cfg.DeployMode == config.ModePatroni && len(plan.EtcdServices[host]) > 0 {
			cmd := systemctlStopDisableCommand(plan.EtcdServices[host])
			if result := exec.RunOnNode(node, cmd, true, false); result.Error != nil {
				log.Warn("Failed to stop etcd services", logger.Fields{"host": host, "services": plan.EtcdServices[host], "error": result.Error})
			}
		}
	}

	for _, host := range hosts {
		node := destroyPlanExecutorNode(cfg, host)

		if cfg.DeployMode == config.ModePatroni {
			if len(plan.PatroniFiles[host]) > 0 {
				cmd := fmt.Sprintf("rm -f %s && systemctl daemon-reload || true", shellJoin(plan.PatroniFiles[host]))
				if result := exec.RunOnNode(node, cmd, true, false); result.Error != nil {
					log.Warn("Failed to remove Patroni files", logger.Fields{"host": host, "error": result.Error})
				}
			}
			if len(plan.EtcdFiles[host]) > 0 {
				cmd := fmt.Sprintf("rm -f %s && systemctl daemon-reload || true", shellJoin(plan.EtcdFiles[host]))
				if result := exec.RunOnNode(node, cmd, true, false); result.Error != nil {
					log.Warn("Failed to remove etcd files", logger.Fields{"host": host, "error": result.Error})
				}
			}
		}
	}

	for _, host := range hosts {
		node := &executor.Node{
			ID:       host,
			Host:     host,
			Port:     22,
			User:     cfg.SSHUser,
			Password: cfg.SSHPassword,
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

func enrichDestroyPlanWithRemoteDiscovery(cfg *config.Config, exec *executor.Executor, log *logger.Logger, plan *DestroyPlan) error {
	for _, host := range uniqueHosts(cfg) {
		node := destroyPlanExecutorNode(cfg, host)
		output, err := discoverDestroyArtifacts(node, exec, plan)
		if err != nil {
			return fmt.Errorf("host %s: %w", host, err)
		}
		applyDiscoveredArtifacts(plan, host, output)
	}

	log.Info("Destroy plan enriched from remote state",
		logger.Fields{"hosts": len(uniqueHosts(cfg))})
	return nil
}

func discoverDestroyArtifacts(node *executor.Node, exec *executor.Executor, plan *DestroyPlan) (string, error) {
	cmd := buildArtifactDiscoveryCommand(plan.PatroniServices[node.Host], plan.EtcdServices[node.Host], plan.PatroniFiles[node.Host], plan.EtcdFiles[node.Host])
	result := exec.RunOnNode(node, cmd, true, true)
	if result.Error != nil {
		return "", result.Error
	}
	return result.Output, nil
}

func buildArtifactDiscoveryCommand(patroniServices, etcdServices, patroniFiles, etcdFiles []string) string {
	patroniSvcList := shellListOrBlankToken(patroniServices)
	etcdSvcList := shellListOrBlankToken(etcdServices)
	patroniFileList := shellListOrBlankToken(patroniFiles)
	etcdFileList := shellListOrBlankToken(etcdFiles)

	return fmt.Sprintf(`
emit_file() { [ -n "$1" ] && printf 'FILE\t%%s\n' "$1"; }
emit_path() { [ -n "$1" ] && printf 'PATH\t%%s\n' "$1"; }
trim_quotes() { printf '%%s' "$1" | tr -d "\"'"; }

discover_patroni_cfg() {
  cfg="$1"
  [ -n "$cfg" ] || return 0
  [ -f "$cfg" ] || return 0
  emit_file "$cfg"
  data_dir=$(sed -n 's/^[[:space:]]*data_dir:[[:space:]]*//p' "$cfg" | head -n 1)
  data_dir=$(trim_quotes "$data_dir")
  emit_path "$data_dir"
  bin_dir=$(sed -n 's/^[[:space:]]*bin_dir:[[:space:]]*//p' "$cfg" | head -n 1)
  bin_dir=$(trim_quotes "$bin_dir")
  case "$bin_dir" in
    */bin) emit_path "${bin_dir%%/bin}" ;;
  esac
}

discover_etcd_cfg() {
  cfg="$1"
  [ -n "$cfg" ] || return 0
  [ -f "$cfg" ] || return 0
  emit_file "$cfg"
  data_dir=$(sed -n 's/^[[:space:]]*data-dir:[[:space:]]*//p' "$cfg" | head -n 1)
  data_dir=$(trim_quotes "$data_dir")
  emit_path "$data_dir"
}

for svc in %s; do
  fragment=$(systemctl show -p FragmentPath --value "$svc" 2>/dev/null || true)
  emit_file "$fragment"
  cfg=$(systemctl cat "$svc" 2>/dev/null | sed -n "s@.*patroni \([^'\"[:space:]]*\.yml\).*@\1@p" | head -n 1)
  [ -n "$cfg" ] && printf 'PATRONI_CFG\t%%s\n' "$cfg"
done

for svc in %s; do
  fragment=$(systemctl show -p FragmentPath --value "$svc" 2>/dev/null || true)
  emit_file "$fragment"
  cfg=$(systemctl cat "$svc" 2>/dev/null | sed -n "s@.*--config-file=\([^'\"[:space:]]*\).*@\1@p" | head -n 1)
  [ -n "$cfg" ] && printf 'ETCD_CFG\t%%s\n' "$cfg"
done

for cfg in %s; do
  [ -f "$cfg" ] && printf 'PATRONI_CFG\t%%s\n' "$cfg"
done

for cfg in %s; do
  [ -f "$cfg" ] && printf 'ETCD_CFG\t%%s\n' "$cfg"
done

for cfg in /etc/patroni/*.yml; do
  [ -f "$cfg" ] && printf 'PATRONI_CFG\t%%s\n' "$cfg"
done

for bin in /usr/local/bin/patroni /usr/local/bin/patronictl /usr/local/bin/etcd /usr/local/bin/etcdctl; do
  target=$(readlink -f "$bin" 2>/dev/null || true)
  [ -n "$target" ] || continue
  emit_file "$bin"
  case "$target" in
    */bin/*) emit_path "$(dirname "$(dirname "$target")")" ;;
  esac
done
`, patroniSvcList, etcdSvcList, patroniFileList, etcdFileList)
}

func applyDiscoveredArtifacts(plan *DestroyPlan, host, output string) {
	patroniCfgs := make([]string, 0)
	etcdCfgs := make([]string, 0)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		kind := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if value == "" {
			continue
		}

		switch kind {
		case "FILE":
			plan.PatroniFiles[host] = appendUnique(plan.PatroniFiles[host], value)
			plan.EtcdFiles[host] = appendUnique(plan.EtcdFiles[host], value)
		case "PATH":
			plan.Hosts[host] = appendUnique(plan.Hosts[host], value)
		case "PATRONI_CFG":
			patroniCfgs = appendUnique(patroniCfgs, value)
			plan.PatroniFiles[host] = appendUnique(plan.PatroniFiles[host], value)
		case "ETCD_CFG":
			etcdCfgs = appendUnique(etcdCfgs, value)
			plan.EtcdFiles[host] = appendUnique(plan.EtcdFiles[host], value)
		}
	}

	for _, cfg := range patroniCfgs {
		plan.PatroniFiles[host] = appendUnique(plan.PatroniFiles[host], cfg)
	}
	for _, cfg := range etcdCfgs {
		plan.EtcdFiles[host] = appendUnique(plan.EtcdFiles[host], cfg)
	}

	filteredPatroniFiles := make([]string, 0, len(plan.PatroniFiles[host]))
	filteredEtcdFiles := make([]string, 0, len(plan.EtcdFiles[host]))
	for _, file := range plan.PatroniFiles[host] {
		if strings.HasSuffix(file, ".yml") || strings.Contains(file, "patroni-") || strings.Contains(file, "/usr/local/bin/patroni") {
			filteredPatroniFiles = appendUnique(filteredPatroniFiles, file)
		}
	}
	for _, file := range plan.EtcdFiles[host] {
		if strings.Contains(file, "etcd") {
			filteredEtcdFiles = appendUnique(filteredEtcdFiles, file)
		}
	}
	if len(filteredPatroniFiles) > 0 {
		plan.PatroniFiles[host] = filteredPatroniFiles
	}
	if len(filteredEtcdFiles) > 0 {
		plan.EtcdFiles[host] = filteredEtcdFiles
	}
}

func destroyPlanExecutorNode(cfg *config.Config, host string) *executor.Node {
	return &executor.Node{
		ID:       host,
		Host:     host,
		Port:     22,
		User:     cfg.SSHUser,
		Password: cfg.SSHPassword,
	}
}

func pausePatroniCluster(cfg *config.Config, exec *executor.Executor, log *logger.Logger) error {
	nodes := cfg.GetAllNodes()
	if len(nodes) == 0 {
		return nil
	}

	controlNode := nodes[0]
	cmd := patroniPauseCommand(controlNode.Name)
	result := exec.RunOnNode(&executor.Node{
		ID:       controlNode.Host,
		Host:     controlNode.Host,
		Port:     22,
		User:     cfg.SSHUser,
		Password: cfg.SSHPassword,
	}, cmd, true, true)
	if result.Error != nil {
		return result.Error
	}

	log.Info("Patroni cluster paused before destroy",
		logger.Fields{"control_node": controlNode.Name, "host": controlNode.Host})
	return nil
}

func clearPatroniDCS(cfg *config.Config, exec *executor.Executor, log *logger.Logger) error {
	nodes := cfg.GetAllNodes()
	if len(nodes) == 0 {
		return nil
	}

	controlNode := nodes[0]
	cmd := patroniDCSCleanupCommand()
	result := exec.RunOnNode(&executor.Node{
		ID:       controlNode.Host,
		Host:     controlNode.Host,
		Port:     22,
		User:     cfg.SSHUser,
		Password: cfg.SSHPassword,
	}, cmd, true, true)
	if result.Error != nil {
		return result.Error
	}

	log.Info("Patroni DCS state cleared before destroy",
		logger.Fields{"control_node": controlNode.Name, "host": controlNode.Host})
	return nil
}

func patroniPauseCommand(nodeName string) string {
	configFile := shellQuote(fmt.Sprintf("/etc/patroni/%s.yml", nodeName))
	return fmt.Sprintf("if command -v patronictl >/dev/null 2>&1; then patronictl -c %s pause --wait || patronictl -c %s pause || true; fi", configFile, configFile)
}

func patroniDCSCleanupCommand() string {
	return "if command -v etcdctl >/dev/null 2>&1; then ETCDCTL_API=3 etcdctl del /service/pg-cluster --prefix || true; fi"
}

func systemctlStopDisableCommand(services []string) string {
	sortedServices := append([]string(nil), services...)
	sort.Strings(sortedServices)
	joined := strings.Join(sortedServices, " ")
	return fmt.Sprintf("systemctl stop %s 2>/dev/null || true && systemctl disable %s 2>/dev/null || true", joined, joined)
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

func PrintDestroyPlan(cfg *config.Config, plan *DestroyPlan, dryRun bool) {
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
		if len(plan.EtcdServices[host]) > 0 {
			fmt.Printf("  Etcd services: %s\n", strings.Join(plan.EtcdServices[host], ", "))
		}
		if len(plan.PatroniFiles[host]) > 0 {
			fmt.Printf("  Patroni files: %s\n", strings.Join(plan.PatroniFiles[host], ", "))
		}
		if len(plan.EtcdFiles[host]) > 0 {
			fmt.Printf("  Etcd files: %s\n", strings.Join(plan.EtcdFiles[host], ", "))
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

func shellListOrBlankToken(values []string) string {
	if len(values) == 0 {
		return "''"
	}
	return shellJoin(values)
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
