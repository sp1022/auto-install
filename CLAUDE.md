# CLAUDE.md

此文件为 Claude Code (claude.ai/code) 在此仓库中工作时提供指导。

## 项目概述

这是一个用 Go 编写的 PostgreSQL 自动化部署工具 (pg-deploy)，支持多种部署拓扑：
- **单机模式**：独立 PostgreSQL 实例
- **主从模式**：传统流复制
- **Patroni 高可用**：基于 Patroni 和 etcd 的高可用集群
- **多组模式 (Citus)**：基于 Citus 扩展的分布式 PostgreSQL

该工具提供交互式 CLI，用于在多个节点上部署和管理 PostgreSQL 集群。

## 构建和运行

```bash
# 构建可执行文件
go build -o pg-deploy main.go

# 运行部署工具
./pg-deploy

# 或直接使用二进制文件
./pg-deploy
```

工具需要安装 Go 环境。构建输出为当前目录下的 `pg-deploy` 文件。

## 本地测试

在没有多节点的情况下进行本地测试：
- 使用 `127.0.0.1` 或 `localhost` 作为 IP 地址
- 工具会自动检测本地 IP 并优化执行
- 同一主机上的每个节点必须使用不同的端口
- 每个节点的数据目录必须唯一

## 架构

### 核心组件

**main.go** - 单文件单体应用（约 2096 行），包含：

1. **配置管理**
   - 通过 `collectConfig()` 交互式收集配置
   - 基于文件的配置持久化存储在 `deploy.conf`
   - 支持部署模式（单机/主从/patroni/多组）
   - 构建模式（从源码编译 vs 分发预编译二进制）

2. **部署引擎** (`executeDeploy()` - 7 步工作流)：
   - 步骤 1: `prepareDirectories()` - 创建用户、目录、权限
   - 步骤 2: `deploySoftware()` - 处理编译或分发
   - 步骤 3-5: 模式特定（Patroni: 安装/配置/启动；标准: initdb/systemd/启动）
   - 步骤 6-7: 复制设置和验证（如适用）
   - `initDatabases()`: 仅在主节点上运行 initdb
   - `setupReplication()`: 通过 pg_basebackup 配置流复制

3. **拓扑模型**
   - `GroupConfig`: 表示 PostgreSQL 组（coordinator/worker/primary）
   - `NodeConfig`: 表示单个数据库节点（IP、端口、数据目录）
   - 每个组可以有 1 个主节点和 N 个从节点
   - 支持每个节点自定义 WAL 和 pglog 目录

4. **Patroni 集成**
   - `installPatroni()`: 通过 pip 安装 Python 依赖和 Patroni
   - `configPatroni()`: 从嵌入的模板生成 YAML 配置
   - `startPatroniCluster()`: 创建 systemd 服务并启动集群
   - Patroni 模式自动包含 Citus 扩展

5. **远程执行**
   - `runCmdRemote()`: 通过 SSH 执行命令（支持密码或密钥）
   - `syncFileToRemote()`: 通过 SCP 传输文件
   - `isLocalNode()`: 检测目标 IP 是否为本地主机以优化执行
   - 通过 `/etc/hosts`、`hostname -I` 和 `ifconfig` 检测本地 IP
   - 支持 SSH 密码（通过 sshpass）和密钥认证

### 关键设计模式

- **交互优先**：主要工作流是交互式 CLI，可选配置文件加载
- **尽可能幂等**：操作前检查现有进程/数据
- **向导式**：逐步配置收集，带有合理的默认值
- **并行执行**：在所有节点上独立运行操作
- **状态跟踪**：使用 `.rollback_state` 进行部署跟踪

### 配置文件格式

`deploy.conf` 使用简单的键值对：
```
ssh_user: root
deploy_mode: patroni
build_mode: distribute
pg_source: /path/to/pgsql.tar.gz
pg_soft_dir: /usr/local/pgsql
group_0: 0|patroni0|master|192.168.1.10:5432:/data/pgdata::::1,192.168.1.11:5432:/data/pgdata_slave::::0
```

组格式：`id|name|role|ip:port:data_dir:wal_dir:pglog_dir:is_master,...`

### 目录结构

- `patroni/`: Patroni YAML 配置示例（g000-g003）
- `prepare_distribute.sh`: 分发包打包脚本
- `scripts/test-integration.sh`: 集成测试脚本
- `deploy.conf`: 生成的配置文件（不在仓库中）
- `.rollback_state`: 部署状态跟踪（不在仓库中）

### 重要实现细节

1. **WAL 目录处理**：创建实际的 WAL 目录，然后将 `$DATA_DIR/pg_wal` 软链接到 `$WAL_DIR`

