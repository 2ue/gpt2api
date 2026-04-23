-- +goose Up
-- +goose StatementBegin

SET @has_biz_key := (
    SELECT COUNT(*)
      FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE()
       AND TABLE_NAME   = 'credit_transactions'
       AND COLUMN_NAME  = 'biz_key'
);

SET @sql_add_col := IF(@has_biz_key = 0,
    'ALTER TABLE `credit_transactions`
       ADD COLUMN `biz_key` VARCHAR(128) NULL DEFAULT NULL COMMENT ''可选业务幂等键,仅对需要强幂等的流水填充'' AFTER `ref_id`',
    'DO 0');
PREPARE s1 FROM @sql_add_col; EXECUTE s1; DEALLOCATE PREPARE s1;

SET @has_biz_key_idx := (
    SELECT COUNT(*)
      FROM information_schema.STATISTICS
     WHERE TABLE_SCHEMA = DATABASE()
       AND TABLE_NAME   = 'credit_transactions'
       AND INDEX_NAME   = 'uk_biz_key'
);

SET @sql_add_idx := IF(@has_biz_key_idx = 0,
    'ALTER TABLE `credit_transactions`
       ADD UNIQUE KEY `uk_biz_key` (`biz_key`)',
    'DO 0');
PREPARE s2 FROM @sql_add_idx; EXECUTE s2; DEALLOCATE PREPARE s2;

-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin

SET @has_biz_key_idx := (
    SELECT COUNT(*)
      FROM information_schema.STATISTICS
     WHERE TABLE_SCHEMA = DATABASE()
       AND TABLE_NAME   = 'credit_transactions'
       AND INDEX_NAME   = 'uk_biz_key'
);

SET @sql_drop_idx := IF(@has_biz_key_idx = 1,
    'ALTER TABLE `credit_transactions` DROP INDEX `uk_biz_key`',
    'DO 0');
PREPARE s3 FROM @sql_drop_idx; EXECUTE s3; DEALLOCATE PREPARE s3;

SET @has_biz_key := (
    SELECT COUNT(*)
      FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE()
       AND TABLE_NAME   = 'credit_transactions'
       AND COLUMN_NAME  = 'biz_key'
);

SET @sql_drop_col := IF(@has_biz_key = 1,
    'ALTER TABLE `credit_transactions` DROP COLUMN `biz_key`',
    'DO 0');
PREPARE s4 FROM @sql_drop_col; EXECUTE s4; DEALLOCATE PREPARE s4;

-- +goose StatementEnd
