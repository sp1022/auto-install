# PostgreSQL 自动化部署工具 (`pg-deploy`)

`pg-deploy` 用于批量部署、验证、销毁和重建 PostgreSQL 环境，支持：

- `standalone`
- `master-slave`
- `patroni`
- `citus`

当前工程重点面向两类场景：

- 用统一配置文件管理多节点 PostgreSQL 环境
- 在测试、联调和运维环境中反复执行 destroy + redeploy

## 功能概览

- 支持单机、主从、Patroni 高可用、Citus 分布式四种模式
- 支持 `validate`、`deploy`、`env destroy`、`wizard`
- 支持 `--destroy-first --yes` 先清理再重建
- Patroni 模式支持离线运行时、wheelhouse 和在线安装
- 销毁流程会探测实际运行路径，而不是只依赖固定默认值
- 部署失败支持回滚，日志为结构化输出

## 构建

```bash
go build -o pg-deploy .
```

如果你需要按固定文件名生成 Linux amd64 二进制：

```bash
env GOCACHE="$PWD/.gocache" GOOS=linux GOARCH=amd64 go build -o pg-deploy-debian-amd64 ./main.go
```

## 常用命令

查看帮助：

```bash
./pg-deploy --help
./pg-deploy deploy --help
./pg-deploy env destroy --help
```

验证环境：

```bash
export PGUSER=postgres
export PGPASSWORD='your_pg_password'

./pg-deploy validate -c deploy.conf
./pg-deploy validate -c deploy.conf --ssh-only
./pg-deploy validate -c deploy.conf --details
```

部署：

```bash
./pg-deploy deploy -c deploy.conf
./pg-deploy deploy -c deploy.conf --dry-run
./pg-deploy deploy -c deploy.conf --destroy-first --yes
./pg-deploy deploy -c deploy.conf --destroy-first --yes --keep-binaries --keep-logs
```

清理环境：

```bash
./pg-deploy env list -c deploy.conf
./pg-deploy env destroy -c deploy.conf --dry-run
./pg-deploy env destroy -c deploy.conf --yes
./pg-deploy env destroy -c deploy.conf --yes --keep-data --keep-logs
```

## `--destroy-first --yes` 的实际行为

这是破坏性操作。当前版本在 Patroni 模式下会优先：

1. 暂停 Patroni 集群
2. 清理 DCS 键
3. 停止 Patroni 服务
4. 停止 etcd 服务
5. 探测并删除实际运行中的配置、数据和运行时路径
6. 重新执行部署

保留选项说明：

- `--keep-binaries`：保留 PostgreSQL / Patroni / etcd 安装目录
- `--keep-data`：保留 PostgreSQL 数据目录和 WAL 目录
- `--keep-logs`：保留 PostgreSQL / Patroni / etcd 日志目录

## Patroni 运行时建议

生产环境建议优先使用完整打包的 Python + Patroni 运行时，而不是依赖目标机系统 Python / pip。

当前推荐固定版本见：

- [soft/patroni-runtime-versions.txt](/Users/zhangqi/Desktop/运维脚本/auto-install/soft/patroni-runtime-versions.txt)
- [soft/README.md](/Users/zhangqi/Desktop/运维脚本/auto-install/soft/README.md)

默认推荐组合：

- Python `3.13.12`
- `patroni[etcd]==4.1.0`
- `prettytable==3.16.0`
- `psycopg2-binary==2.9.11`

运行时包命名规则：

- 如果在配置文件里显式填写 `patroni_package` / `etcd_package`，文件名可以自定义，不要求必须叫 `patroni-runtime-linux-amd64.tar.gz`
- 只要路径存在，且包内目录结构正确，程序就会按该路径下发并解压
- 如果不显式填写，程序自动探测 `soft/` 时目前只识别固定候选名和固定目录布局

例如下面这种自定义命名是支持的：

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

## 配置示例

Patroni 基本示例：

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

更多配置和排障说明见：

- [docs/USER_MANUAL.md](/Users/zhangqi/Desktop/运维脚本/auto-install/docs/USER_MANUAL.md)
- [docs/TROUBLESHOOTING.md](/Users/zhangqi/Desktop/运维脚本/auto-install/docs/TROUBLESHOOTING.md)

## 已知经验

- Patroni 模式下，集群状态校验优先使用 REST API，不依赖 `patronictl list` 表格解析
- `systemctl start patroni-*` 如果看到 `activating`，要同时检查实际 PostgreSQL 监听和 Patroni REST 端口
- 低内存环境下需要降低 PostgreSQL 参数，避免共享内存启动失败
- 全量重建必须同时清理 systemd 服务、DCS、etcd 数据和 PostgreSQL 数据目录，否则容易出现旧状态污染

## 文档

- [使用手册](/Users/zhangqi/Desktop/运维脚本/auto-install/docs/USER_MANUAL.md)
- [故障排查](/Users/zhangqi/Desktop/运维脚本/auto-install/docs/TROUBLESHOOTING.md)
- [运行时说明](/Users/zhangqi/Desktop/运维脚本/auto-install/soft/README.md)
