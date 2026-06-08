package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"

	"github.com/wsshow/agentkit"
	"github.com/wsshow/agentkit/examples/internal/demo"
)

func main() {
	ctx := context.Background()

	chatModel, err := demo.NewChatModel(ctx)
	if err != nil {
		log.Fatalln(err)
	}

	agent, err := agentkit.New(ctx, &agentkit.Config{
		Name:         "vision-assistant",
		SystemPrompt: "你是一个视觉助手，擅长描述和分析图片内容。回答请简洁明了。",
		Model:        chatModel,
	})
	if err != nil {
		log.Fatalln(err)
	}
	defer agent.Close()

	demo.SubscribeText(agent)

	// ── 场景 1：文本 + 图片 URL ──────────────────────────────────────────────
	// 注意：必须使用图片的直链（返回图片二进制），不能使用 HTML 页面链接。
	// GitHub 图片直链格式：https://raw.githubusercontent.com/{owner}/{repo}/{branch}/{path}
	fmt.Println("═══ 场景 1：文本 + 图片 URL ═══")
	fmt.Print("\n用户: [图片] 这张图片里有什么？\n助手: ")

	if err := agent.Send(ctx,
		agentkit.Text("这张图片里有什么？"),
		agentkit.ImageURL("https://raw.githubusercontent.com/wsshow/feikong-teams/main/docs/images/fkteams.png"),
	); err != nil {
		log.Fatalln(err)
	}

	// ── 场景 2：本地图片（Base64） ───────────────────────────────────────────
	// 当 API 无法访问外部 URL 时，可使用 Base64 编码发送本地图片。
	fmt.Println("\n═══ 场景 2：本地图片（Base64） ═══")
	fmt.Print("\n用户: [本地图片] 描述一下这张图片\n助手: ")

	imgData, err := os.ReadFile("test.png") // 替换为你的本地图片路径
	if err != nil {
		fmt.Println("（跳过：未找到 test.png，请放置一张图片到当前目录）")
	} else {
		b64 := base64.StdEncoding.EncodeToString(imgData)
		if err := agent.Send(ctx,
			agentkit.Text("描述一下这张图片"),
			agentkit.ImageBase64(b64, "image/png"),
		); err != nil {
			log.Fatalln(err)
		}
	}

	// ── 场景 3：追问（纯文本，依赖上下文） ───────────────────────────────────
	fmt.Println("\n═══ 场景 3：追问（纯文本，依赖上下文） ═══")

	if err := demo.Ask(ctx, agent, "你刚才看到的图片是关于什么主题的？"); err != nil {
		log.Fatalln(err)
	}
}
