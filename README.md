# dm-exporter
这是一份为你量身定制的、可以直接提交到代码仓库（GitLab/GitHub）的专业级 `README.md`。

它采用了目前开源社区最标准的排版规范，包含了你从编译、部署到配置 Grafana 大盘所需的所有核心知识，堪称这份监控项目的“白皮书”。

你可以直接全选复制以下内容，保存为 `README.md`。

---

# 🐉 Dameng Fullstack Exporter (达梦全栈监控采集器)

**Dameng Fullstack Exporter** 是一个专为达梦数据库（DM7/DM8）及底层物理真机打造的高性能、高安全性的 Prometheus 监控采集采集器。

## ✨ 核心特性 (Features)

* 🔒 **金融级安全保护**：抛弃配置文件明文密码，采用 AES-GCM 算法与宿主机硬件指纹（Machine-ID + UUID）强绑定加密，密码文件离开本物理机即失效。
* 📊 **DBA级全景监控**：不仅监控基础状态，更深入内核采集表空间真实水位、事务吞吐量(TPS)、慢查询、死锁阻塞及缓冲池命中率。
* 🖥️ **OS 物理机融合**：内置 Node Exporter 核心功能（CPU、内存、各挂载点磁盘），单进程实现“数据库+物理机”双端监控，拒绝部署多套 Agent。
* 🛡️ **防雪崩与防假死**：内置连接池按期强制回收、Context 10秒硬超时切断机制，即使存储网络挂载卡死也不会拖垮监控进程。
* 📦 **极简部署**：纯 Go 语言编写，编译为单文件二进制，零外部依赖，即插即用。

---

## 🛠️ 1. 编译 (Build)

要求 Go 1.20 或以上版本。由于目标环境为 Linux 物理机，建议使用交叉编译并禁用 CGO，以获得绝对纯净的静态二进制文件：

```bash
# 在含有 main.go 的目录下执行
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dm-exporter main.go

```

编译完成后，你将得到一个 `dm-exporter` 可执行文件。将其上传至达梦数据库所在的物理机（建议放在 `/usr/local/bin/` 目录下）。

---

## 🚀 2. 部署与安装 (Installation)

### 步骤 1：生成硬件绑定加密凭据

在达梦所在的物理机上，使用 `root` 或 `sudo` 权限执行以下命令，将你的数据库密码（例如 `Dameng@123`）加密：

```bash
sudo /usr/local/bin/dm-exporter -encrypt "Dameng@123"

```

*执行成功后，系统会在 `/etc/dm-exporter/db.cipher` 生成加密文件。*

### 步骤 2：配置 Systemd 守护进程

创建一个 systemd 服务文件：

```bash
sudo vi /etc/systemd/system/dm-exporter.service

```

填入以下内容（**请根据实际情况修改 Environment 变量**）：

```ini
[Unit]
Description=Dameng & Host Fullstack Exporter
After=network.target

[Service]
Type=simple
User=root
# 在这里配置你的数据库账号和地址，无需修改代码重新编译
Environment="DM_USER=SYSDBA"
Environment="DM_HOST=127.0.0.1:5236"

ExecStart=/usr/local/bin/dm-exporter
Restart=on-failure
RestartSec=10s
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target

```

### 步骤 3：启动并设置开机自启

```bash
sudo systemctl daemon-reload
sudo systemctl enable dm-exporter
sudo systemctl start dm-exporter
sudo systemctl status dm-exporter

```

验证服务：访问 `http://<物理机IP>:9161/metrics`，看到指标数据即为成功。

---

## 📖 3. 核心指标说明 (Metrics Dictionary)

本 Exporter 暴露的指标分为两大类：`dm_*`（数据库指标）和 `host_*`（主机指标）。

| 指标名称 (Metric Name) | 类型 (Type) | 标签 (Labels) | 业务说明 (Description) |
| --- | --- | --- | --- |
| `dm_up` | Gauge | - | **[核心]** 数据库存活探针。1 为正常，0 为宕机/失联。 |
| `dm_uptime_seconds` | Gauge | - | 数据库实例从启动到现在的运行时长（秒）。 |
| `dm_tablespace_used_mb` | Gauge | `tablespace` | 各表空间已使用的真实容量。 |
| `dm_tablespace_total_mb` | Gauge | `tablespace` | 各表空间的总分配容量。 |
| `dm_archived_log_used_mb` | Gauge | - | 归档日志目录已占用的总容量。 |
| `dm_transaction_total` | Gauge | `type="commit/rollback"` | 累计事务计数，用于计算数据库 TPS。 |
| `dm_sessions_count` | Gauge | `state="ACTIVE/IDLE"` | 当前会话数分布状态。 |
| `dm_sessions_max` | Gauge | - | 数据库配置的最大允许连接数 (`MAX_SESSIONS`)。 |
| `dm_lock_waits_current` | Gauge | - | 当前因死锁或资源争用处于**阻塞等待**状态的事务数。 |
| `dm_slow_queries_current` | Gauge | - | 当前正在执行且**耗时超过 10 秒**的查询/事务数量。 |
| `dm_buffer_hit_ratio` | Gauge | - | 数据缓冲池命中率（100为满分，越高代表性能越好）。 |
| `host_cpu_usage_percent` | Gauge | - | 主机整体 CPU 使用率 (0-100)。 |
| `host_mem_usage_percent` | Gauge | - | 主机物理内存使用率 (0-100)。 |
| `host_disk_usage_percent` | Gauge | `path`, `fstype` | 各挂载点（如 `/`, `/data`）的磁盘使用率 (0-100)。 |

