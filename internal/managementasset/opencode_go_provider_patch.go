package managementasset

import "strings"

type openCodeGoBundlePatch struct {
	old   string
	new   string
	count int
}

// patchManagementHTMLForOpenCodeGoProvider integrates OpenCode Go with the
// compiled provider workbench. It fails closed when the upstream bundle moves.
func patchManagementHTMLForOpenCodeGoProvider(input string) (string, bool) {
	if strings.Contains(input, "createOpenCodeGoConfig") {
		return input, true
	}
	patches := []openCodeGoBundlePatch{
		{
			old:   "let i={apiKey:r},a=t?.priority;",
			new:   "let i={apiKey:r},opencodeGoName=hp(t?.name);opencodeGoName&&(i.name=opencodeGoName);let opencodeGoModelCount=Number(t?.[`model-count`]);Number.isFinite(opencodeGoModelCount)&&(i.modelCount=opencodeGoModelCount);let a=t?.priority;",
			count: 2,
		},
		{
			old:   "let u=e[`codex-api-key`];Array.isArray(u)&&(t.codexApiKeys=u.map(e=>vp(e)).filter(Boolean));let d=e[`xai-api-key`];",
			new:   "let u=e[`codex-api-key`];Array.isArray(u)&&(t.codexApiKeys=u.map(e=>vp(e)).filter(Boolean));let opencodeGo=e[`opencode-go-api-key`];Array.isArray(opencodeGo)&&(t.openCodeGoKeys=opencodeGo.map(e=>vp(e)).filter(Boolean));let d=e[`xai-api-key`];",
			count: 1,
		},
		{
			old:   "case`codex-api-key`:i.codexApiKeys=r;break;case`xai-api-key`",
			new:   "case`codex-api-key`:i.codexApiKeys=r;break;case`opencode-go-api-key`:i.openCodeGoKeys=r;break;case`xai-api-key`",
			count: 1,
		},
		{
			old:   "R_=[`api-key`,`priority`,`prefix`,`base-url`,`proxy-url`,`headers`,`models`,`excluded-models`,`disable-cooling`]",
			new:   "R_=[`name`,`api-key`,`priority`,`prefix`,`base-url`,`proxy-url`,`headers`,`models`,`excluded-models`,`disable-cooling`]",
			count: 1,
		},
		{
			old:   "gv=e=>{let t={\"api-key\":e.apiKey};e.priority",
			new:   "gv=e=>{let t={\"api-key\":e.apiKey};e.name?.trim()&&(t.name=e.name.trim()),e.priority",
			count: 1,
		},
		{
			old:   "deleteCodexConfig:(e,t)=>sp.delete(`/codex-api-key${pv(e,t)}`),createXAIConfig:",
			new:   "deleteCodexConfig:(e,t)=>sp.delete(`/codex-api-key${pv(e,t)}`),async getOpenCodeGoConfigs(){return fv(await sp.get(`/opencode-go-api-key`),`opencode-go-api-key`).map(e=>vp(e)).filter(Boolean)},createOpenCodeGoConfig:e=>ov(`opencode-go-api-key`,t=>iv(t,gv(e),(e,t)=>uv(e,t,R_))),updateOpenCodeGoConfig:(e,t)=>ov(`opencode-go-api-key`,n=>av(n,(t,n)=>n===e,gv(t),(e,t)=>uv(e,t,R_))),deleteOpenCodeGoConfig:e=>sp.delete(`/opencode-go-api-key?index=${encodeURIComponent(String(e))}`),createXAIConfig:",
			count: 1,
		},
		{
			old:   "providerNames:{gemini:`Gemini`,codex:",
			new:   "providerNames:{gemini:`Gemini`,opencodeGo:`OpenCode Go`,codex:",
			count: 3,
		},
		{
			old:   "var $w={gemini:{id:`gemini`",
			new:   "var $w={opencodeGo:{id:`opencodeGo`,supportsName:!0,supportsApiKey:!0,supportsDisabled:!0,supportsBaseUrl:!0,baseUrlRequired:!1,supportsProxyUrl:!0,supportsPrefix:!0,supportsModels:!0,supportsHeaders:!0,supportsExcludedModels:!0,supportsPriority:!0,supportsTestModel:!1,supportsWebsockets:!1,supportsCloak:!1,supportsApiKeyEntries:!1,sheetSize:`md`},gemini:{id:`gemini`",
			count: 1,
		},
		{
			old:   "eT=[`kimi`,`gemini`,`codex`",
			new:   "eT=[`kimi`,`opencodeGo`,`gemini`,`codex`",
			count: 1,
		},
		{
			old:   "baseUrl:e===`claudeApi`?Ix:e===`xai`?vT:``",
			new:   "baseUrl:e===`opencodeGo`?`https://opencode.ai/zen/go/v1`:e===`claudeApi`?Ix:e===`xai`?vT:``",
			count: 1,
		},
		{
			old:   "return{apiKey:``,name:``,baseUrl:i.baseUrl??``",
			new:   "return{apiKey:``,name:e===`opencodeGo`?i.name??``:``,baseUrl:i.baseUrl??``",
			count: 1,
		},
		{
			old:   "c.supportsName&&!u.name.trim()?s(`providersPage.form.validation.nameRequired`)",
			new:   "c.supportsName&&e!==`opencodeGo`&&!u.name.trim()?s(`providersPage.form.validation.nameRequired`)",
			count: 1,
		},
		{
			old:   "o={apiKey:t.apiKey.trim().length>0?t.apiKey.trim():n?.apiKey??``,priority:",
			new:   "o={name:e===`opencodeGo`?t.name.trim()||void 0:void 0,apiKey:t.apiKey.trim().length>0?t.apiKey.trim():n?.apiKey??``,priority:",
			count: 1,
		},
		{
			old:   "function RT(e,t){return MT(`vertex`,e,t)}function zT",
			new:   "function RT(e,t){return MT(`vertex`,e,t)}function openCodeGoResource(e,t){let n=(e.name??``).trim(),r=MT(`opencodeGo`,e,t);return{...r,name:n||null,identifier:n||Mh(e.apiKey)||`OpenCode Go #${t+1}`,modelCount:e.modelCount??e.models?.length??0}}function zT",
			count: 1,
		},
		{
			old:   "let[e,t,i]=await Promise.allSettled([n(!0),xv.getVertexConfigs(),xv.getOpenAIProviders()]);if(e.status!==`fulfilled`)throw e.reason;t.status===`fulfilled`&&r(`vertex-api-key`,t.value||[]),i.status===`fulfilled`&&r(`openai-compatibility`,i.value||[]),m(new Date().toISOString())",
			new:   "let[e,t,i,opencodeGo]=await Promise.allSettled([n(!0),xv.getVertexConfigs(),xv.getOpenAIProviders(),xv.getOpenCodeGoConfigs()]);if(e.status!==`fulfilled`)throw e.reason;t.status===`fulfilled`&&r(`vertex-api-key`,t.value||[]),i.status===`fulfilled`&&r(`openai-compatibility`,i.value||[]),opencodeGo.status===`fulfilled`&&r(`opencode-go-api-key`,opencodeGo.value||[]),m(new Date().toISOString())",
			count: 1,
		},
		{
			old:   "case`vertex`:n=(t.vertexApiKeys??[]).map((e,t)=>RT(e,t));break;case`openaiCompatibility`",
			new:   "case`vertex`:n=(t.vertexApiKeys??[]).map((e,t)=>RT(e,t));break;case`opencodeGo`:n=(t.openCodeGoKeys??[]).map((e,t)=>openCodeGoResource(e,t));break;case`openaiCompatibility`",
			count: 1,
		},
		{
			old:   "e===`vertex`?await xv.createVertexConfig(ZT(`vertex`,t)):e===`openaiCompatibility`",
			new:   "e===`vertex`?await xv.createVertexConfig(ZT(`vertex`,t)):e===`opencodeGo`?await xv.createOpenCodeGoConfig(ZT(`opencodeGo`,t)):e===`openaiCompatibility`",
			count: 1,
		},
		{
			old:   "else if(n===`vertex`&&r.brand===`vertex`){let n=e.raw;await xv.updateVertexConfig(r.apiKey,r.baseUrl,ZT(`vertex`,t,n))}else n===`openaiCompatibility`",
			new:   "else if(n===`vertex`&&r.brand===`vertex`){let n=e.raw;await xv.updateVertexConfig(r.apiKey,r.baseUrl,ZT(`vertex`,t,n))}else if(n===`opencodeGo`&&r.brand===`opencodeGo`){let n=e.raw;await xv.updateOpenCodeGoConfig(r.index,ZT(`opencodeGo`,t,n))}else n===`openaiCompatibility`",
			count: 1,
		},
		{
			old:   "else if(n.brand===`vertex`){await xv.deleteVertexConfig(n.apiKey,n.baseUrl);let e=(t?.vertexApiKeys??[]).filter((e,t)=>t!==n.index);r(`vertex-api-key`,e)}else if(n.brand===`openaiCompatibility`)",
			new:   "else if(n.brand===`vertex`){await xv.deleteVertexConfig(n.apiKey,n.baseUrl);let e=(t?.vertexApiKeys??[]).filter((e,t)=>t!==n.index);r(`vertex-api-key`,e)}else if(n.brand===`opencodeGo`){await xv.deleteOpenCodeGoConfig(n.index);let e=(t?.openCodeGoKeys??[]).filter((e,t)=>t!==n.index);r(`opencode-go-api-key`,e)}else if(n.brand===`openaiCompatibility`)",
			count: 1,
		},
		{
			old:   "r.brand===`vertex`&&await xv.updateVertexConfig(r.apiKey,r.baseUrl,a)}else n===`openaiCompatibility`",
			new:   "r.brand===`vertex`&&await xv.updateVertexConfig(r.apiKey,r.baseUrl,a)}else if(n===`opencodeGo`&&r.brand===`opencodeGo`){let n=e.raw,i=t?Jb(n.excludedModels):Yb(n.excludedModels),a={...n,excludedModels:i};e.authIndex?await sp.patch(`/auth-files/status`,{name:e.authIndex,disabled:t}):await xv.updateOpenCodeGoConfig(r.index,a)}else n===`openaiCompatibility`",
			count: 1,
		},
		{
			old:   "await sp.put(`/opencode-go-api-key`,i),setOpenCodeGoState",
			new:   "await sp.put(`/opencode-go-api-key`,i),Op.getState().clearCache(),setOpenCodeGoState",
			count: 1,
		},
	}

	result := input
	for _, patch := range patches {
		if strings.Count(result, patch.old) != patch.count {
			return input, false
		}
		result = strings.ReplaceAll(result, patch.old, patch.new)
	}
	return result, true
}

