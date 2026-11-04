-- rollback 000016：只撤销本迁移引入的第二段清理索引，不碰 000015 的 4 条具名索引
DROP INDEX IF EXISTS flows_expired_cleanup_idx;
