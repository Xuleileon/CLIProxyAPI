package managementasset

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPatchManagementHTMLForAuthRefresh(t *testing.T) {
	source := []byte("requestManualRefresh:e=>sp.patch(`/auth-files/fields`,{name:e,expired:qv()}),await Jv.requestManualRefresh(r),t(e(`auth_files.manual_refresh_requested`,{name:r}),`info`),ck()}catch{return delete t[r],t})}}},[t,e]),z=(0,y.useCallback),manual_refresh_requested:`已提交 \"{{name}}\" 的凭证刷新请求`,")
	patched := patchManagementHTMLForAuthRefresh(source)

	if !bytes.Contains(patched, []byte("requestManualRefresh:e=>sp.post(`/auth-files/refresh`,{name:e})")) {
		t.Fatalf("refresh endpoint was not patched: %s", patched)
	}
	if !bytes.Contains(patched, []byte("manual_refresh_requested:`已刷新 \"{{name}}\" 的 OAuth 凭证`")) {
		t.Fatalf("success message was not patched: %s", patched)
	}
	if !bytes.Contains(patched, []byte("ck(),await N()}catch")) {
		t.Fatalf("visible auth list refresh was not patched: %s", patched)
	}
	if !bytes.Contains(patched, []byte("[N,t,e]),z=(0,y.useCallback)")) {
		t.Fatalf("refresh callback dependencies were not patched: %s", patched)
	}
	if second := patchManagementHTMLForAuthRefresh(patched); !bytes.Equal(second, patched) {
		t.Fatal("second patch changed content")
	}
}

func TestPatchManagementHTMLForAuthRefreshCurrentBundle(t *testing.T) {
	panelPath := filepath.Join("..", "..", "static", "management.html")
	data, errRead := os.ReadFile(panelPath)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			t.Skip("static management panel is not present in this checkout")
		}
		t.Fatalf("read current management panel: %v", errRead)
	}
	patched := patchManagementHTMLForAuthRefresh(data)
	if !bytes.Contains(patched, []byte("requestManualRefresh:e=>sp.post(`/auth-files/refresh`,{name:e})")) {
		t.Fatal("current panel refresh action was not patched")
	}
	if bytes.Contains(patched, []byte("requestManualRefresh:e=>sp.patch(`/auth-files/fields`,{name:e,expired:qv()})")) {
		t.Fatal("current panel still contains the asynchronous refresh action")
	}
	if !bytes.Contains(patched, []byte("ck(),await N()}catch")) {
		t.Fatal("current panel does not refresh its visible auth list")
	}
	if second := patchManagementHTMLForAuthRefresh(patched); !bytes.Equal(second, patched) {
		t.Fatal("second current bundle patch changed content")
	}
}
