package daemon

import (
	"errors"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClipboardUncertaintyHTTPRedaction(t *testing.T) {
	w := httptest.NewRecorder()
	writeConsoleError(w, errors.Join(domain.ErrClipboardUncertain, errors.New("synthetic-private-error")))
	message, _ := domain.CanonicalFailureMessage(domain.FailureCategoryClipboardUncertain)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), domain.FailureCategoryClipboardUncertain) || !strings.Contains(w.Body.String(), message) || strings.Contains(w.Body.String(), "synthetic-private-error") {
		t.Fatal(w.Code, w.Body.String())
	}
}
