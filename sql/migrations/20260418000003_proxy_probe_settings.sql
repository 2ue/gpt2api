-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 代理池健康探测相关的 system_settings key 种子。
-- 已存在的 key 保留原值,不覆盖管理员已调整的设置。
-- ============================================================

INSERT INTO `system_settings` (`k`, `v`, `description`) VALUES
    ('proxy.probe_enabled',      'true',                                     '代理池健康探测总开关;关闭后不再定时探测,health_score 停止刷新'),
    ('proxy.probe_interval_sec', '300',                                      '两轮定时探测之间的间隔(秒),建议 ≥ 60'),
    ('proxy.probe_timeout_sec',  '10',                                       '单条代理一次探测的超时时间(秒)'),
    ('proxy.probe_target_url',   '',                                         '探测目标 URL;留空(推荐)走内置候选链(ipify/cloudflare/httpbin),任一 2xx/3xx 即判成功;填单一 URL 则只用该地址'),
    ('proxy.probe_concurrency',  '8',                                        '同时进行探测的并发数(1~64)')
ON DUPLICATE KEY UPDATE `k` = VALUES(`k`);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- 不删除数据:保留历史配置便于回滚。
-- +goose StatementEnd
