/*
===========================================================================

operator_premium_test.go - the premium clear's admission and dispatch

===========================================================================
*/
package agentapi

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

/*
================
TestPlayerOperationsClearPremiumDispatch
================
*/
func TestPlayerOperationsClearPremiumDispatch(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	calls := 0
	config := PlayerOperations{Token: strings.Repeat("k", 32), AuditPath: audit,
		Read:    func(string) (any, error) { return map[string]any{}, nil },
		Rescue:  func(PlayerOperation) (any, error) { t.Fatal("premium clear dispatched to rescue"); return nil, nil },
		ClearPK: func(PlayerOperation) (any, error) { t.Fatal("premium clear dispatched to clear-pk"); return nil, nil },
		ClearPremium: func(request PlayerOperation) (any, error) {
			calls++
			return map[string]any{"character": request.Character}, nil
		},
	}
	api := &API{}
	if err := api.InstallPlayerOperations(config); err != nil {
		t.Fatal(err)
	}
	send := func(id string, input PlayerOperation) int {
		input.ID, input.Operator, input.Character, input.Reason, input.Action = id, "operator", "Probe", "Refund request", "clear-premium"
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "http://127.0.0.1:8791/internal/operations/player", strings.NewReader(string(data)))
		request.RemoteAddr = "127.0.0.1:1234"
		request.Header.Set("X-SRO-Local-Diagnostics", "1")
		request.Header.Set("Authorization", "Bearer "+config.Token)
		response := httptest.NewRecorder()
		api.operator.ServeHTTP(response, request)
		return response.Code
	}
	for _, invalid := range []PlayerOperation{{Town: 2}, {Silk: 5}, {Items: []OperatorItemGrant{{Codename: "ITEM", Count: 1}}}} {
		if send("invalid-clear-premium-1", invalid) != 400 || calls != 0 {
			t.Fatal("invalid premium clear admitted")
		}
	}
	api.operator.(*operatorEndpoint).config.ClearPremium = nil
	if send("missing-clear-premium-1", PlayerOperation{}) != 400 {
		t.Fatal("missing callback admitted")
	}
	api.operator.(*operatorEndpoint).config.ClearPremium = config.ClearPremium
	if code := send("unique-clear-premium-1", PlayerOperation{}); code != 200 || calls != 1 {
		t.Fatalf("premium clear: %d, %d calls", code, calls)
	}
}
