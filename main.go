package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "gitee.com/chunanyong/dm"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// ==========================================
// 1. 全局配置与指标定义
// ==========================================
const cipherFilePath = "/etc/dm-exporter/db.cipher"

var (
	// --- 【核心】存活探针 ---
	dbUpMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dm_up",
		Help: "达梦数据库存活探针 (1: 正常, 0: 宕机或无法连接)",
	})

	// --- 数据库运行状态与容量指标 ---
	dbUptimeMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dm_uptime_seconds",
		Help: "达梦数据库实例已持续运行时长(秒)",
	})

	dbSizeMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dm_schema_used_size_mb",
		Help: "达梦数据库各Schema已用空间(MB)",
	}, []string{"schema"})

	dbTablespaceUsedMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dm_tablespace_used_mb",
		Help: "达梦数据库各表空间已用容量(MB)",
	}, []string{"tablespace"})
	
	dbTablespaceTotalMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dm_tablespace_total_mb",
		Help: "达梦数据库各表空间总容量(MB)",
	}, []string{"tablespace"})

	dbArchivedLogUsedMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dm_archived_log_used_mb",
		Help: "达梦数据库已生成的归档日志总计占用空间(MB)",
	})

	// --- 数据库性能与并发指标 ---
	dbSessionsMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dm_sessions_count",
		Help: "达梦数据库当前会话数量",
	}, []string{"state"})

	dbMaxSessionsMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dm_sessions_max",
		Help: "达梦数据库最大允许会话数配置 (MAX_SESSIONS)",
	})

	dbTxMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dm_transaction_total",
		Help: "达梦数据库事务累计次数(Commit/Rollback)",
	}, []string{"type"})

	dbLockWaitMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dm_lock_waits_current",
		Help: "达梦数据库当前处于等待状态的事务数(死锁/阻塞风险)",
	})

	dbSlowQueryMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dm_slow_queries_current",
		Help: "达梦数据库当前执行时间超过10秒的长事务/慢查询数量",
	})

	dbBufferHitRatioMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dm_buffer_hit_ratio",
		Help: "达梦数据库数据缓冲池命中率 (接近 100 为极佳)",
	})

	// --- 主机物理指标 ---
	cpuUsageMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "host_cpu_usage_percent",
		Help: "主机CPU总体使用率(%)",
	})
	memUsageMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "host_mem_usage_percent",
		Help: "主机内存已使用百分比(%)",
	})
	diskUsageMetric = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "host_disk_usage_percent",
		Help: "主机磁盘/挂载点使用百分比(%)",
	}, []string{"path", "fstype"})
)

func init() {
	prometheus.MustRegister(
		dbUpMetric, dbUptimeMetric, dbArchivedLogUsedMetric,
		dbSizeMetric, dbTablespaceUsedMetric, dbTablespaceTotalMetric, 
		dbSessionsMetric, dbMaxSessionsMetric, dbTxMetric, 
		dbLockWaitMetric, dbSlowQueryMetric, dbBufferHitRatioMetric,
		cpuUsageMetric, memUsageMetric, diskUsageMetric,
	)
}

// ==========================================
// 2. 硬件指纹加解密模块
// ==========================================

func getHardwareFingerprint() []byte {
	mID, err1 := os.ReadFile("/etc/machine-id")
	bID, err2 := os.ReadFile("/sys/class/dmi/id/product_uuid")

	if err1 != nil || err2 != nil {
		log.Fatalf("❌ 无法读取硬件信息，请确保以 root/sudo 运行。Err1: %v, Err2: %v", err1, err2)
	}

	s1 := strings.TrimSpace(string(mID))
	s2 := strings.TrimSpace(string(bID))
	
	raw := s1 + ":" + s2
	hash := sha256.Sum256([]byte(raw))
	return hash[:]
}

func encryptPassword(plain string) []byte {
	key := getHardwareFingerprint()
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	io.ReadFull(rand.Reader, nonce)
	return gcm.Seal(nonce, nonce, []byte(plain), nil)
}

func decryptPassword(enc []byte) string {
	key := getHardwareFingerprint()
	block, err := aes.NewCipher(key)
	if err != nil {
		log.Fatalf("❌ Cipher 初始化失败: %v", err)
	}
	
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		log.Fatalf("❌ GCM 初始化失败: %v", err)
	}
	
	nSize := gcm.NonceSize()
	if len(enc) < nSize {
		log.Fatalf("❌ 密文长度非法")
	}
	
	nonce, cipherText := enc[:nSize], enc[nSize:]
	plain, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		log.Fatalf("❌ 解密失败：密钥不匹配。请确认是否在同一台机器使用 sudo 执行。错误详情: %v", err)
	}
	return string(plain)
}

