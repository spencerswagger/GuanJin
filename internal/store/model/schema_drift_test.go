package model_test

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

// testDatabaseURL 读取测试库 DSN；未设置时跳过（默认 make test 不依赖真实 PG）。
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("GUANJIN_TEST_DATABASE_URL 未设置，跳过 schema 漂移检查（需要真实 PostgreSQL 16）")
	}
	return dsn
}

// newTestDBWithMigrations 从空库开始跑迁移到最新，返回可用于 Statement.Parse 的 *gorm.DB。
// 先 DROP SCHEMA public CASCADE; CREATE SCHEMA public; 保证从空库开始。
func newTestDBWithMigrations(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := testDatabaseURL(t)

	raw, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	_, err = raw.Exec("DROP SCHEMA IF EXISTS public CASCADE")
	require.NoError(t, err)
	_, err = raw.Exec("CREATE SCHEMA public")
	require.NoError(t, err)

	require.NoError(t, store.MigrateUp(dsn))

	db, err := store.Open(store.Options{DSN: dsn})
	require.NoError(t, err)
	return db
}

// TestSchemaHasNoDrift 校验迁移列集合 ⊇ 每个 GORM 模型字段的 DBName（迁移为权威）。
func TestSchemaHasNoDrift(t *testing.T) {
	db := newTestDBWithMigrations(t)

	for _, m := range []any{
		&model.User{}, &model.Identifier{}, &model.Authenticator{}, &model.Challenge{},
		&model.Session{}, &model.AuthEvent{}, &model.AuditLog{}, &model.Client{},
		&model.LoginPolicy{}, &model.SigningKey{}, &model.RuntimeConfig{},
		&model.RoleBinding{}, &model.PolicySnapshot{}, &model.UserVisitedClient{},
	} {
		stmt := &gorm.Statement{DB: db}
		require.NoError(t, stmt.Parse(m))
		table := stmt.Schema.Table

		// 迁移里的真实列集合
		var dbCols []string
		require.NoError(t, db.Raw(`
			SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = ?`, table).Scan(&dbCols).Error)
		colSet := map[string]bool{}
		for _, c := range dbCols {
			colSet[c] = true
		}
		require.NotEmptyf(t, colSet, "表 %s 在迁移中不存在（无任何列）", table)

		// GORM 的每个字段都必须能在迁移里找到
		for _, f := range stmt.Schema.Fields {
			require.Truef(t, colSet[f.DBName],
				"GORM 模型 %s 的字段 %s（列 %s）在迁移中不存在 —— schema 漂移",
				table, f.Name, f.DBName)
		}
	}
}
