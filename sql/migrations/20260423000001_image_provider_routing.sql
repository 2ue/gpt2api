-- +goose Up
-- +goose StatementBegin

ALTER TABLE `oai_accounts`
    ADD COLUMN `provider_kind`            VARCHAR(32)  NOT NULL DEFAULT 'reverse' AFTER `account_type`,
    ADD COLUMN `api_key_enc`              TEXT         NULL AFTER `session_token_enc`,
    ADD COLUMN `api_base_url`             VARCHAR(255) NOT NULL DEFAULT 'https://api.openai.com/v1' AFTER `client_id`,
    ADD COLUMN `image_capabilities_json`  JSON         NULL AFTER `api_base_url`,
    ADD COLUMN `same_account_retry_limit` INT          NOT NULL DEFAULT 1 AFTER `image_capabilities_json`,
    ADD COLUMN `priority`                 INT          NOT NULL DEFAULT 0 AFTER `same_account_retry_limit`;

CREATE INDEX `idx_provider_status_priority` ON `oai_accounts` (`provider_kind`, `status`, `priority`);

ALTER TABLE `image_tasks`
    ADD COLUMN `operation`            VARCHAR(16)  NOT NULL DEFAULT 'generate' AFTER `size`,
    ADD COLUMN `provider_kind`        VARCHAR(32)  NOT NULL DEFAULT 'reverse' AFTER `operation`,
    ADD COLUMN `route_policy`         VARCHAR(16)  NOT NULL DEFAULT 'auto' AFTER `provider_kind`,
    ADD COLUMN `request_options_json` JSON         NULL AFTER `route_policy`,
    ADD COLUMN `attempt_count`        INT          NOT NULL DEFAULT 0 AFTER `request_options_json`,
    ADD COLUMN `switch_count`         INT          NOT NULL DEFAULT 0 AFTER `attempt_count`;

CREATE TABLE IF NOT EXISTS `image_task_outputs` (
    `id`             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    `task_id`        VARCHAR(64)     NOT NULL,
    `output_index`   INT             NOT NULL,
    `source_type`    VARCHAR(32)     NOT NULL COMMENT 'chatgpt_ref | stored_blob | remote_url',
    `source_ref`     TEXT            NOT NULL,
    `content_type`   VARCHAR(128)    NOT NULL DEFAULT 'image/png',
    `revised_prompt` TEXT            NULL,
    `meta_json`      JSON            NULL,
    `created_at`     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_task_output` (`task_id`, `output_index`),
    KEY `idx_task_id` (`task_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='统一图片产物';

INSERT INTO `system_settings` (`k`, `v`, `description`) VALUES
    ('image.reverse_only', 'false', '强制所有图片请求仅走 reverse provider'),
    ('image.native_enabled', 'true', '允许图片请求使用 native provider'),
    ('image.responses_fallback_enabled', 'true', '允许 Responses provider 参与自动图片路由'),
    ('image.responses_direct_enabled', 'true', '允许独立 Responses 图片接口对外提供服务'),
    ('image.safe_mode', 'false', '启用后默认排除 reverse provider')
ON DUPLICATE KEY UPDATE `k` = VALUES(`k`);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DELETE FROM `system_settings` WHERE `k` IN (
    'image.reverse_only',
    'image.native_enabled',
    'image.responses_fallback_enabled',
    'image.responses_direct_enabled',
    'image.safe_mode'
);

DROP TABLE IF EXISTS `image_task_outputs`;

ALTER TABLE `image_tasks`
    DROP COLUMN `switch_count`,
    DROP COLUMN `attempt_count`,
    DROP COLUMN `request_options_json`,
    DROP COLUMN `route_policy`,
    DROP COLUMN `provider_kind`,
    DROP COLUMN `operation`;

ALTER TABLE `oai_accounts`
    DROP INDEX `idx_provider_status_priority`,
    DROP COLUMN `priority`,
    DROP COLUMN `same_account_retry_limit`,
    DROP COLUMN `image_capabilities_json`,
    DROP COLUMN `api_base_url`,
    DROP COLUMN `api_key_enc`,
    DROP COLUMN `provider_kind`;

-- +goose StatementEnd