// ==========================================
// 3. 采集任务模块
// ==========================================

func fetchDatabaseMetrics(db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. 采集运行时间 (Uptime)
	var uptime float64
	err := db.QueryRowContext(ctx, "SELECT DATEDIFF(SS, START_TIME, SYSDATE) FROM V$INSTANCE").Scan(&uptime)
	if err == nil {
		dbUptimeMetric.Set(uptime)
	}

	// 2. 采集缓冲池命中率 (Buffer Hit Ratio) - 兼容 STAT_VAL
	var hitRatio float64
	hitRatioSQL := "SELECT STAT_VAL FROM V$SYSSTAT WHERE UPPER(NAME) LIKE '%BUFFER HIT RATIO%'"
	err = db.QueryRowContext(ctx, hitRatioSQL).Scan(&hitRatio)
	if err == nil {
		dbBufferHitRatioMetric.Set(hitRatio)
	}

	// 3. 采集归档日志占用空间总量 (MB)
	var archSize float64
	archSQL := "SELECT NVL(SUM(FSIZE)/1024/1024, 0) FROM V$ARCHIVED_LOG"
	err = db.QueryRowContext(ctx, archSQL).Scan(&archSize)
	if err == nil {
		dbArchivedLogUsedMetric.Set(archSize)
	}

	// 4. 采集会话数
	rows1, err := db.QueryContext(ctx, "SELECT STATE, COUNT(*) FROM V$SESSIONS GROUP BY STATE")
	if err == nil {
		for rows1.Next() {
			var state string
			var count float64
			if err := rows1.Scan(&state, &count); err == nil {
				dbSessionsMetric.WithLabelValues(state).Set(count)
			}
		}
		rows1.Close()
	}

	// 5. 采集表空间
	tsQuery := `
		SELECT 
			a.tablespace_name, 
			a.total_mb, 
			NVL(a.total_mb - b.free_mb, a.total_mb) as used_mb 
		FROM 
			(SELECT tablespace_name, SUM(bytes)/1024/1024 as total_mb FROM dba_data_files GROUP BY tablespace_name) a 
		LEFT JOIN 
			(SELECT tablespace_name, SUM(bytes)/1024/1024 as free_mb FROM dba_free_space GROUP BY tablespace_name) b 
		ON a.tablespace_name = b.tablespace_name`
	
	rows2, err := db.QueryContext(ctx, tsQuery)
	if err == nil {
		for rows2.Next() {
			var ts string
			var total, used float64
			if err := rows2.Scan(&ts, &total, &used); err == nil {
				dbTablespaceTotalMetric.WithLabelValues(ts).Set(total)
				dbTablespaceUsedMetric.WithLabelValues(ts).Set(used)
			}
		}
		rows2.Close()
	}

	// 6. 采集事务吞吐 (TPS) - 使用正确的 STAT_VAL 字段
	txQuery := "SELECT NAME, STAT_VAL FROM V$SYSSTAT WHERE UPPER(NAME) LIKE '%COMMIT%' OR UPPER(NAME) LIKE '%ROLLBACK%'"
	rows3, err := db.QueryContext(ctx, txQuery)
	if err == nil {
		for rows3.Next() {
			var name string
			var value float64
			if err := rows3.Scan(&name, &value); err == nil {
				metricType := "rollback"
				if strings.Contains(strings.ToUpper(name), "COMMIT") {
					metricType = "commit"
				}
				dbTxMetric.WithLabelValues(metricType).Set(value)
			}
		}
		rows3.Close()
	}

	// 7. 采集锁与等待
	var waitCount float64
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM V$TRXWAIT").Scan(&waitCount)
	if err == nil {
		dbLockWaitMetric.Set(waitCount)
	}

	// 8. 采集 Schema 空间
	rows5, err := db.QueryContext(ctx, "SELECT OWNER, SUM(BYTES)/1024/1024 FROM DBA_SEGMENTS GROUP BY OWNER")
	if err == nil {
		for rows5.Next() {
			var schema string
			var size float64
			if err := rows5.Scan(&schema, &size); err == nil {
				dbSizeMetric.WithLabelValues(schema).Set(size)
			}
		}
		rows5.Close()
	}

	// 9. 采集最大连接数限制 (MAX_SESSIONS)
	var maxSessions float64
	err = db.QueryRowContext(ctx, "SELECT PARA_VALUE FROM V$DM_INI WHERE PARA_NAME = 'MAX_SESSIONS'").Scan(&maxSessions)
	if err == nil {
		dbMaxSessionsMetric.Set(maxSessions)
	}

	// 10. 采集慢查询/长事务预警
	var slowQueryCount float64
	slowQuerySQL := "SELECT COUNT(*) FROM V$SESSIONS WHERE STATE='ACTIVE' AND DATEDIFF(SS, LAST_SEND_TIME, SYSDATE) > 10"
	err = db.QueryRowContext(ctx, slowQuerySQL).Scan(&slowQueryCount)
	if err == nil {
		dbSlowQueryMetric.Set(slowQueryCount)
	}
}

