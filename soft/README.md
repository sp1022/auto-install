# Offline Runtime Bundles

`soft/` 目录用于放置 Patroni 离线运行时软件，部署时会优先使用这些本地软件。

优先级：

1. 配置文件里显式指定的 `patroni_package` / `patroni_wheelhouse` / `etcd_package`
2. `soft/patroni-runtime/` 和 `soft/patroni-runtime-linux-amd64.tar.gz`
3. `soft/etcd-runtime/` 和 `soft/etcd-linux-amd64.tar.gz`
4. `soft/patroni-wheelhouse-debian-amd64.tar.gz`
5. 目标机系统在线安装

推荐目录布局：

```text
soft/
  patroni-runtime/
    bin/
      python3
      patroni
      patronictl
    lib/
      site-packages/...
  etcd-runtime/
    bin/
      etcd
      etcdctl
```

也支持平铺文件布局：

```text
soft/
  python3
  patroni
  patronictl
  etcd
  etcdctl
  site-packages/...
```

其中：

- `python3`、`patroni`、`patronictl` 会自动组装成 Patroni 运行时
- `etcd`、`etcdctl` 会自动组装成 etcd 运行时
- `site-packages/` 或 `lib/` 会在 Patroni 打包时一并带上

推荐文件名：

- `soft/patroni-runtime-linux-amd64.tar.gz`
- `soft/etcd-linux-amd64.tar.gz`

`patroni-runtime` tar.gz 解压后至少应包含：

```text
bin/
  python3
  patroni
  patronictl
lib/
  python3.x/...
  site-packages/...
include/
share/
```

推荐把完整 Python 运行时前缀一起带进包里，而不是只放一个 `python3` 二进制。这样标准库、动态库和 `encodings` 等基础模块都在 runtime 内，目标机不需要系统 Python。

`scripts/package-patroni-runtime.sh` 已支持：

- 直接从本地 Python 前缀目录打包
- 从 `python3` 可执行文件反推其前缀目录打包
- 从本地或网络 tar.gz/tar.xz 下载并展开后打包

生产建议固定版本基线：

- Python `3.13.12`
- `patroni[etcd]==4.1.0`
- `prettytable==3.16.0`
- `psycopg2-binary==2.9.11`

仓库内已记录在：

- `soft/patroni-runtime-versions.txt`

推荐先在一台干净的 Linux amd64 构建机上准备完整运行时，再打包：

```bash
python3.13 -m venv /tmp/patroni-venv
/tmp/patroni-venv/bin/pip install --upgrade pip
/tmp/patroni-venv/bin/pip install 'patroni[etcd]==4.1.0' 'prettytable==3.16.0' 'psycopg2-binary==2.9.11'
./scripts/package-patroni-runtime.sh /path/to/python-3.13.12-prefix /tmp/patroni-venv/lib/python3.13/site-packages soft/patroni-runtime-linux-amd64.tar.gz
```

这样做的目的不是“尽量新”，而是“较新的同时可重复、可审计、可回放”。目标机部署时不再依赖系统 Python 或在线解析依赖版本。

`etcd` tar.gz 解压后至少应包含：

```text
bin/
  etcd
  etcdctl
```

配置示例：

```text
deploy_mode: patroni
# 可以不写，程序会自动探测 soft/ 下的运行时和离线包
# patroni_package: soft/patroni-runtime-linux-amd64.tar.gz
# etcd_package: soft/etcd-linux-amd64.tar.gz
```

如果未配置离线包，部署会退回现有逻辑：

- Patroni: 目标机在线安装 `python3` / `pip` / `venv`，然后在独立虚拟环境里安装固定版本的 Patroni 运行时
- etcd: 目标机通过系统包管理器安装
