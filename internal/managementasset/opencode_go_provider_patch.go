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
	legacyPatches := []openCodeGoBundlePatch{
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

	if result, ok := applyOpenCodeGoBundlePatches(input, legacyPatches); ok {
		return result, true
	}
	return applyOpenCodeGoBundlePatches(input, currentOpenCodeGoBundlePatches())
}

func applyOpenCodeGoBundlePatches(input string, patches []openCodeGoBundlePatch) (string, bool) {
	result := input
	for _, patch := range patches {
		if strings.Count(result, patch.old) != patch.count {
			return input, false
		}
		result = strings.ReplaceAll(result, patch.old, patch.new)
	}
	return result, true
}

// currentOpenCodeGoBundlePatches supports management center v1.22.2-1. The
// bundle's minified identifiers differ from legacy releases, so this patch set
// remains separate and fails closed if a future release changes its anchors.
func currentOpenCodeGoBundlePatches() []openCodeGoBundlePatch {
	return []openCodeGoBundlePatch{
		{
			old:   "let d=e[`codex-api-key`];Array.isArray(d)&&(t.codexApiKeys=d.map(e=>Bp(e)).filter(Boolean));let f=e[`xai-api-key`];",
			new:   "let d=e[`codex-api-key`];Array.isArray(d)&&(t.codexApiKeys=d.map(e=>Bp(e)).filter(Boolean));let openCodeGo=e[`opencode-go-api-key`];Array.isArray(openCodeGo)&&(t.openCodeGoKeys=openCodeGo.map(e=>openCodeGoParse(e)).filter(Boolean));let f=e[`xai-api-key`];",
			count: 1,
		},
		{
			old:   "case`codex-api-key`:i.codexApiKeys=r;break;case`xai-api-key`",
			new:   "case`codex-api-key`:i.codexApiKeys=r;break;case`opencode-go-api-key`:i.openCodeGoKeys=r;break;case`xai-api-key`",
			count: 1,
		},
		{
			old:   "},ry={",
			new:   "},openCodeGoFields=[`name`,`api-key`,`priority`,`prefix`,`base-url`,`proxy-url`,`headers`,`models`,`excluded-models`,`disable-cooling`],openCodeGoPayload=e=>{let t=Qv({...e,weight:void 0,websockets:void 0}),n=String(e?.name??``).trim();return delete t.weight,delete t.websockets,n&&(t.name=n),t},openCodeGoParse=e=>{let t=Bp(e);if(!t)return null;let n=String(e?.name??``).trim();n&&(t.name=n);let r=Number(e?.[`model-count`]);return Number.isFinite(r)&&(t.modelCount=r),t},ry={",
			count: 1,
		},
		{
			old:   "deleteCodexConfig:(e,t)=>Tp.delete(`/codex-api-key${Yv(e,t)}`),createXAIConfig:",
			new:   "deleteCodexConfig:(e,t)=>Tp.delete(`/codex-api-key${Yv(e,t)}`),async getOpenCodeGoConfigs(){return Jv(await Tp.get(`/opencode-go-api-key`),`opencode-go-api-key`).map(e=>openCodeGoParse(e)).filter(Boolean)},createOpenCodeGoConfig:e=>Hv(`opencode-go-api-key`,t=>Bv(t,openCodeGoPayload(e),(e,t)=>Kv(e,t,openCodeGoFields))),updateOpenCodeGoConfig:(e,t)=>Hv(`opencode-go-api-key`,n=>Vv(n,(t,n)=>n===e,openCodeGoPayload(t),(e,t)=>Kv(e,t,openCodeGoFields))),deleteOpenCodeGoConfig:e=>Tp.delete(`/opencode-go-api-key?index=${encodeURIComponent(String(e))}`),createXAIConfig:",
			count: 1,
		},
		{
			old:   "providerNames:{gemini:`Gemini`,",
			new:   "providerNames:{gemini:`Gemini`,opencodeGo:`OpenCode Go`,",
			count: 3,
		},
		{
			old:   "var AE={gemini:{id:`gemini`",
			new:   "var AE={opencodeGo:{id:`opencodeGo`,supportsName:!0,supportsApiKey:!0,supportsDisabled:!0,supportsBaseUrl:!0,baseUrlRequired:!1,supportsProxyUrl:!0,supportsPrefix:!0,supportsModels:!0,supportsHeaders:!0,supportsExcludedModels:!0,supportsPriority:!0,supportsTestModel:!1,supportsWebsockets:!1,supportsCloak:!1,supportsApiKeyEntries:!1,sheetSize:`md`},gemini:{id:`gemini`",
			count: 1,
		},
		{
			old:   "jE=[`kimi`,`gemini`",
			new:   "jE=[`kimi`,`opencodeGo`,`gemini`",
			count: 1,
		},
		{
			old:   "baseUrl:e===`claudeApi`?wS:e===`xai`?gD:``",
			new:   "baseUrl:e===`opencodeGo`?`https://opencode.ai/zen/go/v1`:e===`claudeApi`?wS:e===`xai`?gD:``",
			count: 1,
		},
		{
			old:   "return{apiKey:``,name:``,baseUrl:i.baseUrl??``",
			new:   "return{apiKey:``,name:e===`opencodeGo`?i.name??``:``,baseUrl:i.baseUrl??``",
			count: 1,
		},
		{
			old:   "if(c.supportsName&&!u.name.trim())return s(`providersPage.form.validation.nameRequired`);",
			new:   "if(c.supportsName&&e!==`opencodeGo`&&!u.name.trim())return s(`providersPage.form.validation.nameRequired`);",
			count: 1,
		},
		{
			old:   "o={apiKey:t.apiKey.trim().length>0?t.apiKey.trim():n?.apiKey??``,priority:",
			new:   "o={name:e===`opencodeGo`?t.name.trim()||void 0:void 0,apiKey:t.apiKey.trim().length>0?t.apiKey.trim():n?.apiKey??``,priority:",
			count: 1,
		},
		{
			old:   "function LD(e,t){return AD(`vertex`,e,t)}function RD",
			new:   "function LD(e,t){return AD(`vertex`,e,t)}function openCodeGoResource(e,t){let n=(e.name??``).trim(),r=AD(`opencodeGo`,e,t);return{...r,name:n||null,identifier:n||Xh(e.apiKey)||`OpenCode Go #${t+1}`,modelCount:e.modelCount??e.models?.length??0}}function RD",
			count: 1,
		},
		{
			old:   "let[e,t,i]=await Promise.allSettled([n(!0),ry.getVertexConfigs(),ry.getOpenAIProviders()]);if(e.status!==`fulfilled`)throw e.reason;t.status===`fulfilled`&&r(`vertex-api-key`,t.value||[]),i.status===`fulfilled`&&r(`openai-compatibility`,i.value||[]),m(new Date().toISOString())",
			new:   "let[e,t,i,openCodeGo]=await Promise.allSettled([n(!0),ry.getVertexConfigs(),ry.getOpenAIProviders(),ry.getOpenCodeGoConfigs()]);if(e.status!==`fulfilled`)throw e.reason;t.status===`fulfilled`&&r(`vertex-api-key`,t.value||[]),i.status===`fulfilled`&&r(`openai-compatibility`,i.value||[]),openCodeGo.status===`fulfilled`&&r(`opencode-go-api-key`,openCodeGo.value||[]),m(new Date().toISOString())",
			count: 1,
		},
		{
			old:   "case`vertex`:i=(t.vertexApiKeys??[]).map((e,t)=>LD(e,t));break;case`openaiCompatibility`",
			new:   "case`vertex`:i=(t.vertexApiKeys??[]).map((e,t)=>LD(e,t));break;case`opencodeGo`:i=(t.openCodeGoKeys??[]).map((e,t)=>openCodeGoResource(e,t));break;case`openaiCompatibility`",
			count: 1,
		},
		{
			old:   "e===`vertex`?await ry.createVertexConfig(QD(`vertex`,t)):e===`openaiCompatibility`",
			new:   "e===`vertex`?await ry.createVertexConfig(QD(`vertex`,t)):e===`opencodeGo`?await ry.createOpenCodeGoConfig(QD(`opencodeGo`,t)):e===`openaiCompatibility`",
			count: 1,
		},
		{
			old:   "else if(n===`vertex`&&r.brand===`vertex`){let n=e.raw;await ry.updateVertexConfig(r.apiKey,r.baseUrl,QD(`vertex`,t,n))}else n===`openaiCompatibility`",
			new:   "else if(n===`vertex`&&r.brand===`vertex`){let n=e.raw;await ry.updateVertexConfig(r.apiKey,r.baseUrl,QD(`vertex`,t,n))}else if(n===`opencodeGo`&&r.brand===`opencodeGo`){let n=e.raw;await ry.updateOpenCodeGoConfig(r.index,QD(`opencodeGo`,t,n))}else n===`openaiCompatibility`",
			count: 1,
		},
		{
			old:   "else if(n.brand===`vertex`){await ry.deleteVertexConfig(n.apiKey,n.baseUrl);let e=(t?.vertexApiKeys??[]).filter((e,t)=>t!==n.index);r(`vertex-api-key`,e)}else if(n.brand===`openaiCompatibility`)",
			new:   "else if(n.brand===`vertex`){await ry.deleteVertexConfig(n.apiKey,n.baseUrl);let e=(t?.vertexApiKeys??[]).filter((e,t)=>t!==n.index);r(`vertex-api-key`,e)}else if(n.brand===`opencodeGo`){await ry.deleteOpenCodeGoConfig(n.index);let e=(t?.openCodeGoKeys??[]).filter((e,t)=>t!==n.index);r(`opencode-go-api-key`,e)}else if(n.brand===`openaiCompatibility`)",
			count: 1,
		},
		{
			old:   "r.brand===`vertex`&&await ry.updateVertexConfig(r.apiKey,r.baseUrl,a)}else n===`openaiCompatibility`",
			new:   "r.brand===`vertex`&&await ry.updateVertexConfig(r.apiKey,r.baseUrl,a)}else if(n===`opencodeGo`&&r.brand===`opencodeGo`){let n=e.raw,i=t?Ax(n.excludedModels):jx(n.excludedModels),a={...n,excludedModels:i};e.authIndex?await Tp.patch(`/auth-files/status`,{name:e.authIndex,disabled:t}):await ry.updateOpenCodeGoConfig(r.index,a)}else n===`openaiCompatibility`",
			count: 1,
		},
	}
}