2. **编译策略**：
   - 本地编译：将源码解压到 `/tmp/pg_build`，运行 `./configure && make install-world-bin`
   - 远程编译：通过 SCP 同步源码包，在目标主机上编译
   - 并行编译使用 `-j$(nproc)`（远程）或 `-j4`（本地）
   - 编译完成后清理构建产物

3. **复制设置**：
   - 创建随机 16 位密码的复制用户
   - 使用 `pg_basebackup -R` 初始化副本（自动创建 standby.signal）
   - 在 initdb 后修改 `postgresql.conf` 和 `pg_hba.conf`
   - 在启动 basebackup 前等待主节点就绪

4. **服务管理**：
   - 创建 systemd 服务：`postgresql-{groupname}` 或 `patroni-{groupname}`
   - 服务文件包含 `User=` 和 `Group=` 指令
   - 使用可配置超时的 `pg_isready` 等待就绪
   - Patroni 服务使用 Type=forking 和 Restart=on-failure

5. **错误处理**：
   - 非致命错误继续执行（以黄色记录警告）
   - 致命错误停止部署（以红色记录错误）
   - 数据目录冲突时交互式提示
   - 所有操作记录到带时间戳的 `deploy-*.log` 文件

6. **本地 vs 远程检测**：
   - 从多个源检查目标 IP 是否为本地 IP
   - 对本地主机使用直接命令而非 SSH 优化执行
   - 避免本地部署的不必要 SSH/scp 操作

### 健康检查

`healthCheck()` 函数验证：
- systemd 服务状态
- 通过 `pg_isready` 检查 PostgreSQL 连接性
- 数据目录所有权
- 主从设置的复制状态

### 扩展支持

编译时扩展：
- **citus**: 需要源码路径，Patroni 模式自动配置
- **pg_stat_statements**: 内置，通过 `shared_preload_libraries` 配置
- **auto_explain**: 内置，通过 `shared_preload_libraries` 配置

## 常见操作

**部署新集群**：
1. 运行 `./pg-deploy`，选择选项 1（全新部署）
2. 按照向导配置拓扑（模式、构建类型、节点、路径）
3. 确认后让其自动运行 7 个步骤

**使用现有配置部署**：
- 将 `deploy.conf` 放在脚本目录中
- 运行 `./pg-deploy`，选择选项 1
- 提示时选择使用现有配置文件

**生成配置模板**：
- 从主菜单选择选项 4
- 手动编辑 `deploy.conf` 设置
- 重新运行并从配置文件加载

**检查集群健康**：
- 从主菜单选择选项 2
- 检查所有配置的组和节点
- 检查 systemd 服务状态、PostgreSQL 连接性、数据目录所有权
- 报告主从设置的复制状态

**销毁部署**：
- 从主菜单选择选项 3
- 停止所有 PostgreSQL 服务
- 可选删除数据目录（提示确认）

## 开发说明

- 除 Go 标准库外无外部依赖
- 使用 `crypto/rand` 生成安全密码
- 通过 `sshpass` 进行 SSH 密码认证（如果提供）
- 通过 `/etc/hosts`、`hostname -I` 和 `ifconfig` 检测本地 IP
- 跨平台：支持 Linux 和 macOS（macOS 可能需要安装 sshpass）
- 同时记录到控制台（彩色）和带时间戳的 `deploy-*.log` 文件
- 所有远程命令使用 `bash -c` 进行正确的 shell 解释
- 在 `.rollback_state` 中跟踪部署状态

## 故障排除

**常见问题：**

1. **SSH 连接失败**
   - 验证 SSH 基于密钥的认证：`ssh -o BatchMode=yes root@target-ip`
   - 对于密码认证，确保已安装 `sshpass`：`brew install sshpass`（macOS）
   - 检查 deploy.conf 中的 `SSH_USER` 和 `SSHPassword`

2. **编译错误**
   - 确保已安装构建依赖：`gcc`、`make`、`zlib-devel`、`libssl-devel`
   - 检查 `PG_CONFIGURE_OPTS` 中缺少的依赖（zstd、lz4、icu）
   - 验证源码包有效且未损坏

3. **数据目录冲突**
   - 工具在删除现有数据目录前会提示
   - 检查运行中的 PostgreSQL 进程：`ps aux | grep postgres`
   - 验证端口可用性：`netstat -tlnp | grep PORT`

4. **复制设置失败**
   - 确保 `pg_hba.conf` 允许复制连接
   - 检查主从节点之间的防火墙规则
   - 在复制设置前验证 `pg_isready` 返回成功
