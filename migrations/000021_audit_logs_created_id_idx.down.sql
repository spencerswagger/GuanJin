-- rollback 000021：只撤本迁移引入的链序索引，不碰 000011 的 4 条取证索引与主键
DROP INDEX IF EXISTS audit_logs_created_id_idx;
