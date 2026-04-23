-- +goose Up
-- +goose StatementBegin

-- ============================================================
-- 放宽代理探测目标:把仍停留在旧默认 gstatic 的记录清空,
-- 让 Prober 走内置候选链(api.ipify.org / cloudflare / httpbin),
-- 任意一个 2xx/3xx 即判代理可用,避免单一目标站被墙/限流时误判整条代理挂掉。
--
-- 仅在**用户从未改过**此配置(仍等于历史默认 gstatic)时清空;
-- 管理员已自定义的目标(比如内网探测地址)不会被覆盖。
-- 同步把描述文本更新为新的候选链说明。
-- ============================================================

UPDATE `system_settings`
SET    `v` = '',
       `description` = '探测目标 URL;留空(推荐)走内置候选链(ipify/cloudflare/httpbin),任一 2xx/3xx 即判成功;填单一 URL 则只用该地址'
WHERE  `k` = 'proxy.probe_target_url'
  AND  `v` = 'https://www.gstatic.com/generate_204';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- 不做回滚:清空值是向后兼容的(prober 空值时自动走候选链),无需恢复旧默认。
-- +goose StatementEnd
