//go:build integration

package store

// **D20 的落地证据**：链序索引 `audit_logs_created_id_idx (created_at, id)` 必须
//   ① 在父表上、② 在**每一个现有分区**上（且是挂在父索引下的子索引）、
//   ③ 由 `audit.EnsurePartitions` 建出的**新分区自动继承**。
//
// 为什么放在 `internal/store`：这条索引是**迁移产物**（`migrations/000021`），
// 而 `internal/audit` 的用例默认跑在自建的隔离 schema 里（`audit_test`，表是手工建的、不含迁移索引）。
// 索引形状类断言本来就归 store（先例：`flows_index_db_test.go`）。
//
// 为什么要一条用例钉这些：分区索引"少一个分区"时规划器**就不会用**它（每分区独立规划），
// 性能悄悄退回 Seq Scan —— 功能用例完全看不出来；"新分区没继承"则只会在下个月第一次
// 写审计时暴露（那时没人盯 plan）。
//
// 命名注意（写错会假红/假绿）：PG 给分区建的子索引名字是 `audit_logs_pYYYYMM_created_at_id_idx`，
// **不是父索引名**——所以不能按"索引名相同"去数分区，必须按 `pg_inherits` 的父子关系判。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/audit"
)

const chainOrderIdxName = "audit_logs_created_id_idx"

// partitionIndexState 返回"分区 → 是否挂着父索引的子索引"，**一条 SQL 一次快照**。
//
// 为什么要合成一条：先枚举分区、再逐个查继承关系是 **TOCTOU** —— `-p 8` 下同库的
// `internal/audit` 用例会在收尾 `DropPartitions`，两次查询之间分区就没了，
// 于是 `42P01 relation "audit_logs_pYYYYMM" does not exist`（实测偶发红一次）。
// 一条语句里枚举+判定，拿的是同一个 MVCC 快照，不会再出现"查到一半消失"。
func partitionIndexState(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()
	var rows []struct {
		Partition string
		HasIndex  bool
	}
	err := db.Raw(`SELECT c.relname::text AS partition,
			EXISTS (
				SELECT 1 FROM pg_inherits h
				  JOIN pg_index i ON i.indexrelid = h.inhrelid
				 WHERE h.inhparent = ?::regclass AND i.indrelid = c.oid
			) AS has_index
		FROM pg_class c
		JOIN pg_inherits pi ON pi.inhrelid = c.oid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE pi.inhparent = 'audit_logs'::regclass AND n.nspname = current_schema()
		ORDER BY 1`, chainOrderIdxName).Scan(&rows).Error
	if err != nil {
		t.Fatalf("读 audit_logs 分区的索引状态：%v", err)
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.Partition] = r.HasIndex
	}
	return out
}

// partitionHasChainOrderIndex 报告某分区上是否挂着**父索引的子索引**（不看子索引名字）。
func partitionHasChainOrderIndex(t *testing.T, db *gorm.DB, partition string) bool {
	t.Helper()
	var n int
	err := db.Raw(`SELECT count(*) FROM pg_inherits h
		 JOIN pg_index i ON i.indexrelid = h.inhrelid
		WHERE h.inhparent = ?::regclass AND i.indrelid = ?::regclass`,
		chainOrderIdxName, partition).Scan(&n).Error
	if err != nil {
		t.Fatalf("查分区 %s 的索引继承关系：%v", partition, err)
	}
	return n > 0
}

func TestDBAuditCreatedIDIndexOnParentAndEveryPartition(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// ① 父索引存在：分区表的父索引 relkind='I'，必须在 audit_logs 所在的 schema 里。
	var parentKind string
	if err := db.Raw(`SELECT c.relkind::text FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema()`,
		chainOrderIdxName).Row().Scan(&parentKind); err != nil {
		t.Fatalf("父表上没有 %s（迁移 000021 没跑？）：%v", chainOrderIdxName, err)
	}
	if parentKind != "I" && parentKind != "i" {
		t.Fatalf("父索引 relkind 期望 I/i（分区父索引），实际 %q", parentKind)
	}

	// ② 每个现有分区都必须挂着它的子索引（一条 SQL 一次快照，避免与并行包的 DDL 抢出 TOCTOU）。
	state := partitionIndexState(t, db)
	if len(state) == 0 {
		t.Fatal("audit_logs 竟然没有分区（D4-C 要求按月分区）")
	}
	for p, hasIndex := range state {
		if !hasIndex {
			t.Errorf("分区 %s 上没有 %s 的子索引：分区索引不全 → 规划器会退回逐分区 Seq Scan",
				p, chainOrderIdxName)
		}
	}

	// ③ 形状：`(created_at, id)`、无 DESC、无 partial、非唯一（D20 的"不增不减"）。
	var def string
	if err := db.Raw(`SELECT pg_get_indexdef(?::regclass)`, chainOrderIdxName).Row().Scan(&def); err != nil {
		t.Fatalf("读父索引定义：%v", err)
	}
	if !strings.Contains(def, "btree (created_at, id)") {
		t.Errorf("索引列必须是 (created_at, id)（列序即链序），实际：%s", def)
	}
	upper := strings.ToUpper(def)
	if strings.Contains(upper, "DESC") {
		t.Errorf("不许带 DESC（btree 可反向扫，DESC 只是白占空间的镜像）：%s", def)
	}
	if strings.Contains(upper, "WHERE") {
		t.Errorf("不许是 partial 索引（链尾读没有可用谓词）：%s", def)
	}
	if strings.Contains(upper, "UNIQUE") {
		t.Errorf("不许是唯一索引（链上 created_at 允许并列）：%s", def)
	}

	// ④ 新分区自动继承（`EnsurePartitions` 走 `CREATE TABLE … PARTITION OF`）。
	now := time.Now().UTC()
	future := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 24, 0)
	name := fmt.Sprintf("audit_logs_p%s", future.Format("200601"))
	if err := audit.EnsurePartitions(ctx, db, []time.Time{future}); err != nil {
		t.Fatalf("EnsurePartitions(%s)：%v", name, err)
	}
	t.Cleanup(func() { _ = audit.DropPartitions(context.Background(), db, []time.Time{future}) })
	if !partitionHasChainOrderIndex(t, db, name) {
		t.Fatalf("新建分区 %s 没有继承 %s：父索引没被当作模板（D20 的前提不成立）", name, chainOrderIdxName)
	}
}
