# pg-deploy 使用手册

## 目标场景

`pg-deploy` 适合以下场景：

- 反复清理并重建 PostgreSQL 环境
- 在同一台机器或多台机器上维护多套 PG 环境
- 部署单机、主从、Patroni 高可用、Citus 分布式
- 用统一配置文件管理安装目录、数据目录、日志目录和节点拓扑

## 核心原则

- `compile` 模式允许重复清理旧安装并重新编译
- 密码默认不落盘，SSH 密码建议通过 `SSH_PASSWORD` 提供
- 多环境建议显式使用 `env_name`
- 同机多实例时，必须为每个实例分配不同的 PostgreSQL 端口

## 常用命令

构建：

```bash
go build -o pg-deploy .
```

验证配置和连通性：

```bash
export SSH_PASSWORD='your_ssh_password'
export PGUSER=postgres
export PGPASSWORD='your_pg_password'

./pg-deploy validate -c deploy.conf
./pg-deploy validate -c deploy.conf --ssh-only
./pg-deploy validate -c deploy.conf --details
```

模拟执行：

```bash
export SSH_PASSWORD='your_ssh_password'
./pg-deploy deploy -c deploy.conf --dry-run
```

正式部署：

```bash
export SSH_PASSWORD='your_ssh_password'
./pg-deploy deploy -c deploy.conf
```

交互式生成配置：

```bash
./pg-deploy wizard -o deploy.conf
```

查看环境：

```bash
./pg-deploy env list -c deploy.conf
```

销毁环境：

```bash
export SSH_PASSWORD='your_ssh_password'
./pg-deploy env destroy -c deploy.conf --dry-run
./pg-deploy env destroy -c deploy.conf --yes
./pg-deploy env destroy -c deploy.conf --yes --keep-binaries
./pg-deploy env destroy -c deploy.conf --yes --keep-data --keep-logs
```

## 配置文件格式

### 通用字段

```conf
ssh_user: root
deploy_mode: patroni
build_mode: distribute
pg_source: /opt/packages/postgresql-17.9-linux-amd64.tar.gz
pg_soft_dir: /usr/local/{env}/pgsql
env_name: pg17-dev
env_prefix: pg17-dev
```

字段说明：

- `ssh_user`: 远程 SSH 用户
- `deploy_mode`: `standalone` / `master-slave` / `patroni` / `citus`
- `build_mode`: `compile` / `distribute`
- `pg_source`: 源码包或二进制包路径
- `pg_soft_dir`: PostgreSQL 安装目录
- `env_name`: 环境名，启用多环境模板替换
- `env_prefix`: 环境前缀，默认等于 `env_name`

### 多环境模板

以下字段支持模板：

- `pg_source`
- `pg_soft_dir`
- `group name`
- `node name`
- `data_dir`
- `wal_dir`
- `pglog_dir`

可用变量：

- `{env}`: 环境名
- `{prefix}`: 环境前缀

示例：

```conf
env_name: pg17-dev
env_prefix: pg17-dev
pg_soft_dir: /usr/local/{env}/pgsql
group_0: 0|primary|primary|10.0.0.11:5432:/data/{env}/primary:/wal/{env}/primary:/log/{env}/primary:1
```

展开后：

- `pg_soft_dir` -> `/usr/local/pg17-dev/pgsql`
- `group name` -> `pg17-dev-primary`
- `node name` -> `pg17-dev-primary0`
- `data_dir` -> `/data/pg17-dev/primary`

## 典型配置

### 1. 单机模式

```conf
ssh_user: root
deploy_mode: standalone
build_mode: distribute
pg_source: /opt/packages/pgsql.tar.gz
pg_soft_dir: /usr/local/{env}/pgsql
env_name: pg17-standalone
group_0: 0|db|primary|10.0.0.11:5432:/data/{env}/db::::1
```

### 2. 主从模式

```conf
ssh_user: root
deploy_mode: master-slave
build_mode: distribute
pg_source: /opt/packages/pgsql.tar.gz
pg_soft_dir: /usr/local/{env}/pgsql
env_name: pg17-repl
group_0: 0|db|primary|10.0.0.11:5432:/data/{env}/master::::1,1|db|standby|10.0.0.12:5432:/data/{env}/slave1::::0,2|db|standby|10.0.0.13:5432:/data/{env}/slave2::::0
```

