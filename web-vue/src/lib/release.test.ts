import { describe, expect, it } from "vite-plus/test";
import { parseChangelog } from "./release";

describe("parseChangelog", () => {
  it("parses current version, group, and bullet format", () => {
    const releases = parseChangelog(`# 更新记录

## 未发布

## v1.3.0

### 图片生成与任务

- 新增 gpt-image-2.5 入口。
- 图片轮询遵守 Retry-After。

### 账号与凭证

- CPA 导入账号由原渠道独占管理。

### 工程与安全

- 前端工具链迁移到 Vite+。
`);

    expect(releases).toEqual([
      {
        version: "v1.3.0",
        date: "",
        items: [
          { type: "图片生成与任务", content: "新增 gpt-image-2.5 入口。" },
          { type: "图片生成与任务", content: "图片轮询遵守 Retry-After。" },
          { type: "账号与凭证", content: "CPA 导入账号由原渠道独占管理。" },
          { type: "工程与安全", content: "前端工具链迁移到 Vite+。" },
        ],
      },
    ]);
  });

  it("keeps the old plus-typed item format", () => {
    expect(
      parseChangelog(`## v1.2.0

+ [新增] 账号导入
+ [修复] 刷新失败
`),
    ).toEqual([
      {
        version: "v1.2.0",
        date: "",
        items: [
          { type: "新增", content: "账号导入" },
          { type: "修复", content: "刷新失败" },
        ],
      },
    ]);
  });

  it("parses version titles with dates", () => {
    expect(
      parseChangelog(`## v1.2.3 - 2026-01-02

+ [优化] 配额快照
`),
    ).toEqual([
      {
        version: "v1.2.3",
        date: "2026-01-02",
        items: [{ type: "优化", content: "配额快照" }],
      },
    ]);
  });

  it("uses a default type when bullets have no group", () => {
    expect(
      parseChangelog(`## v1.2.4

- 提供 Go 服务、GPT 聊天及图片接口。
`),
    ).toEqual([
      {
        version: "v1.2.4",
        date: "",
        items: [{ type: "变更", content: "提供 Go 服务、GPT 聊天及图片接口。" }],
      },
    ]);
  });

  it("keeps mixed formats without dropping items", () => {
    expect(
      parseChangelog(`## v1.1.0 - 2026-03-01

### 工程

- 分组条目
- [新增] 显式类型条目
+ [修复] 旧格式条目

- 空白行后仍沿用最近分组
`),
    ).toEqual([
      {
        version: "v1.1.0",
        date: "2026-03-01",
        items: [
          { type: "工程", content: "分组条目" },
          { type: "新增", content: "显式类型条目" },
          { type: "修复", content: "旧格式条目" },
          { type: "工程", content: "空白行后仍沿用最近分组" },
        ],
      },
    ]);
  });
});
