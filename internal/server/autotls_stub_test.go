//go:build notunnel

package server

import (
	"net/http"
	"testing"
)

// notunnel 构建下 tryAutoTLS 永远回退 plain HTTP.
func TestTryAutoTLSStubFallsBack(t *testing.T) {
	tlsSrv, httpSrv, certExpiryFn, ok := tryAutoTLS(http.NewServeMux(), configForTest("example.com"))
	if ok || tlsSrv != nil || httpSrv != nil || certExpiryFn != nil {
		t.Errorf("stub should fall back, got ok=%v srv=%v http=%v fn=%v", ok, tlsSrv, httpSrv, certExpiryFn != nil)
	}
}
