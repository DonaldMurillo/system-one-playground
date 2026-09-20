package e2e

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"github.com/DonaldMurillo/system-one-playground/internal/studio"
)

const ambiguousEditorSource = "make tickets [{\"team\": \"a\"}]\nmake archive [{\"team\": \"b\"}]\ngroup them by team called teams\n"

func accountingStudio(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	s, err := studio.New(studio.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(s)
	t.Cleanup(host.Close)
	return host, s.Token()
}

func TestEditorRefusalRetainsKnownUsage(t *testing.T) {
	fx := semNewFixture(t, semServeLowConfidence, semChoiceRule{when: []string{"group"}, pick: []string{"tickets"}})
	setGlobalConfig(t, "version = 1\n[interpretation]\nmode = \"assisted\"\n")
	host, token := accountingStudio(t)
	resp, body := postAnalyze(t, host, token, ambiguousEditorSource)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %v", resp.StatusCode, body)
	}
	usage := usageOf(t, body)
	if usage["totalAdmitted"] != float64(1) {
		t.Fatalf("failed analysis lost request: %v", body)
	}
	buckets := usage["buckets"].(map[string]any)
	editor := buckets["editor"].(map[string]any)
	if editor["reportedInputTokens"] != float64(140) || editor["unresolved"] != float64(0) {
		t.Fatalf("failed analysis lost known usage: %v", editor)
	}
	fx.requireContacts(t, 1)
}

func TestEditorZeroBudgetRefusesBeforeProvider(t *testing.T) {
	fx := semNewFixture(t, semServeCanary)
	setGlobalConfig(t, "version = 1\n[interpretation]\nmode = \"assisted\"\n[budget.run]\nrequests = 0\n")
	host, token := accountingStudio(t)
	resp, body := postAnalyze(t, host, token, ambiguousEditorSource)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d: %v", resp.StatusCode, body)
	}
	if usageOf(t, body)["totalAdmitted"] != float64(0) {
		t.Fatalf("zero cap admitted request: %v", body)
	}
	fx.requireNoContacts(t)
}

func TestEditorConfiguredDeadlineRetainsUnresolvedUsage(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			http.Error(w, "deadline did not reach provider", 500)
		}
	}))
	defer provider.Close()
	t.Setenv("TYPESAFE_API_KEY", "transport-fixture")
	t.Setenv("TYPESAFE_BASE_URL", provider.URL)
	setGlobalConfig(t, "version = 1\n[interpretation]\nmode = \"assisted\"\n[budget.run]\nrequests = 1\ntimeout = \"100ms\"\n")
	host, token := accountingStudio(t)
	start := time.Now()
	resp, body := postAnalyze(t, host, token, ambiguousEditorSource)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d: %v", resp.StatusCode, body)
	}
	if time.Since(start) > time.Second {
		t.Fatal("configured deadline was not applied")
	}
	usage := usageOf(t, body)
	editor := usage["buckets"].(map[string]any)["editor"].(map[string]any)
	if usage["totalAdmitted"] != float64(1) || editor["unresolved"] != float64(1) {
		t.Fatalf("timeout lost admission: %v", usage)
	}
}
