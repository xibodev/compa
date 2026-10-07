package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRedactProxyURL(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                    "",
		"http://proxy.example.test:8080":      "http://proxy.example.test:8080",
		"http://user@proxy.example.test:8080": "http://user@proxy.example.test:8080",
		"http://user:secret@proxy.example.test:8080":   "http://user:****@proxy.example.test:8080",
		"socks5://u:p%40ss@proxy.example.test:1080":    "socks5://u:****@proxy.example.test:1080",
		"https://user:@proxy.example.test":             "https://user:****@proxy.example.test",
		"http://user:secret@proxy.example.test:bad%zz": "http://user:****@proxy.example.test:bad%zz",
	} {
		if got := redactProxyURL(raw); got != want {
			t.Errorf("redactProxyURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

const secretProxy = "http://user:secret@proxy.example.test:8080"
const redactedProxy = "http://user:****@proxy.example.test:8080"

func TestProviderInstanceResponsesRedactProxyPassword(t *testing.T) {
	_, mux, configPath := providerInstanceTestHandler(t)

	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances",
		tunedInstanceBody+`,"runtime":{"proxy":"`+secretProxy+`","rpm":5}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("create response leaks the proxy password: %s", recorder.Body.String())
	}
	if created, _ := decodeProviderInstanceResponse(t, recorder); created.Runtime == nil || created.Runtime.Proxy != redactedProxy {
		t.Fatalf("created runtime = %#v", created.Runtime)
	}
	if stored := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; stored == nil || stored.Proxy != secretProxy {
		t.Fatalf("stored runtime = %#v", stored)
	}

	for _, path := range []string{"/api/provider-instances", "/api/config"} {
		recorder = providerInstanceRequest(t, mux, http.MethodGet, path, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, recorder.Code)
		}
		// The password itself: the config also has settings such as
		// logging.redact_secrets whose names hold the word.
		if strings.Contains(recorder.Body.String(), "user:secret") || !strings.Contains(recorder.Body.String(), redactedProxy) {
			t.Fatalf("GET %s does not redact the proxy: %s", path, recorder.Body.String())
		}
	}
}

func TestProviderInstanceUpdateKeepsRedactedProxyAndReplacesNewOne(t *testing.T) {
	_, mux, configPath := providerInstanceTestHandler(t)
	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances",
		tunedInstanceBody+`,"runtime":{"proxy":"`+secretProxy+`"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// Round-tripping the redacted form keeps the stored secret.
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/tuned",
		tunedInstanceBody+`,"runtime":{"proxy":"`+redactedProxy+`","rpm":7}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stored := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; stored == nil || stored.Proxy != secretProxy || stored.RPM != 7 {
		t.Fatalf("round-trip lost the proxy secret: %#v", stored)
	}

	// A new value replaces it, even one using the same user.
	const replaced = "http://user:fresh@proxy.example.test:8080"
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/tuned",
		tunedInstanceBody+`,"runtime":{"proxy":"`+replaced+`"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("replace status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stored := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; stored == nil || stored.Proxy != replaced {
		t.Fatalf("replaced proxy = %#v", stored)
	}

	// A redacted value for a different host is not a round trip: it is saved
	// as given (the literal password "****").
	const otherHost = "http://user:****@other.example.test:8080"
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/provider-instances/tuned",
		tunedInstanceBody+`,"runtime":{"proxy":"`+otherHost+`"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("other host status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stored := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; stored == nil || stored.Proxy != otherHost {
		t.Fatalf("other host proxy = %#v", stored)
	}
}

func TestConfigRoundTripKeepsRedactedProxy(t *testing.T) {
	_, mux, configPath := providerInstanceTestHandler(t)
	recorder := providerInstanceRequest(t, mux, http.MethodPost, "/api/provider-instances",
		tunedInstanceBody+`,"runtime":{"proxy":"`+secretProxy+`"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = providerInstanceRequest(t, mux, http.MethodGet, "/api/config", "")
	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	body, _ := json.Marshal(raw)
	recorder = providerInstanceRequest(t, mux, http.MethodPut, "/api/config", string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT /api/config status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stored := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; stored == nil || stored.Proxy != secretProxy {
		t.Fatalf("PUT round-trip lost the proxy secret: %#v", stored)
	}

	patch := `{"provider_instances":` + string(mustMarshal(t, raw["provider_instances"])) + `}`
	recorder = providerInstanceRequest(t, mux, http.MethodPatch, "/api/config", patch)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH /api/config status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stored := loadSavedConfig(t, configPath).ProviderInstances[0].Runtime; stored == nil || stored.Proxy != secretProxy {
		t.Fatalf("PATCH round-trip lost the proxy secret: %#v", stored)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return raw
}
