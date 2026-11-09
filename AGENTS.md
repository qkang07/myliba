# AGENTS.md

## 验收约定

- 验收测试（`docs/unified-library-design.md` 验收矩阵 T01–T23、真机验证、性能基线）由用户本人执行，agent 完成任务后不需要跑验收，也不要在文档中代替用户填写验收结果。
- agent 完成的标志：代码实现完整、`go build`/`go vet`/现有单元与协议测试通过、前端 `pnpm typecheck` 通过（涉及前端改动时）、Android 改动可编译（涉及 Android 时）。
- 更新设计文档各阶段"当前进展"时，如实记录实现边界与剩余缺口，把验收项标注为"待用户验收"，不得标记阶段完成。

## 构建与测试

- 前端构建：`pnpm build:web`（产物 `cmd/myliba/web/dist/` 被 go:embed 打进二进制，新检出后必须先构建再 `go build`/`go test ./cmd/myliba`）。
- Go 构建需 CGO 与 `-tags sqlite_fts5`：`go build -tags sqlite_fts5 ./cmd/myliba`。
- Go 测试：`go test -tags sqlite_fts5 ./...`。
- 前端类型检查：`pnpm typecheck`。
- Android：在 `android/` 下运行 `./build.ps1`（使用仓库本地工具链 `.runtime/android-toolchain/`）。

## 项目结构速览

- Go 后端与入口：`cmd/myliba/`（`main.go` 按 `MYLIBA_MODE` 分发本地/hub/storage 三种模式）。
- Web 前端：`cmd/myliba/web/src/`（Vue 3 + TypeScript + Vite，根 `vite.config.ts`）。
- Android：`android/app/src/main/java/com/myliba/android/`（纯 Java）。
- 设计文档：`docs/`，统一资源库分期计划见 `docs/unified-library-design.md` §11。