// patchManagementHTMLForOpenCodeGoAccountCell keeps the account label visible
// in the provider table while retaining the masked key and runtime auth index.
// It is separate from the main provider patch so already-patched assets can be
// upgraded without reapplying the complete bundle transformation.
func patchManagementHTMLForOpenCodeGoAccountCell(input string) (string, bool) {
	if strings.Contains(input, "data-opencode-go-account") {
		return input, true
	}
	patches := [][2]string{
		{
			"}return(0,H.jsxs)(`div`,{className:cw.primaryCell,children:[(0,H.jsx)(`span`,{className:cw.primaryName,children:e.apiKeyPreview??`—`}),e.authIndex?(0,H.jsxs)(`span`,{className:cw.primarySub,children:[`auth: `,e.authIndex]}):null]})},h=e=>",
			"}if(e.brand===`opencodeGo`)return(0,H.jsxs)(`div`,{className:cw.primaryCell,\"data-opencode-go-account\":!0,children:[(0,H.jsx)(`span`,{className:cw.primaryName,children:e.name??e.identifier}),(0,H.jsxs)(`span`,{className:cw.primarySub,children:[e.apiKeyPreview??`—`,e.authIndex?` · auth: ${e.authIndex}`:``]})]});return(0,H.jsxs)(`div`,{className:cw.primaryCell,children:[(0,H.jsx)(`span`,{className:cw.primaryName,children:e.apiKeyPreview??`—`}),e.authIndex?(0,H.jsxs)(`span`,{className:cw.primarySub,children:[`auth: `,e.authIndex]}):null]})},h=e=>",
		},
		{
			"}return(0,H.jsxs)(`div`,{className:MT.primaryCell,children:[(0,H.jsx)(`span`,{className:MT.primaryName,children:e.apiKeyPreview??`—`}),e.authIndex?(0,H.jsxs)(`span`,{className:MT.primarySub,children:[`auth: `,e.authIndex]}):null]})},h=e=>",
			"}if(e.brand===`opencodeGo`)return(0,H.jsxs)(`div`,{className:MT.primaryCell,\"data-opencode-go-account\":!0,children:[(0,H.jsx)(`span`,{className:MT.primaryName,children:e.name??e.identifier}),(0,H.jsxs)(`span`,{className:MT.primarySub,children:[e.apiKeyPreview??`—`,e.authIndex?` · auth: ${e.authIndex}`:``]})]});return(0,H.jsxs)(`div`,{className:MT.primaryCell,children:[(0,H.jsx)(`span`,{className:MT.primaryName,children:e.apiKeyPreview??`—`}),e.authIndex?(0,H.jsxs)(`span`,{className:MT.primarySub,children:[`auth: `,e.authIndex]}):null]})},h=e=>",
		},
	}
	for _, patch := range patches {
		if strings.Count(input, patch[0]) == 1 {
			return strings.Replace(input, patch[0], patch[1], 1), true
		}
	}
	return input, false
}

