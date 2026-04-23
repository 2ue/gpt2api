-- +goose Up
-- +goose StatementBegin

-- oai_accounts 扩展:
--   - session_token_enc:  ST(__Secure-next-auth.session-token)加密存储,可选
--   - client_id:          OAuth client_id,RT → AT 刷新时必填
--   - chatgpt_account_id: 账号在 chatgpt.com 的 uuid
--   - account_type:       codex / chatgpt / plus / team / free
--   - last_refresh_at:    最近一次 AT 刷新时间
--   - last_refresh_source: rt / st / manual
--   - refresh_error:      最近一次失败原因(中文友好)
--   - image_quota_remaining / image_quota_total: 剩余 / 总量(-1 未知)
--   - image_quota_reset_at / image_quota_updated_at: 重置 / 探测时间

ALTER TABLE `oai_accounts`
    ADD COLUMN `session_token_enc`      TEXT         NULL                                    AFTER `refresh_token_enc`,
    ADD COLUMN `client_id`               VARCHAR(64)  NOT NULL DEFAULT 'app_EMoamEEZ73f0CkXaXp7hrann' AFTER `oai_device_id`,
    ADD COLUMN `chatgpt_account_id`      VARCHAR(64)  NOT NULL DEFAULT ''                     AFTER `client_id`,
    ADD COLUMN `account_type`            VARCHAR(32)  NOT NULL DEFAULT 'codex'                AFTER `chatgpt_account_id`,
    ADD COLUMN `last_refresh_at`         DATETIME     NULL                                    AFTER `today_used_date`,
    ADD COLUMN `last_refresh_source`     VARCHAR(8)   NOT NULL DEFAULT ''                     AFTER `last_refresh_at`,
    ADD COLUMN `refresh_error`           VARCHAR(500) NOT NULL DEFAULT ''                     AFTER `last_refresh_source`,
    ADD COLUMN `image_quota_remaining`   INT          NOT NULL DEFAULT -1                     AFTER `refresh_error`,
    ADD COLUMN `image_quota_total`       INT          NOT NULL DEFAULT -1                     AFTER `image_quota_remaining`,
    ADD COLUMN `image_quota_reset_at`    DATETIME     NULL                                    AFTER `image_quota_total`,
    ADD COLUMN `image_quota_updated_at`  DATETIME     NULL                                    AFTER `image_quota_reset_at`;

-- 补索引:按过期时间扫待刷新账号
CREATE INDEX `idx_token_expires`     ON `oai_accounts` (`token_expires_at`);
CREATE INDEX `idx_quota_updated_at`  ON `oai_accounts` (`image_quota_updated_at`);

-- 新增 settings 默认值(账号刷新 / 额度探测)
INSERT INTO `system_settings` (`k`, `v`, `description`) VALUES
    ('account.refresh_enabled',          'true',                                 '账号 AccessToken 自动刷新总开关'),
    ('account.refresh_interval_sec',     '120',                                  '后台扫描间隔(秒),判断哪些账号需要刷新'),
    ('account.refresh_ahead_sec',        '900',                                  '距离过期多少秒内触发预刷新(建议 ≥ 300)'),
    ('account.refresh_concurrency',      '4',                                    '同时刷新的账号数(1~32)'),
    ('account.quota_probe_enabled',      'true',                                 '账号图片额度自动探测总开关'),
    ('account.quota_probe_interval_sec', '18000',                                '额度探测最小间隔(秒);默认 18000=5h,剩余额度=0 且已过重置时间会忽略此间隔立即补探'),
    ('account.default_client_id',        'app_EMoamEEZ73f0CkXaXp7hrann',         '导入账号时未指定 client_id 则使用此值')
ON DUPLICATE KEY UPDATE `k` = VALUES(`k`);

-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin
ALTER TABLE `oai_accounts`
    DROP INDEX `idx_quota_updated_at`,
    DROP INDEX `idx_token_expires`,
    DROP COLUMN `image_quota_updated_at`,
    DROP COLUMN `image_quota_reset_at`,
    DROP COLUMN `image_quota_total`,
    DROP COLUMN `image_quota_remaining`,
    DROP COLUMN `refresh_error`,
    DROP COLUMN `last_refresh_source`,
    DROP COLUMN `last_refresh_at`,
    DROP COLUMN `account_type`,
    DROP COLUMN `chatgpt_account_id`,
    DROP COLUMN `client_id`,
    DROP COLUMN `session_token_enc`;

DELETE FROM `system_settings` WHERE `k` IN (
    'account.refresh_enabled',
    'account.refresh_interval_sec',
    'account.refresh_ahead_sec',
    'account.refresh_concurrency',
    'account.quota_probe_enabled',
    'account.quota_probe_interval_sec',
    'account.default_client_id'
);
-- +goose StatementEnd
