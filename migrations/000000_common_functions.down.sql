-- rollback 000000 共享函数
DROP FUNCTION IF EXISTS forbid_mutation() CASCADE;
DROP FUNCTION IF EXISTS set_updated_at() CASCADE;
