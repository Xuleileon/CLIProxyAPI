package managementasset

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	openCodeGoPageStart = "function ej(){"
	openCodeGoPageEnd   = "function tj(){"

	openCodeGoStateAnchor = "[l,u]=(0,y.useState)({fileName:``,location:``,loading:!1}),d="
	openCodeGoStatePatch  = "[l,u]=(0,y.useState)({fileName:``,location:``,loading:!1}),[openCodeGoState,setOpenCodeGoState]=(0,y.useState)({apiKey:``,loading:!1,status:``,error:``}),d="

	openCodeGoHandlerAnchor = "},N=(n,r=!1)=>{"
	openCodeGoHandlerPatch  = "},saveOpenCodeGo=async()=>{let t=openCodeGoState.apiKey.trim();if(!t){r(e(`auth_login.opencode_go_key_required`),`warning`);return}setOpenCodeGoState(a=>({...a,loading:!0,status:``,error:``}));try{let n=await sp.get(`/opencode-go-api-key`),i=Array.isArray(n?.[`opencode-go-api-key`])?n[`opencode-go-api-key`].map(e=>{let t={...e};return delete t[`auth-index`],t}):[];i.some(e=>String(e?.[`api-key`]??``).trim()===t)||i.push({\"api-key\":t}),await sp.put(`/opencode-go-api-key`,i),setOpenCodeGoState(a=>({...a,apiKey:``,loading:!1,status:`success`,error:``})),r(e(`auth_login.opencode_go_key_saved`),`success`)}catch(t){let n=Ec(t);setOpenCodeGoState(a=>({...a,loading:!1,status:`error`,error:n||e(`auth_login.opencode_go_key_save_error`)})),r(`${e(`auth_login.opencode_go_key_save_error`)} ${n||``}`,`error`)}},N=(n,r=!1)=>{"

	openCodeGoCardAnchor = "(0,H.jsx)(jE,{title:(0,H.jsxs)(`span`,{className:FA.cardTitle,children:[(0,H.jsx)(`img`,{src:bx,alt:``,className:FA.cardTitleIcon}),e(`vertex_import.title`)]})"
	openCodeGoCard       = "(0,H.jsx)(jE,{title:(0,H.jsxs)(`span`,{className:FA.cardTitle,children:[(0,H.jsx)(`span`,{className:FA.cardTitleIconFallback,\"aria-hidden\":`true`,children:`G`}),(0,H.jsx)(`span`,{children:e(`auth_login.opencode_go_key_title`)})]}),extra:(0,H.jsx)(U,{onClick:saveOpenCodeGo,loading:openCodeGoState.loading,children:e(`auth_login.opencode_go_key_button`)}),children:(0,H.jsxs)(`div`,{className:FA.cardContent,children:[(0,H.jsx)(`div`,{className:FA.cardHint,children:e(`auth_login.opencode_go_key_hint`)}),(0,H.jsxs)(`div`,{className:FA.formItem,children:[(0,H.jsx)(`label`,{className:FA.formItemLabel,children:e(`auth_login.opencode_go_key_label`)}),(0,H.jsx)(`input`,{className:`input`,type:`password`,autoComplete:`off`,value:openCodeGoState.apiKey,onChange:e=>setOpenCodeGoState(t=>({...t,apiKey:e.target.value,status:``,error:``})),placeholder:e(`auth_login.opencode_go_key_placeholder`)}),(0,H.jsx)(`div`,{className:FA.cardHintSecondary,children:e(`auth_login.opencode_go_key_field_hint`)})]}),openCodeGoState.status===`success`&&(0,H.jsx)(`div`,{className:`status-badge success`,children:e(`auth_login.opencode_go_key_saved`)}),openCodeGoState.status===`error`&&(0,H.jsx)(`div`,{className:`status-badge error`,children:openCodeGoState.error||e(`auth_login.opencode_go_key_save_error`)})]})}),"
)

