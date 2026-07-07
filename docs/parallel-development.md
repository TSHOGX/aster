# 并行开发方案(git worktree)

在 `publish` 分支上并行开发多个功能时,用 `git worktree` 给每个功能开一个独立目录 + 独立分支,互不干扰,可以同时开多个终端/会话分别工作。

## 现状

仓库布局:

```
/home/agent-runtime-platform/
├── main/                          # main 分支 checkout
├── publish/                       # publish 分支 checkout(生产部署源,当前所在)
└── worktrees/
    └── <feature-name>/            # 每个并行开发功能一个独立 worktree,用完即删
```

每个 `worktrees/<name>/` 目录都是一个独立的 git worktree,checkout 在自己的分支上,共享同一个 `.git`(commit/object 存储共用,不占额外磁盘空间,但工作区文件是独立的)。按需新开,不要预先建通用/占位的 worktree。

## 新开一个并行开发分支

基于 `publish` 分支创建新 worktree:

```bash
git -C /home/agent-runtime-platform/publish worktree add \
  /home/agent-runtime-platform/worktrees/<feature-name> \
  -b <feature-name> publish
```

- `<feature-name>` 同时作为目录名和分支名,保持一致方便查找。
- 分支基于 `publish` 分支的最新 commit 创建。

## 使用方式

- 想在某个 worktree 里开发,直接把工作目录换到 `/home/agent-runtime-platform/worktrees/<feature-name>`(新开一个终端/会话,或者告诉 Claude "去 worktrees/<feature-name> 里做 xxx"),不影响其他 worktree 正在跑的东西。
- 每个 worktree 是独立的工作区,可以同时跑 `go build`、`npm run dev`、跑测试等,互不冲突。**但注意端口/数据目录冲突**——如果需要同时启动服务,确认各自的 `HARNESS_*` 端口和数据目录(参考 `docs/configuration.md`)不撞车。
- 开发完成后正常 `git push -u origin <feature-name>`,走 PR 流程合并回 `publish`(或 `main`,视目标而定)。

## 收尾:合并完清理 worktree

功能合并后,删除对应 worktree 和分支:

```bash
git -C /home/agent-runtime-platform/publish worktree remove /home/agent-runtime-platform/worktrees/<feature-name>
git -C /home/agent-runtime-platform/publish branch -d <feature-name>   # 已合并才能删,未合并需要 -D(会问用户确认)
```

## 给 Claude 的备注

以后如果用户说"在 publish 上并行开发 xxx"或类似需求,直接:

1. 用 `git -C /home/agent-runtime-platform/publish worktree add /home/agent-runtime-platform/worktrees/<name> -b <name> publish` 开新 worktree(名字从任务内容里提炼,或问用户)。
2. 后续该功能的所有改动都在这个 worktree 目录里进行,不要动 `publish/` 主目录。
3. 参考本文档,不用每次重新问"怎么开并行环境"。