func fetchHostMetrics() {
	go func() {
		if c, err := cpu.Percent(time.Second, false); err == nil && len(c) > 0 {
			cpuUsageMetric.Set(c[0])
		}
	}()

	if v, err := mem.VirtualMemory(); err == nil {
		memUsageMetric.Set(v.UsedPercent)
	}

	parts, err := disk.Partitions(false)
	if err != nil {
		return
	}

	for _, p := range parts {
		if strings.Contains(p.Mountpoint, "/var/lib/docker") || 
		   strings.Contains(p.Mountpoint, "/var/lib/containerd") ||
		   strings.Contains(p.Mountpoint, "loop") {
			continue
		}

		go func(path, fstype string) {
			usage, err := disk.Usage(path)
			if err == nil {
				diskUsageMetric.WithLabelValues(path, fstype).Set(usage.UsedPercent)
			}
		}(p.Mountpoint, p.Fstype)
	}
}

// ==========================================
// 4. 主程序
// ==========================================

func main() {
	encryptFlag := flag.String("encrypt", "", "加密并保存密码")
	flag.Parse()

	// 初始化模式
	if *encryptFlag != "" {
		_ = os.MkdirAll("/etc/dm-exporter", 0700)
		data := encryptPassword(*encryptFlag)
		_ = os.WriteFile(cipherFilePath, data, 0600)
		fmt.Printf("✅ 硬件绑定密文已生成: %s\n", cipherFilePath)
		return
	}

	// 正常运行模式
	log.Println("🛠️  Dameng Fullstack Exporter (Industrial Edition) 正在启动...")

	encData, err := os.ReadFile(cipherFilePath)
	if err != nil {
		log.Fatalf("❌ 缺失密文文件，请先执行 -encrypt")
	}
	dbPass := decryptPassword(encData)

	dbUser := os.Getenv("DM_USER")
	if dbUser == "" {
		dbUser = "SYSDBA"
	}
	dbHost := os.Getenv("DM_HOST")
	if dbHost == "" {
		dbHost = "127.0.0.1:5236"
	}

	dsn := fmt.Sprintf("dm://%s:%s@%s?autoCommit=true", dbUser, dbPass, dbHost)
	db, err := sql.Open("dm", dsn)
	if err != nil {
		log.Printf("⚠️  数据库引擎初始化失败: %v", err)
	} else {
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(5 * time.Minute)
	}

	// 后台异步采集主机指标
	go func() {
		for {
			fetchHostMetrics()
			time.Sleep(15 * time.Second)
		}
	}()

	// ⭐️ 后台异步采集数据库指标 (包含高可用探针逻辑)
	go func() {
		for {
			if db != nil {
				// 每次采集前先 Ping，成功则 up=1，失败则 up=0 且不执行采集
				if err := db.Ping(); err == nil {
					dbUpMetric.Set(1)
					fetchDatabaseMetrics(db)
				} else {
					dbUpMetric.Set(0)
					log.Printf("⚠️ 数据库 Ping 失败，当前判定为不可用状态: %v", err)
				}
			} else {
				dbUpMetric.Set(0)
			}
			time.Sleep(60 * time.Second)
		}
	}()

	// 启动 HTTP 服务
	http.Handle("/metrics", promhttp.Handler())
	server := &http.Server{Addr: ":9161"}
	
	go func() {
		log.Println("🌐 监听地址 :9161/metrics")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ HTTP服务异常退出: %v", err)
		}
	}()

	// 优雅关停机制
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop 
	
	log.Println("⚠️  收到停止信号，正在安全关闭服务...")
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("❌ HTTP 服务强行关闭: %v", err)
	}
	
	if db != nil {
		db.Close()
	}
	
	log.Println("👋 服务已安全退出")
}
