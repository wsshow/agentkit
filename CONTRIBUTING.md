# Contributing to AgentKit

Thanks for helping improve AgentKit. Stability and ease of use come before adding more abstraction, so focused changes with clear tests are the easiest to review.

## Before opening a pull request

- Discuss large features in an issue before implementation.
- Keep each behavior change or fix in its own commit.
- Preserve public API compatibility unless a breaking change has been agreed on.
- Add regression tests for fixes and user-facing tests for new behavior.
- Update both `README.md` and `README_zh.md` when public usage changes.

Commit messages follow Conventional Commits with a concise Chinese summary, for example:

```text
feat: 新增会话存储
fix: 修复并发取消竞态
docs: 更新接入说明
```

## Local checks

AgentKit requires Go 1.25.14 or later. Run the same checks used by CI from the repository root:

```bash
test -z "$(gofmt -l .)"
go mod verify
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
go test -race -shuffle=on ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
```

Then verify the separate examples module:

```bash
cd examples
go mod verify
go test ./...
go vet ./...
```

## Pull requests

Explain the user-visible outcome, compatibility impact, and how the change was tested. Keep pull requests small enough to review as one coherent change and avoid unrelated formatting or dependency updates.
