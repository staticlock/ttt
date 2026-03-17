package main

import (
	"context"
	"fmt"
	"github.com/cloudwego/eino-ext/components/model/ark" // 假设使用 Ark 节点
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// BashInput 定义工具输入参数
type BashInput struct {
	Command string `json:"command" jsonschema:"required" jsonschema_description:"Run a shell command."`
}

// NewBashTool 创建 Bash 工具
func NewBashTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		"bash",
		"Run a shell command. Use this to create files or run scripts.",
		func(ctx context.Context, input BashInput) (string, error) {
			// 简单的危险命令过滤
			dangerous := []string{"rm -rf", "sudo", "shutdown"}
			command := strings.ToLower(input.Command)
			for _, d := range dangerous {
				if strings.Contains(command, d) {
					return "Error: Dangerous command blocked", nil
				}
			}
			var cmd *exec.Cmd
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(ctx, "cmd", "/C", input.Command)
			} else {
				cmd = exec.CommandContext(ctx, "sh", "-c", input.Command)
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Sprintf("Error: %v, Output: %s", err, string(out)), nil
			}
			return string(out), nil
		},
	)
}
func BuildAgent(ctx context.Context, chatModel model.ToolCallingChatModel, bashTool tool.InvokableTool) (compose.Runnable[[]*schema.Message, []*schema.Message], error) {
	// 1. 创建画板 (Graph)
	// 输入是消息列表，输出也是消息列表
	g := compose.NewGraph[[]*schema.Message, []*schema.Message]()

	// 2. 添加模型节点
	// 绑定工具，这样模型知道自己可以调用 bash
	bashToolInfo, err := bashTool.Info(ctx)
	if err != nil {
		return nil, err
	}

	runnableModel, err := chatModel.WithTools([]*schema.ToolInfo{bashToolInfo})
	if err != nil {
		return nil, err
	}
	if err := g.AddChatModelNode("llm", runnableModel); err != nil {
		return nil, err
	}

	// 3. 添加工具执行节点
	// Eino 提供了内置的工具执行器
	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: []tool.BaseTool{bashTool},
	})
	if err != nil {
		return nil, err
	}
	if err := g.AddToolsNode("tools", toolsNode); err != nil {
		return nil, err
	}

	// 4. 设置边 (Edges) 和 路由逻辑
	if err := g.AddEdge(compose.START, "llm"); err != nil { // 开始 -> 模型
		return nil, err
	}

	// 关键：根据模型输出决定是去执行工具，还是直接结束
	branch := compose.NewGraphBranch(func(ctx context.Context, msgs []*schema.Message) (string, error) {
		if len(msgs) == 0 {
			return compose.END, nil
		}

		lastMsg := msgs[len(msgs)-1]
		// 如果模型生成了 ToolCalls，就走工具节点
		if len(lastMsg.ToolCalls) > 0 {
			return "tools", nil
		}
		// 否则，任务完成，走结束节点
		return compose.END, nil
	}, map[string]bool{"tools": true, compose.END: true})
	if err := g.AddBranch("llm", branch); err != nil {
		return nil, err
	}
	// 关键：工具执行完后，必须回到模型节点，实现循环
	if err := g.AddEdge("tools", "llm"); err != nil {
		return nil, err
	}
	// 5. 编译并运行
	return g.Compile(ctx)
}

func main() {
	ctx := context.Background()

	// --- 这里初始化你的模型 (如 OpenAI 或 Ark) ---
	// var chatModel model.ChatModel = ...
	// 1. 初始化 ChatModel (这里以 Ark 为例，你需要根据你的 base_url 配置)
	apiKey := strings.TrimSpace(os.Getenv("ARK_API_KEY"))
	if apiKey == "" {
		apiKey = "ak_22b0vG31R1BJ1VD4Ej2YU2Vq3Im6R"
	}

	baseURL := strings.TrimSpace(os.Getenv("ARK_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.longcat.chat/anthropic/"
	}

	modelName := strings.TrimSpace(os.Getenv("ARK_MODEL"))
	if modelName == "" {
		modelName = "LongCat-Flash-Chat"
	}

	chatModel, err := ark.NewChatModel(ctx, &ark.ChatModelConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   modelName,
	})
	if err != nil {
		fmt.Printf("init chat model failed: %v\n", err)
		return
	}
	// 2. 初始化工具
	bashTool, err := NewBashTool()
	if err != nil {
		fmt.Printf("init bash tool failed: %v\n", err)
		return
	}
	agent, err := BuildAgent(ctx, chatModel, bashTool)
	if err != nil {
		fmt.Printf("build agent failed: %v\n", err)
		return
	}

	// 运行任务
	input := []*schema.Message{
		schema.SystemMessage("You are a coding agent. Use bash."),
		schema.UserMessage("Create a file 'test.py', then run it and tell me the output."),
	}

	output, err := agent.Invoke(ctx, input)
	if err != nil {
		fmt.Printf("invoke agent failed: %v\n", err)
		return
	}
	if len(output) == 0 {
		fmt.Println("agent returned no messages")
		return
	}

	// 最后一条消息就是 LLM 在执行完所有步骤后的总结
	fmt.Println(output[len(output)-1].Content)
}