// patchManagementHTMLForOpenCodeGoDashboardCount includes OpenCode Go entries
// in the dashboard's provider-key total.
func patchManagementHTMLForOpenCodeGoDashboardCount(input string) (string, bool) {
	if strings.Contains(input, "openCodeGoKeys?.length??0") {
		return input, true
	}
	patches := [][2]string{
		{
			"n?{gemini:n.geminiApiKeys?.length??0,codex:n.codexApiKeys?.length??0,xai:",
			"n?{gemini:n.geminiApiKeys?.length??0,codex:n.codexApiKeys?.length??0,opencodeGo:n.openCodeGoKeys?.length??0,xai:",
		},
		{
			"Pb=e=>({gemini:e.geminiApiKeys?.length??0,interactions:e.interactionsApiKeys?.length??0,codex:e.codexApiKeys?.length??0,xai:",
			"Pb=e=>({gemini:e.geminiApiKeys?.length??0,interactions:e.interactionsApiKeys?.length??0,codex:e.codexApiKeys?.length??0,opencodeGo:e.openCodeGoKeys?.length??0,xai:",
		},
	}
	for _, patch := range patches {
		if strings.Count(input, patch[0]) == 1 {
			return strings.Replace(input, patch[0], patch[1], 1), true
		}
	}
	return input, false
}
