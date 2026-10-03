package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestDesktopInvalidReceiptFailsClosedWithCanonicalError(t *testing.T) {
	for _, cached := range []bool{false, true} {
		w := httptest.NewRecorder()
		writeDesktopError(w, domain.ErrClipboardUncertain, app.DesktopActionResult{Receipt: &domain.Receipt{ReceiptID: "synthetic-private-invalid"}, CachedReceipt: cached, Response: domain.DesktopResponse{Text: "synthetic-private-response"}})
		var env DesktopErrorEnvelope
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusConflict || env.Error.Category != domain.FailureCategoryClipboardUncertain || !env.ReceiptInvalid || env.Receipt != nil || env.CachedReceipt || strings.Contains(w.Body.String(), "synthetic-private") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	writeDesktopError(w, errors.New("synthetic-private-error"), app.DesktopActionResult{})
	var env DesktopErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.ReceiptInvalid || env.Receipt != nil {
		t.Fatal(w.Body.String(), err)
	}
}