// patchManagementHTMLForOpenCodeGo adds the OpenCode Go subscription key form
// to the stock management panel's "other login methods" section.
func patchManagementHTMLForOpenCodeGo(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	s := string(data)
	original := s

	if !strings.Contains(s, "saveOpenCodeGo=async") {
		pageStart := strings.Index(s, openCodeGoPageStart)
		if pageStart == -1 {
			log.Debug("management panel OpenCode Go patch skipped: OAuth page start not found")
			return data
		}
		pageEndOffset := strings.Index(s[pageStart:], openCodeGoPageEnd)
		if pageEndOffset == -1 {
			log.Debug("management panel OpenCode Go patch skipped: OAuth page end not found")
			return data
		}
		pageEnd := pageStart + pageEndOffset
		page := s[pageStart:pageEnd]
		if strings.Count(page, openCodeGoStateAnchor) != 1 ||
			strings.Count(page, openCodeGoHandlerAnchor) != 1 ||
			strings.Count(page, openCodeGoCardAnchor) != 1 {
			log.Debug("management panel OpenCode Go patch skipped: OAuth page anchors not found")
			return data
		}

		page = strings.Replace(page, openCodeGoStateAnchor, openCodeGoStatePatch, 1)
		page = strings.Replace(page, openCodeGoHandlerAnchor, openCodeGoHandlerPatch, 1)
		page = strings.Replace(page, openCodeGoCardAnchor, openCodeGoCard+openCodeGoCardAnchor, 1)
		s = s[:pageStart] + page + s[pageEnd:]
	}

	localePatches := [][2]string{
		{
			"cursor_oauth_polling_error:`检查认证状态失败:`,plugin_oauth_title:",
			"cursor_oauth_polling_error:`检查认证状态失败:`,opencode_go_key_title:`OpenCode Go API Key`,opencode_go_key_button:`保存 OpenCode Go Key`,opencode_go_key_hint:`OpenCode Go 使用订阅 API Key，不走 OAuth。粘贴从 OpenCode Go 账户页面复制的 Key，保存后立即载入。`,opencode_go_key_label:`API Key`,opencode_go_key_field_hint:`Key 仅写入本地配置，不会显示在页面中。`,opencode_go_key_placeholder:`粘贴 OpenCode Go API Key`,opencode_go_key_required:`请先填写 OpenCode Go API Key。`,opencode_go_key_saved:`OpenCode Go API Key 已保存。`,opencode_go_key_save_error:`保存 OpenCode Go API Key 失败:`,plugin_oauth_title:",
		},
		{
			"cursor_oauth_polling_error:`檢查驗證狀態失敗:`,plugin_oauth_title:",
			"cursor_oauth_polling_error:`檢查驗證狀態失敗:`,opencode_go_key_title:`OpenCode Go API Key`,opencode_go_key_button:`儲存 OpenCode Go Key`,opencode_go_key_hint:`OpenCode Go 使用訂閱 API Key，不使用 OAuth。貼上從 OpenCode Go 帳戶頁面複製的 Key，儲存後立即載入。`,opencode_go_key_label:`API Key`,opencode_go_key_field_hint:`Key 只會寫入本機設定，不會顯示在頁面中。`,opencode_go_key_placeholder:`貼上 OpenCode Go API Key`,opencode_go_key_required:`請先填寫 OpenCode Go API Key。`,opencode_go_key_saved:`OpenCode Go API Key 已儲存。`,opencode_go_key_save_error:`儲存 OpenCode Go API Key 失敗:`,plugin_oauth_title:",
		},
		{
			"cursor_oauth_polling_error:`Failed to check authentication status:`,plugin_oauth_title:",
			"cursor_oauth_polling_error:`Failed to check authentication status:`,opencode_go_key_title:`OpenCode Go API Key`,opencode_go_key_button:`Save OpenCode Go Key`,opencode_go_key_hint:`OpenCode Go uses a subscription API key, not OAuth. Paste the key copied from your OpenCode Go account; it is loaded immediately after saving.`,opencode_go_key_label:`API Key`,opencode_go_key_field_hint:`The key is written only to the local configuration and is not displayed on this page.`,opencode_go_key_placeholder:`Paste OpenCode Go API Key`,opencode_go_key_required:`Enter an OpenCode Go API Key first.`,opencode_go_key_saved:`OpenCode Go API Key saved.`,opencode_go_key_save_error:`Failed to save OpenCode Go API Key:`,plugin_oauth_title:",
		},
		{
			"cursor_oauth_polling_error:`Не удалось проверить статус аутентификации:`,plugin_oauth_title:",
			"cursor_oauth_polling_error:`Не удалось проверить статус аутентификации:`,opencode_go_key_title:`OpenCode Go API Key`,opencode_go_key_button:`Сохранить OpenCode Go Key`,opencode_go_key_hint:`OpenCode Go использует ключ подписки API, а не OAuth. Вставьте ключ из учетной записи OpenCode Go; после сохранения он загрузится сразу.`,opencode_go_key_label:`API Key`,opencode_go_key_field_hint:`Ключ сохраняется только в локальной конфигурации и не отображается на странице.`,opencode_go_key_placeholder:`Вставьте OpenCode Go API Key`,opencode_go_key_required:`Сначала введите OpenCode Go API Key.`,opencode_go_key_saved:`OpenCode Go API Key сохранен.`,opencode_go_key_save_error:`Не удалось сохранить OpenCode Go API Key:`,plugin_oauth_title:",
		},
	}
	for _, patch := range localePatches {
		if strings.Contains(s, patch[0]) {
			s = strings.Replace(s, patch[0], patch[1], 1)
		}
	}

	if patchedProvider, ok := patchManagementHTMLForOpenCodeGoProvider(s); ok {
		s = patchedProvider
	} else {
		log.Debug("management panel OpenCode Go provider patch skipped: provider workbench anchors not found")
	}
	if patchedAccountCell, ok := patchManagementHTMLForOpenCodeGoAccountCell(s); ok {
		s = patchedAccountCell
	} else {
		log.Debug("management panel OpenCode Go account cell patch skipped: provider table anchor not found")
	}
	if patchedDashboardCount, ok := patchManagementHTMLForOpenCodeGoDashboardCount(s); ok {
		s = patchedDashboardCount
	} else {
		log.Debug("management panel OpenCode Go dashboard count patch skipped: dashboard anchor not found")
	}

	if s == original {
		return data
	}
	log.Info("management panel: injected OpenCode Go API key card")
	return []byte(s)
}

// EnsureOpenCodeGoOnDisk patches an existing management.html when the key form is missing.
func EnsureOpenCodeGoOnDisk(localPath string) {
	data, errRead := os.ReadFile(localPath)
	if errRead != nil {
		return
	}
	patched := patchManagementHTMLForOpenCodeGo(data)
	if string(patched) == string(data) {
		return
	}
	if errWrite := atomicWriteFile(localPath, patched); errWrite != nil {
		log.WithError(errWrite).Warn("failed to write management panel OpenCode Go patch")
	}
}
