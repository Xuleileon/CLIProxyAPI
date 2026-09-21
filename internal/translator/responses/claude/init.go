package claude

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	codex "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(constant.Claude, constant.OpenaiResponse, ConvertClaudeRequestToResponses, interfaces.TranslateResponse{
		Stream:    codex.ConvertCodexResponseToClaude,
		NonStream: ConvertResponsesToClaudeNonStream,
	})
}
