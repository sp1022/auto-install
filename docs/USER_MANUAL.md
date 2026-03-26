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
./pg-deploy deploy -c deploy.conf --destroy-first --yes --keep-binaries
```

正式部署：

```bash
export SSH_PASSWORD='your_ssh_password'
./pg-deploy deploy -c deploy.conf
./pg-deploy deploy -c deploy.conf --destroy-first --yes
./pg-deploy deploy -c deploy.conf --destroy-first --yes --keep-binaries
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
- 支持本地运行时目录、离线包、wheelhouse、在线安装四种来源
- `soft/` 软件源优先级：
  1. 配置文件里显式指定的 `patroni_package`、`patroni_wheelhouse`、`etcd_package`
  2. `soft/patroni-runtime/`、`soft/patroni-runtime-linux-amd64.tar.gz`
  3. `soft/etcd-runtime/`、`soft/etcd-linux-amd64.tar.gz`
  4. `soft/patroni-wheelhouse-debian-amd64.tar.gz`
  5. 目标机在线安装
- 推荐优先使用 `patroni-runtime-linux-amd64.tar.gz` 和 `etcd-linux-amd64.tar.gz`
- Debian 可使用 `patroni_wheelhouse` 做离线安装，但 wheelhouse 需要完整可用
- `patroni_package` 推荐包含完整 Python 运行时前缀，而不是只拷一个 `python3` 二进制
- 如果配置文件里显式填写 `patroni_package` / `etcd_package`，运行时包文件名可以自定义；程序不依赖固定文件名
- 如果不显式填写而改为自动探测 `soft/`，当前只识别固定候选名和固定目录布局

```conf
ssh_user: root
deploy_mode: patroni
build_mode: distribute
pg_source: /opt/packages/pgsql.tar.gz
pg_soft_dir: /usr/local/{env}/pgsql
env_name: pg17-ha
patroni_package: soft/patroni-runtime-linux-amd64.tar.gz
etcd_package: soft/etcd-linux-amd64.tar.gz
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

离线运行时准备：

```bash
mkdir -p soft
bash scripts/package-patroni-runtime.sh /opt/python3.11 /opt/patroni-venv/lib/python3.11/site-packages soft/patroni-runtime-linux-amd64.tar.gz
```

如果本地没有完整 Python runtime，也可以直接指定一个可下载的 Python runtime tar.gz 地址：

```bash
bash scripts/package-patroni-runtime.sh https://example.com/python-runtime.tar.gz /opt/patroni-venv/lib/python3.11/site-packages soft/patroni-runtime-linux-amd64.tar.gz
```

也可以直接把已解压运行时放到仓库：

```text
soft/
  patroni-runtime/
    bin/python3
    bin/patroni
    bin/patronictl
    lib/python3.x/...
    lib/site-packages/...
  etcd-runtime/
    bin/etcd
    bin/etcdctl
```

如果 `deploy_mode: patroni` 且配置文件未显式填写 `patroni_package` / `patroni_wheelhouse` / `etcd_package`，程序会自动探测 `soft/` 下可用的离线运行时、wheelhouse 和 etcd 包，并优先使用离线路径。

也支持 `soft/` 平铺文件：

```text
soft/
  python3
  patroni
  patronictl
  etcd
  etcdctl
  site-packages/...
