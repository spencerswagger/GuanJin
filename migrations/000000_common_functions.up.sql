-- 000000 共享函数（所有可变表依赖）
-- 口径：数据模型规格 §1.1；只增表保护用于 auth_events / audit_logs

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END $$;

-- 只增表保护：禁止 UPDATE / DELETE（SQLSTATE 23001 restrict_violation）
CREATE OR REPLACE FUNCTION forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'append-only relation "%": % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END $$;
