-- +goose Up
-- +goose StatementBegin
--
-- 账号图片额度探测间隔由 900s(15 分钟)调整为 18000s(5 小时):
--   - chatgpt.com 的 rate_limits 接口本身按天/小时桶计算,15 分钟探测太频繁,
--     对风控不友好,也没必要。改成 5h 常规轮询已经够用。
--   - 关键补足:DAO 的 ListNeedProbeQuota 加了一个分支 —— 当账号剩余额度=0
--     且已过 image_quota_reset_at,会忽略 5h 最小间隔立即补测,能第一时间
--     反映账号"归零等重置 → 重置后满额"的恢复状态。
--
-- 只更新用户仍在使用默认值的场景(v='900'),手工改过的值保持不动。
UPDATE `system_settings`
SET    `v`           = '18000',
       `description` = '额度探测最小间隔(秒);默认 18000=5h,剩余额度=0 且已过重置时间会忽略此间隔立即补探'
WHERE  `k` = 'account.quota_probe_interval_sec'
  AND  `v` = '900';
-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin
UPDATE `system_settings`
SET    `v`           = '900',
       `description` = '额度探测最小间隔(秒)'
WHERE  `k` = 'account.quota_probe_interval_sec'
  AND  `v` = '18000';
-- +goose StatementEnd
