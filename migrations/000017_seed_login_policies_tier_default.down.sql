-- 回滚：只删本迁移 seed 的那三行（按固定 UUID 精确删，不动运维自建的 client_override 行）
DELETE FROM login_policies
WHERE scope = 'tier_default'
  AND id IN (
    '01910000-0000-7000-8000-0000000000a1',
    '01910000-0000-7000-8000-0000000000a2',
    '01910000-0000-7000-8000-0000000000a3'
  );