```

这种情况下程序会自动把它们临时组装成运行时包再下发到目标机。

自定义命名示例：

```conf
patroni_package: soft/patroni-runtime-v20260326.tar.gz
etcd_package: soft/etcd-runtime-v20260326.tar.gz
```

自动探测当前识别的 Patroni 候选路径：

- `soft/patroni-runtime`
- `soft/patroni-runtime-linux-amd64.tar.gz`
- `soft/patroni-runtime.tar.gz`

自动探测当前识别的 etcd 候选路径：

- `soft/etcd-runtime`
- `soft/etcd-linux-amd64.tar.gz`
- `soft/etcd-runtime.tar.gz`

推荐生产运行时版本见：

- [`soft/patroni-runtime-versions.txt`](/Users/zhangqi/Desktop/运维脚本/auto-install/soft/patroni-runtime-versions.txt)
- [`soft/README.md`](/Users/zhangqi/Desktop/运维脚本/auto-install/soft/README.md)

### 3.1 小内存环境说明

如果单节点可用内存较低，优先降低 Patroni 生成的 PostgreSQL 参数，尤其是：

- `max_connections`
- `shared_buffers`

排查时优先看：

```bash
free -h
sysctl vm.overcommit_memory
systemctl status patroni-<node>
journalctl -u patroni-<node> -n 100 --no-pager
```

如果出现：

- `could not map anonymous shared memory`
- `systemctl start ...` 卡在 `activating`

先确认：

- 实际 PostgreSQL 是否已经监听端口
- Patroni REST 端口是否可访问
- 当前生效参数是否仍偏大

### 3.2 示例参数模板

仓库里已经提供两份 1G 示例文件：

- [`deploy-patroni-8g.conf`](/Users/zhangqi/Desktop/运维脚本/auto-install/examples/patroni/deploy-patroni-8g.conf)
  可直接作为部署配置使用
- [`patroni-8g.conf`](/Users/zhangqi/Desktop/运维脚本/auto-install/examples/patroni/patroni-8g.conf)
  作为 Patroni / PostgreSQL 调优参数模板参考

适用场景：

- 单节点约 1GB 可用内存
- 通用 OLTP 场景
- SSD 或云盘

1G 模板中的核心参数示例：

- `max_connections: 100`
- `shared_buffers: 256MB`
- `effective_cache_size: 768MB`
- `work_mem: 2MB`
- `maintenance_work_mem: 64MB`
- `max_wal_size: 1GB`
- `wal_keep_size: 512MB`
- `max_wal_senders: 8`
- `max_replication_slots: 8`

使用建议：

- 先复制 [`deploy-patroni-8g.conf`](/Users/zhangqi/Desktop/运维脚本/auto-install/examples/patroni/deploy-patroni-8g.conf) 并修改主机、路径、`pg_source`
- 再按业务负载参考 [`patroni-8g.conf`](/Users/zhangqi/Desktop/运维脚本/auto-install/examples/patroni/patroni-8g.conf) 调整参数
- 如果当前项目后续接入自定义 Patroni YAML 生成逻辑，可以直接把这份模板作为基线

Debian wheelhouse 方案：

- 不做完整 Python 运行时 tar.gz 时，可只准备 `patroni_wheelhouse`
- 目标 Debian 机器会安装系统 `python3` / `pip`
- 然后通过独立 `venv` 和 `pip --no-index --find-links=...` 离线安装 Patroni
- wheelhouse 内需要包含完整依赖；推荐优先使用已打好的 runtime 包

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
./pg-deploy deploy -c pg17-dev.conf --destroy-first --yes
./pg-deploy deploy -c pg17-test.conf --destroy-first --yes --keep-binaries
./pg-deploy deploy -c pg16-benchmark.conf --destroy-first --yes --keep-binaries --keep-logs
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
curl -s http://127.0.0.1:6432/patroni
```

### 3. 查看 PostgreSQL

```bash
/usr/local/pg17-ha/pgsql/bin/pg_isready -h localhost -p 5432
```

## 当前限制

- SSH 端口目前固定按 `22` 使用
- Patroni 模式默认使用本地 systemd 管理服务
- `env destroy` 支持 `--keep-binaries`、`--keep-data`、`--keep-logs`
- `deploy --destroy-first --yes` 会先按销毁计划清理当前环境，再重新部署；在 `patroni` 模式下会同时清理当前配置涉及主机上的 Patroni 和 etcd 服务、配置与数据目录
- `examples/patroni/patroni-8g.conf` 目前是参数模板，主部署流程不会自动逐项读取里面全部字段
- `patronictl` 只作为运维辅助工具，集群校验逻辑不再依赖它的表格输出

## 近期排障结论

- `deploy --destroy-first --yes` 必须同时处理 systemd 服务、DCS、etcd 数据目录和 PostgreSQL 数据目录，否则重建时容易残留旧状态
- Patroni 模式下销毁不能只按固定路径删除，应该先探测实际运行路径
- `systemctl start patroni-*` 是否显示 `active` 不是唯一判断依据，还要同时看 PostgreSQL 端口和 Patroni REST 接口
- 生产环境优先使用完整打包的 Python + Patroni 运行时，避免系统 Python / pip 依赖漂移

更具体的故障定位步骤见：

- [`docs/TROUBLESHOOTING.md`](/Users/zhangqi/Desktop/运维脚本/auto-install/docs/TROUBLESHOOTING.md)

## 建议

- 生产环境优先使用独立主机部署 Patroni 节点
- 同机多实例更适合测试、验证和实验环境
- 每套环境都使用独立 `env_name`
- 正式环境建议为 `pg_soft_dir`、`data_dir`、`pglog_dir` 使用不同磁盘或挂载点
