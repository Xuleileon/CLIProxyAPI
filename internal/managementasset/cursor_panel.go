package managementasset

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

const cursorProviderCard = ",{kind:`builtin`,id:`cursor`,titleKey:`auth_login.cursor_oauth_title`,icon:{light:Ex,dark:Dx}}"

const xaiProviderCardPrefix = "{kind:`builtin`,id:`xai`,titleKey:`auth_login.xai_oauth_title`,icon:{light:"

func injectCursorProviderCard(s string) (string, bool) {
	start := strings.Index(s, xaiProviderCardPrefix)
	if start < 0 {
		return s, false
	}

	cardEnd := strings.Index(s[start:], "}}")
	if cardEnd < 0 {
		return s, false
	}
	cardEnd += start + 2
	xaiCard := s[start:cardEnd]
	cursorCard := strings.Replace(xaiCard, "id:`xai`", "id:`cursor`", 1)
	cursorCard = strings.Replace(cursorCard, "auth_login.xai_oauth_title", "auth_login.cursor_oauth_title", 1)
	return s[:cardEnd] + "," + cursorCard + s[cardEnd:], true
}

// patchManagementHTMLForCursorOAuth injects the Cursor OAuth login card into the
// management control panel when the upstream panel build omits it.
// Backend already exposes GET /v0/management/cursor-auth-url; the stock panel
// only lists kimi/codex/anthropic/antigravity/qoder/xai.
func patchManagementHTMLForCursorOAuth(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	s := string(data)
	if strings.Contains(s, "id:`cursor`") || strings.Contains(s, `id:"cursor"`) {
		return data
	}

	patched := false

	// Provider card list (LA) — management center v1.20+
	oldLA := "{kind:`builtin`,id:`xai`,titleKey:`auth_login.xai_oauth_title`,icon:{light:Ex,dark:Dx}}],RA=new Set(LA.map(e=>e.id))"
	newLA := "{kind:`builtin`,id:`xai`,titleKey:`auth_login.xai_oauth_title`,icon:{light:Ex,dark:Dx}}" + cursorProviderCard + "],RA=new Set(LA.map(e=>e.id))"
	if strings.Contains(s, oldLA) {
		s = strings.Replace(s, oldLA, newLA, 1)
		patched = true
	}

	// Provider card list (Vk) — legacy bundled panel builds
	oldVk := "{kind:`builtin`,id:`xai`,titleKey:`auth_login.xai_oauth_title`,icon:{light:$b,dark:ex}}"
	newVk := oldVk + ",{kind:`builtin`,id:`cursor`,titleKey:`auth_login.cursor_oauth_title`,icon:{light:$b,dark:ex}}"
	if strings.Contains(s, oldVk) {
		s = strings.Replace(s, oldVk, newVk, 1)
		patched = true
	}
	if !strings.Contains(s, "id:`cursor`") {
		var injected bool
		s, injected = injectCursorProviderCard(s)
		patched = patched || injected
	}

	// WebUI OAuth provider set used by startAuth() — new (Yv) and legacy (bv)
	oldYv := "Yv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`])"
	newYv := "Yv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`,`cursor`])"
	if strings.Contains(s, oldYv) {
		s = strings.Replace(s, oldYv, newYv, 1)
		patched = true
	}
	oldBv := "bv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`])"
	newBv := "bv=new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`,`cursor`])"
	if strings.Contains(s, oldBv) {
		s = strings.Replace(s, oldBv, newBv, 1)
		patched = true
	}

	// Callback-style OAuth provider set — new (zA) and legacy (Uk)
	oldZA := "zA=new Set([`codex`,`anthropic`,`antigravity`,`xai`])"
	newZA := "zA=new Set([`codex`,`anthropic`,`antigravity`,`xai`,`cursor`])"
	if strings.Contains(s, oldZA) {
		s = strings.Replace(s, oldZA, newZA, 1)
		patched = true
	}
	oldUk := "Uk=new Set([`codex`,`anthropic`,`antigravity`,`xai`])"
	newUk := "Uk=new Set([`codex`,`anthropic`,`antigravity`,`xai`,`cursor`])"
	if strings.Contains(s, oldUk) {
		s = strings.Replace(s, oldUk, newUk, 1)
		patched = true
	}

	// Minified variable names change between management center releases. Patch
	// the provider set values as a fallback instead of relying on those names.
	providerSetPatches := [][2]string{
		{
			"new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`])",
			"new Set([`codex`,`anthropic`,`antigravity`,`qoder`,`xai`,`cursor`])",
		},
		{
			"new Set([`codex`,`anthropic`,`antigravity`,`xai`])",
			"new Set([`codex`,`anthropic`,`antigravity`,`xai`,`cursor`])",
		},
	}
	for _, providerSetPatch := range providerSetPatches {
		if strings.Contains(s, providerSetPatch[0]) {
			s = strings.ReplaceAll(s, providerSetPatch[0], providerSetPatch[1])
			patched = true
		}
	}

	// i18n strings (zh-CN / zh-TW / en / ru) — insert before plugin_oauth_title
	localePatches := [][2]string{
		{
			"xai_oauth_polling_error:`检查认证状态失败:`,plugin_oauth_title:",
			"xai_oauth_polling_error:`检查认证状态失败:`,cursor_oauth_title:`Cursor OAuth`,cursor_oauth_button:`开始 Cursor 登录`,cursor_oauth_hint:`通过 OAuth 流程登录 Cursor 服务，自动获取并保存认证文件。登录后以 IDE 模式请求上游。`,cursor_oauth_url_label:`授权链接:`,cursor_open_link:`打开链接`,cursor_copy_link:`复制链接`,cursor_oauth_status_waiting:`等待认证中...`,cursor_oauth_status_success:`认证成功！`,cursor_oauth_status_error:`认证失败:`,cursor_oauth_start_error:`启动 Cursor OAuth 失败:`,cursor_oauth_polling_error:`检查认证状态失败:`,plugin_oauth_title:",
		},
		{
			"xai_oauth_polling_error:`檢查驗證狀態失敗:`,plugin_oauth_title:",
			"xai_oauth_polling_error:`檢查驗證狀態失敗:`,cursor_oauth_title:`Cursor OAuth`,cursor_oauth_button:`開始 Cursor 登入`,cursor_oauth_hint:`透過 OAuth 流程登入 Cursor 服務，自動取得並儲存驗證檔案。登入後以 IDE 模式請求上游。`,cursor_oauth_url_label:`授權連結:`,cursor_open_link:`開啟連結`,cursor_copy_link:`複製連結`,cursor_oauth_status_waiting:`等待驗證中...`,cursor_oauth_status_success:`驗證成功！`,cursor_oauth_status_error:`驗證失敗:`,cursor_oauth_start_error:`啟動 Cursor OAuth 失敗:`,cursor_oauth_polling_error:`檢查驗證狀態失敗:`,plugin_oauth_title:",
		},
		{
			"xai_oauth_polling_error:`Failed to check authentication status:`,plugin_oauth_title:",
			"xai_oauth_polling_error:`Failed to check authentication status:`,cursor_oauth_title:`Cursor OAuth`,cursor_oauth_button:`Start Cursor Login`,cursor_oauth_hint:`Login to Cursor through OAuth flow, automatically obtain and save authentication files. Requests use IDE client mode.`,cursor_oauth_url_label:`Authorization URL:`,cursor_open_link:`Open Link`,cursor_copy_link:`Copy Link`,cursor_oauth_status_waiting:`Waiting for authentication...`,cursor_oauth_status_success:`Authentication successful!`,cursor_oauth_status_error:`Authentication failed:`,cursor_oauth_start_error:`Failed to start Cursor OAuth:`,cursor_oauth_polling_error:`Failed to check authentication status:`,plugin_oauth_title:",
		},
		{
			"xai_oauth_polling_error:`Не удалось проверить статус аутентификации:`,plugin_oauth_title:",
			"xai_oauth_polling_error:`Не удалось проверить статус аутентификации:`,cursor_oauth_title:`Cursor OAuth`,cursor_oauth_button:`Начать вход Cursor`,cursor_oauth_hint:`Выполните вход в Cursor через OAuth и автоматически получите/сохраните файлы авторизации. Запросы идут в режиме IDE.`,cursor_oauth_url_label:`URL авторизации:`,cursor_open_link:`Открыть ссылку`,cursor_copy_link:`Скопировать ссылку`,cursor_oauth_status_waiting:`Ожидание аутентификации...`,cursor_oauth_status_success:`Аутентификация успешна!`,cursor_oauth_status_error:`Ошибка аутентификации:`,cursor_oauth_start_error:`Не удалось запустить Cursor OAuth:`,cursor_oauth_polling_error:`Не удалось проверить статус аутентификации:`,plugin_oauth_title:",
		},
	}
	for _, p := range localePatches {
		if strings.Contains(s, p[0]) {
			s = strings.Replace(s, p[0], p[1], 1)
			patched = true
		}
	}

	if !strings.Contains(s, "id:`cursor`") {
		if patched {
			log.Debug("management panel cursor OAuth patch did not apply cleanly")
		} else {
			log.Debug("management panel cursor OAuth patch skipped: provider anchors not found")
		}
		return data
	}
	log.Info("management panel: injected Cursor OAuth login card")
	return []byte(s)
}

// EnsureCursorOAuthOnDisk patches an existing management.html if Cursor is missing.
// Exported so the HTTP server can apply the patch on every panel request.
func EnsureCursorOAuthOnDisk(localPath string) {
	ensureCursorOAuthOnDisk(localPath)
}

// ensureCursorOAuthOnDisk patches an existing management.html if Cursor is missing.
func ensureCursorOAuthOnDisk(localPath string) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return
	}
	patched := patchManagementHTMLForCursorOAuth(data)
	if string(patched) == string(data) {
		return
	}
	if err = atomicWriteFile(localPath, patched); err != nil {
		log.WithError(err).Warn("failed to write management panel cursor OAuth patch")
	}
}