---

## 🔮 4. PromQL 秘籍 (Grafana 大盘配置指南)

在 Grafana 中配置大盘或灵雀云 Prometheus 告警规则时，请直接复制以下经过生产验证的 PromQL 语句（假设变量 `$instance` 为你的物理机 IP）：

### 🚨 P0 级致命告警规则

**1. 数据库宕机警报**

```promql
dm_up{instance=~"$instance"} == 0

```

*(说明：只要等于 0，立刻触发电话/飞书告警，业务已停摆。)*

**2. 物理表空间爆满预警 (使用率 > 90%)**

```promql
(dm_tablespace_used_mb{instance=~"$instance"} / dm_tablespace_total_mb{instance=~"$instance"}) * 100 > 90

```

*(说明：表空间写满会导致数据库直接 Hang 住，需提前扩容。)*

**3. 数据库阻塞/死锁警报**

```promql
dm_lock_waits_current{instance=~"$instance"} > 0

```

*(说明：配合“持续 3 分钟”规则使用，说明有长事务锁死了核心表，需 DBA 介入 Kill 进程。)*

### 📈 性能与水位看板 (Dashboard 面板)

**1. 计算数据库真实 TPS (每秒事务数)**
*图表类型：Time Series (折线图)*

```promql
rate(dm_transaction_total{instance=~"$instance", type="commit"}[1m])

```

*(说明：这是衡量数据库读写压力的最核心“心电图”。)*

**2. 连接池饱和度 (百分比)**
*图表类型：Gauge (仪表盘)*

```promql
(sum(dm_sessions_count{instance=~"$instance"}) / dm_sessions_max{instance=~"$instance"}) * 100

```

*(说明：监控是否即将达到最大连接数瓶颈，超过 80% 需预警。)*

**3. 慢查询/烂 SQL 实时捕获**
*图表类型：Stat (大数字面板)，背景设置红色阈值*

```promql
dm_slow_queries_current{instance=~"$instance"}

```

*(说明：只要大于 0，说明系统内有执行超过 10 秒的 SQL 正在拖垮资源。)*

**4. 物理磁盘健康度防爆盘 (找出最满的盘)**
*图表类型：Stat (大数字面板)，单位设为 Percent (0-100)*

```promql
max(host_disk_usage_percent{instance=~"$instance"})

```

*(说明：木桶原理，只要有一块盘（比如归档盘或数据盘）满了就会引发雪崩，监控最大使用率最安全。)*

---

## 🌐 5. 云原生集群集成 (Kubernetes)

如果你的 Exporter 运行在物理机，而监控平台（如灵雀云）在 Kubernetes 集群内，请使用 `Endpoints` 进行关联抓取。

```yaml
# 1. 声明外部 Endpoints (支持多台达梦集群节点)
apiVersion: v1
kind: Endpoints
metadata:
  name: dm-exporter-svc
  namespace: monitoring
subsets:
  - addresses:
      - ip: "192.168.1.71" 
      - ip: "192.168.1.72"
    ports:
      - port: 9161
        name: metrics

---
# 2. 声明 Service 映射
apiVersion: v1
kind: Service
metadata:
  name: dm-exporter-svc
  namespace: monitoring
  labels:
    service_name: dm-database-monitor
spec:
  ports:
    - name: metrics
      port: 9161
      protocol: TCP
      targetPort: 9161
  clusterIP: None 

---
# 3. 供 Prometheus Operator 抓取的 ServiceMonitor
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: dm-exporter-monitor
  namespace: monitoring
  labels:
    prometheus: kube-prometheus 
spec:
  jobLabel: service_name
  namespaceSelector:
    matchNames:
      - monitoring
  selector:
    matchLabels:
      service_name: dm-database-monitor
  endpoints:
    - port: metrics
      path: /metrics
      interval: 30s
      honorLabels: true

```
