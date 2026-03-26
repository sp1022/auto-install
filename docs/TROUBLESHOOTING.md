# pg-deploy 故障排查

这份手册只关注当前工程里反复出现、且已经验证过的故障类型。

## 1. `deploy --destroy-first --yes` 后仍然残留旧环境

常见现象：

- 重新部署后出现旧 leader / replica 信息
- Patroni 报 `system ID is invalid`
- etcd 中仍能看到旧集群键

优先确认：

```bash
ETCDCTL_API=3 etcdctl get /service --prefix --keys-only
systemctl list-units | grep -E 'patroni|etcd'
find /etc/patroni /etc/etcd -maxdepth 2 -type f 2>/dev/null
```

说明：

- 这类问题通常不是 PostgreSQL 本身先坏，而是旧的 DCS、etcd 数据目录或 Patroni 配置残留。
- 当前版本已经把 destroy 流程改成先探测真实路径再删除，但现场仍建议抽样确认一次。

## 2. Patroni 服务是 `active`，但集群状态不对

先不要只看 `systemctl is-active`，还要看三个层面：

```bash
systemctl status patroni-<node> --no-pager -l
curl -s http://127.0.0.1:<rest_port>/patroni
ss -lntp | grep <pg_port>
```

判断原则：

- `systemd active` 只说明 Patroni 进程在
- `/patroni` REST 才能看出 `role` 和 `state`
- PostgreSQL 端口监听才能说明数据库真的起来了

## 3. `systemctl start patroni-*` 卡住

旧版本常见原因是 systemd 使用了不合适的 `Type=notify`。当前工程已改为 `Type=simple`。

如果现场仍出现类似现象，先看：

```bash
systemctl status patroni-<node> --no-pager -l
journalctl -u patroni-<node> -n 100 --no-pager
curl -s http://127.0.0.1:<rest_port>/patroni
```

如果 REST 正常、PostgreSQL 端口正常监听，通常不是服务没起，而是就绪判断出了偏差。

## 4. PostgreSQL 启动时报共享内存错误

典型日志：

- `could not map anonymous shared memory`
- `shared_buffers`
- `max_connections`

先检查：

```bash
free -h
sysctl vm.overcommit_memory
```

然后确认实际生效参数，而不是只看你以为已经改过的本地文件。

处理原则：

- 优先降低 `shared_buffers`
- 再降低 `max_connections`
- 小内存环境先追求稳定启动，再做参数回调

## 5. `patronictl list` 没有任何输出

这类问题要和“集群坏了”分开看。

已确认过的直接根因：

- 早期打包的 `patronictl` wrapper 错误使用了：
  - `python3 -m patroni.ctl`
- 但 `patroni/ctl.py` 没有 `__main__` 入口
- 所以命令会静默退出 `0`

正确入口应该是：

```bash
python3 -c 'from patroni.ctl import ctl; ctl()' "$@"
```

如果怀疑现场还是旧包，先检查：

```bash
head -20 /opt/pg-deploy/patroni-runtime/bin/patronictl
```

## 6. 部署日志报 `no leader elected in Patroni cluster`

先区分“真的没有 leader”和“校验误判”。

当前工程已经不再依赖 `patronictl list` 表格输出，而是改为直接查询 Patroni REST API。

现场确认：

```bash
curl -s http://127.0.0.1:6432/patroni
curl -s http://127.0.0.1:6433/patroni
curl -s http://127.0.0.1:6434/patroni
```

如果能看到一个 `leader` 或 `primary`，那就不是集群没起来，而要回头看校验逻辑、端口映射或返回值解析。

## 7. `pg_hba.conf` 为空导致 PostgreSQL 直接退出

典型日志：

- `configuration file ".../pg_hba.conf" contains no entries`
- `could not load ... pg_hba.conf`

这不是认证失败，而是文件本身无有效规则。

先检查：

```bash
wc -l /data/<env>/<node>/pg_hba.conf
cat -n /data/<env>/<node>/pg_hba.conf
```

如果是空文件，优先回查部署和销毁流程中是否错误覆盖了数据目录内容。

## 8. 推荐最小排查命令集

Patroni 场景建议优先跑这组：

```bash
systemctl status patroni-<node> --no-pager -l
journalctl -u patroni-<node> -n 100 --no-pager
curl -s http://127.0.0.1:<rest_port>/patroni
ss -lntp | grep <pg_port>
ETCDCTL_API=3 etcdctl get /service --prefix --keys-only
```

如果要判断是不是旧环境没清干净，再补：

```bash
find /etc/patroni /etc/etcd -maxdepth 2 -type f 2>/dev/null
find /data -maxdepth 3 -type d | grep -E 'pg|patroni|etcd'
```
