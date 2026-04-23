-- +goose Up
-- +goose StatementBegin
--
-- 面板新增「4K 出图」能力。
--
-- 上游 chatgpt.com 生图原生只有 1024×1024 / 1792×1024 / 1024×1792 三档,
-- 这里的 upscale 字段记录"拿到原图后希望由本服务做多大倍的本地放大"。
-- 算法是 golang.org/x/image/draw.CatmullRom(biquintic 插值),属于传统算法,
-- 不是 AI 超分:只会让画面更平滑、更大,不会补出新的细节或纹理。
--
-- 值:
--   ''   原图直出(默认)
--   '2k' 长边 2560 PNG
--   '4k' 长边 3840 PNG
--
-- 放大执行时机:/v1/images/proxy/:task_id/:idx 首次被请求时,对单张图做
-- decode + 放大 + PNG 重编码,并放进进程内 LRU 缓存(默认 512MB),之后
-- 同一条代理 URL 的请求毫秒级命中,不会重复计算。
ALTER TABLE `image_tasks`
    ADD COLUMN `upscale` VARCHAR(8) NOT NULL DEFAULT '' AFTER `size`;
-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin
ALTER TABLE `image_tasks` DROP COLUMN `upscale`;
-- +goose StatementEnd