// patchManagementHTMLForOpenCodeGoAccountCell keeps the account label visible
// in the provider table while retaining the masked key and runtime auth index.
// It is separate from the main provider patch so already-patched assets can be
// upgraded without reapplying the complete bundle transformation.
func patchManagementHTMLForOpenCodeGoAccountCell(input string) (string, bool) {
	if strings.Contains(input, "data-opencode-go-account") {
		return input, true
	}
	old := "}return(0,H.jsxs)(`div`,{className:cw.primaryCell,children:[(0,H.jsx)(`span`,{className:cw.primaryName,children:e.apiKeyPreview??`—`}),e.authIndex?(0,H.jsxs)(`span`,{className:cw.primarySub,children:[`auth: `,e.authIndex]}):null]})},h=e=>"
	newValue := "}if(e.brand===`opencodeGo`)return(0,H.jsxs)(`div`,{className:cw.primaryCell,\"data-opencode-go-account\":!0,children:[(0,H.jsx)(`span`,{className:cw.primaryName,children:e.name??e.identifier}),(0,H.jsxs)(`span`,{className:cw.primarySub,children:[e.apiKeyPreview??`—`,e.authIndex?` · auth: ${e.authIndex}`:``]})]});return(0,H.jsxs)(`div`,{className:cw.primaryCell,children:[(0,H.jsx)(`span`,{className:cw.primaryName,children:e.apiKeyPreview??`—`}),e.authIndex?(0,H.jsxs)(`span`,{className:cw.primarySub,children:[`auth: `,e.authIndex]}):null]})},h=e=>"
	if strings.Count(input, old) != 1 {
		return input, false
	}
	return strings.Replace(input, old, newValue, 1), true
}

// patchManagementHTMLForOpenCodeGoDashboardCount includes OpenCode Go entries
// in the dashboard's provider-key total.
func patchManagementHTMLForOpenCodeGoDashboardCount(input string) (string, bool) {
	if strings.Contains(input, "opencodeGo:n.openCodeGoKeys?.length??0") {
		return input, true
	}
	old := "n?{gemini:n.geminiApiKeys?.length??0,codex:n.codexApiKeys?.length??0,xai:"
	newValue := "n?{gemini:n.geminiApiKeys?.length??0,codex:n.codexApiKeys?.length??0,opencodeGo:n.openCodeGoKeys?.length??0,xai:"
	if strings.Count(input, old) != 1 {
		return input, false
	}
	return strings.Replace(input, old, newValue, 1), true
}