### 3. Patroni + PostgreSQL + etcd

说明：

- etcd 默认选择前 3 个唯一主机部署
- Patroni REST 端口自动按 `PG 端口 + 1000` 计算，避免同机多实例冲突
- Patroni 配置文件写入 `/etc/patroni/<node_name>.yml`
- etcd 配置文件写入 `/etc/etcd/etcd.yml`

```conf
ssh_user: root
deploy_mode: patroni
build_mode: distribute
pg_source: /opt/packages/pgsql.tar.gz
pg_soft_dir: /usr/local/{env}/pgsql
env_name: pg17-ha
group_0: 0|ha|primary|10.0.0.11:5432:/data/{env}/node1::::1,1|ha|standby|10.0.0.12:5432:/data/{env}/node2::::0,2|ha|standby|10.0.0.13:5432:/data/{env}/node3::::0
```

部署步骤：

1. `PrepareDirectories`
2. `DeploySoftware`
3. `InstallPatroni`
4. `ConfigurePatroni`
   这里会同时完成 etcd 集群安装/配置/健康检查，并生成 Patroni YAML
5. `StartPatroniCluster`
6. `ValidateDeployment`

### 4. 同机多实例 Patroni

```conf
ssh_user: root
deploy_mode: patroni
build_mode: distribute
pg_source: /opt/packages/pgsql.tar.gz
pg_soft_dir: /usr/local/{env}/pgsql
env_name: pg17-lab
group_0: 0|ha|primary|10.0.0.11:5432:/data/{env}/node1::::1,1|ha|standby|10.0.0.11:5433:/data/{env}/node2::::0,2|ha|standby|10.0.0.11:5434:/data/{env}/node3::::0
```

这个场景下：

- PostgreSQL 端口分别为 `5432 / 5433 / 5434`
- Patroni REST 端口分别为 `6432 / 6433 / 6434`
- etcd 只会在这个主机上部署一个实例

## 推荐流程

### 新建环境

```bash
export SSH_PASSWORD='your_ssh_password'
./pg-deploy deploy -c deploy.conf --dry-run
./pg-deploy validate -c deploy.conf --ssh-only
./pg-deploy deploy -c deploy.conf
```

### 重建已有环境

适用于你当前的主要需求：

- 旧环境可以反复删除并重建
- `compile` 模式会清理旧安装后重新编译
- 建议不同环境使用不同的 `env_name`，避免路径和服务名互相覆盖

示例：

```bash
export SSH_PASSWORD='your_ssh_password'
./pg-deploy deploy -c pg17-dev.conf
./pg-deploy deploy -c pg17-test.conf
./pg-deploy deploy -c pg16-benchmark.conf
```

### 部分清理环境

只清理运行态和配置，保留二进制：

```bash
./pg-deploy env destroy -c pg17-dev.conf --yes --keep-binaries
```

保留数据目录和日志，只移除服务和环境级配置：

```bash
./pg-deploy env destroy -c pg17-dev.conf --yes --keep-data --keep-logs
```

## Patroni 模式排查

### 1. 查看 etcd

```bash
systemctl status etcd
ETCDCTL_API=3 etcdctl endpoint health --cluster --endpoints=http://10.0.0.11:2379,http://10.0.0.12:2379,http://10.0.0.13:2379
```

### 2. 查看 Patroni

```bash
systemctl status patroni-pg17-ha-ha0
patronictl -c /etc/patroni/pg17-ha-ha0.yml list
```

### 3. 查看 PostgreSQL

```bash
/usr/local/pg17-ha/pgsql/bin/pg_isready -h localhost -p 5432
```

## 当前限制

- SSH 端口目前固定按 `22` 使用
- etcd 安装依赖目标机包管理器可用
- Patroni 模式默认使用本地 systemd 管理服务
- `env destroy` 当前只清理当前配置能确定属于该环境的 PG 目录和 Patroni 文件，不会自动删除共享 etcd
- `env destroy` 支持 `--keep-binaries`、`--keep-data`、`--keep-logs`

## 建议

- 生产环境优先使用独立主机部署 Patroni 节点
- 同机多实例更适合测试、验证和实验环境
- 每套环境都使用独立 `env_name`
- 正式环境建议为 `pg_soft_dir`、`data_dir`、`pglog_dir` 使用不同磁盘或挂载点
