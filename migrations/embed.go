// Package migrations 只做一件事：把 migrations/*.sql 嵌进二进制，供**测试与发布 Job** 使用。
//
// 明确边界（decisions.md P1）：
//   - 这里**没有**任何"应用启动时自动迁移"的入口，也不得添加；
//   - schema 的唯一权威是本目录下的 .sql，执行一律走 golang-migrate（手动 / 一次性 Job）。
package migrations

import "embed"

// Files 是 migrations 目录下全部 .sql 的只读副本（up/down 成对）。
//
//go:embed *.sql
var Files embed.FS
