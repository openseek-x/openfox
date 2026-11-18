-- 阶段 12d「排队与等待」（G-05、F-06）：允许 Run 以失败码 WORKDIR_BUSY 结束。
--
-- 背景：支持 workdir_busy_requeue 能力位的 daemon 在目录被本机其他 Run 占用时向中心回报，中心把 Run 放回排队
-- （wait_reason = WORKDIR_BUSY，该等待原因自 000011 起已在 runs_wait_reason_valid 中）；回报次数超过上限后
-- 以失败码 WORKDIR_BUSY 结束。旧约束 runs_failure_code_valid（000019）不含该值，需要放宽。
--
-- 仅扩展（expand-only）：新约束是旧约束的超集，旧行全部满足。
-- 锁：DROP/ADD ... NOT VALID 只短暂持有 ACCESS EXCLUSIVE；VALIDATE 持 SHARE UPDATE EXCLUSIVE 扫描 runs 一遍，
--     不阻塞读写（与 000062 相同做法）。无回填。
-- 回滚：应用回退后保留该约束即可（旧二进制不会写 WORKDIR_BUSY）；不提供 down 迁移。若确需恢复旧约束，
--     必须先确认没有 failure_code = 'WORKDIR_BUSY' 的行，否则 ADD CONSTRAINT 会失败。

SET ROLE openfox_migrator;

ALTER TABLE openfox.runs DROP CONSTRAINT runs_failure_code_valid;
ALTER TABLE openfox.runs ADD CONSTRAINT runs_failure_code_valid CHECK (
    failure_code IN ('', 'CREDENTIAL_REJECTED', 'QUOTA_EXHAUSTED', 'RATE_LIMITED',
       'PROVIDER_OVERLOADED', 'MODEL_NOT_FOUND', 'CONTEXT_TOO_LARGE',
       'IMAGE_TOO_LARGE', 'NETWORK_UNREACHABLE', 'WORKTREE_PREP_FAILED',
       'REPO_UNAVAILABLE', 'RUNTIME_UNAVAILABLE', 'QUEUE_TIMEOUT',
       'RUN_TIMEOUT', 'AUTHORIZATION_REVOKED', 'UNKNOWN_PROCESS',
       'PREPARE_FAILED', 'INTERNAL', 'WORKDIR_BUSY')
) NOT VALID;
ALTER TABLE openfox.runs VALIDATE CONSTRAINT runs_failure_code_valid;

RESET ROLE;
