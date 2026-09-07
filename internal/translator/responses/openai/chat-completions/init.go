package chat_completions

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	codex "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(constant.OpenAI, constant.OpenaiResponse, ConvertOpenAIRequestToResponses, interfaces.TranslateResponse{
		Stream:    codex.ConvertCodexResponseToOpenAI,
		NonStream: ConvertResponsesToOpenAINonStream,
	})
}
