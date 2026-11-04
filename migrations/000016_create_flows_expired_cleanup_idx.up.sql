-- 000016 flows 第二段清理索引（口径：decisions.md D5「第二段清理索引裁定」，2026-11-04）
-- 服务查询：DELETE FROM flows WHERE status='expired' AND updated_at < $1（repo_flows.go 的 deleteExpiredBeforeSQL）
-- 为什么新增而不是改造 000015 的 4 条：flows_expires_idx 是 (expires_at) WHERE status='active'，
--   列的语义与行集（active vs expired）都与本段不同，合并只会让两段都变差；其余 3 条各自服务别的谓词。
-- 为什么单开一条迁移：golang-migrate 只记版本号、不校验 checksum，改已 up 过的 000015 会让
--   已迁移的库静默缺索引（技术栈 §6.2：序号单调递增、不复用）。
-- 语义：updated_at 由 flows_set_updated_at 触发器维护，即「最后一次状态变更（置 expired）的时间」；
--   不新增列、不改触发器（D5 I-F6 已按现语义写死）。

CREATE INDEX flows_expired_cleanup_idx ON flows (updated_at) WHERE status = 'expired';
