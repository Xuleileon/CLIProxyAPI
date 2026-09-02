package managementasset

import (
	"bytes"
	"os"

	log "github.com/sirupsen/logrus"
)

var authRefreshPanelReplacements = []struct {
	old []byte
	new []byte
}{
	{
		old: []byte("requestManualRefresh:e=>sp.patch(`/auth-files/fields`,{name:e,expired:qv()})"),
		new: []byte("requestManualRefresh:e=>sp.post(`/auth-files/refresh`,{name:e})"),
	},
	{
		old: []byte("await Jv.requestManualRefresh(r),t(e(`auth_files.manual_refresh_requested`,{name:r}),`info`),ck()}catch"),
		new: []byte("await Jv.requestManualRefresh(r),t(e(`auth_files.manual_refresh_requested`,{name:r}),`info`),ck(),await N()}catch"),
	},
	{
		old: []byte("return delete t[r],t})}}},[t,e]),z=(0,y.useCallback)"),
		new: []byte("return delete t[r],t})}}},[N,t,e]),z=(0,y.useCallback)"),
	},
	{
		old: []byte("manual_refresh_requested:`已提交 \"{{name}}\" 的凭证刷新请求`"),
		new: []byte("manual_refresh_requested:`已刷新 \"{{name}}\" 的 OAuth 凭证`"),
	},
	{
		old: []byte("manual_refresh_requested:`已提交「{{name}}」的憑證重新整理請求`"),
		new: []byte("manual_refresh_requested:`已重新整理「{{name}}」的 OAuth 憑證`"),
	},
	{
		old: []byte("manual_refresh_requested:`Credential refresh requested for \"{{name}}\"`"),
		new: []byte("manual_refresh_requested:`OAuth credential refreshed for \"{{name}}\"`"),
	},
}

func patchManagementHTMLForAuthRefresh(data []byte) []byte {
	patched := data
	for _, replacement := range authRefreshPanelReplacements {
		patched = bytes.ReplaceAll(patched, replacement.old, replacement.new)
	}
	return patched
}

// EnsureAuthRefreshOnDisk upgrades the panel's asynchronous expiry-field hack
// to the synchronous credential refresh endpoint and refreshes the visible list.
func EnsureAuthRefreshOnDisk(localPath string) {
	data, errRead := os.ReadFile(localPath)
	if errRead != nil {
		return
	}
	patched := patchManagementHTMLForAuthRefresh(data)
	if bytes.Equal(data, patched) {
		return
	}
	if errWrite := atomicWriteFile(localPath, patched); errWrite != nil {
		log.WithError(errWrite).Warn("failed to patch management panel auth refresh action")
	}
}
