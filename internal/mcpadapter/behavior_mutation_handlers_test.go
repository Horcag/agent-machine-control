package mcpadapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMachineList_EmptyAndSorting(t *testing.T) {
	ctx := t.Context()

	// 1. Empty List
	mockObs := &MockObserver{
		machines: []domain.MachineObservation{},
	}
	a := &Adapter{
		discoveryService: app.NewDiscoveryService(mockObs),
	}
	res, listRes, err := a.MachineList(ctx, nil, MachineListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res != nil && res.IsError {
		t.Error("expected no tool error")
	}
	if len(listRes.Machines) != 0 {
		t.Errorf("expected 0 machines, got %d", len(listRes.Machines))
	}

	// 2. Sorting by Name and ID
	mockObs.machines = []domain.MachineObservation{
		{
			ID:   "c4a523d4-6b99-4d62-a5e2-4752c0f20002",
			Name: "VM-B",
		},
		{
			ID:   "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Name: "VM-B",
		},
		{
			ID:   "c4a523d4-6b99-4d62-a5e2-4752c0f20003",
			Name: "VM-A",
		},
	}
	res2, listRes2, err := a.MachineList(ctx, nil, MachineListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res2 != nil && res2.IsError {
		t.Error("expected no tool error")
	}
	if len(listRes2.Machines) != 3 {
		t.Fatalf("expected 3 machines, got %d", len(listRes2.Machines))
	}
	if listRes2.Machines[0].Name != "VM-A" {
		t.Errorf("expected first machine to be VM-A, got %s", listRes2.Machines[0].Name)
	}
	if listRes2.Machines[1].ID != "c4a523d4-6b99-4d62-a5e2-4752c0f20001" {
		t.Errorf("expected second machine to be the one with lower ID, got %s", listRes2.Machines[1].ID)
	}

	// 3. Network adapter sorting branch in convertToMachineDTO
	mockObs.machines = []domain.MachineObservation{
		{
			ID:   "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Name: "VM-1",
			NetworkAdapters: []domain.NetworkAdapterSummary{
				{
					Name:       "Eth1",
					MACAddress: "00-11-22-33-44-55",
				},
				{
					Name:       "Eth1",
					MACAddress: "00-11-22-33-44-00",
				},
				{
					Name:       "Eth0",
					MACAddress: "00-11-22-33-44-22",
				},
			},
		},
	}
	_, listRes3, _ := a.MachineList(ctx, nil, MachineListInput{})
	adapters := listRes3.Machines[0].NetworkAdapters
	if len(adapters) != 3 {
		t.Fatalf("expected 3 adapters, got %d", len(adapters))
	}
	if adapters[0].Name != "Eth0" || adapters[1].MACAddress != "00-11-22-33-44-00" || adapters[2].MACAddress != "00-11-22-33-44-55" {
		t.Errorf("adapters sorted incorrectly: %+v", adapters)
	}
}

func TestMutation_TerminalDenial(t *testing.T) {
	ctx := t.Context()
	serverDenial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch {
		case r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"admitted"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/operations/op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"):
			_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"failed","error_message":"domain: denied by policy","error_category":"policy"}`))
		}
	}))
	defer serverDenial.Close()

	aDenial := &Adapter{client: client.New(serverDenial.URL, "token")}
	resDenial, _, _ := aDenial.MachineStart(ctx, nil, MachineStartInput{
		ID:             "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test",
		IdempotencyKey: "key-denial",
		Timeout:        "30s",
	})
	if resDenial == nil || !resDenial.IsError {
		t.Error("expected tool error for terminal denial")
	} else {
		msg := resDenial.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(msg, "operation failed") || !strings.Contains(msg, "domain: denied by policy") {
			t.Errorf("unexpected denial error message: %q", msg)
		}
	}
}

func TestMutation_MissingReceipt(t *testing.T) {
	ctx := t.Context()
	serverNoReceipt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch {
		case r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"op-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","state":"admitted"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/operations/op-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"):
			_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"op-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","state":"completed","receipt_id":""}`))
		}
	}))
	defer serverNoReceipt.Close()

	aNoRcpt := &Adapter{client: client.New(serverNoReceipt.URL, "token")}
	resNoRcpt, _, _ := aNoRcpt.MachineStart(ctx, nil, MachineStartInput{
		ID:             "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test",
		IdempotencyKey: "key-norcpt",
		Timeout:        "30s",
	})
	if resNoRcpt == nil || !resNoRcpt.IsError {
		t.Error("expected tool error for missing receipt")
	} else {
		msg := resNoRcpt.Content[0].(*mcp.TextContent).Text
		if msg != "an internal daemon error occurred" {
			t.Errorf("unexpected missing receipt message: %q", msg)
		}
	}
}

func TestMutation_ReceiptFetchFailure(t *testing.T) {
	ctx := t.Context()
	serverFetchErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"op-cccccccccccccccccccccccccccccccc","state":"admitted"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/operations/op-cccccccccccccccccccccccccccccccc"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"op-cccccccccccccccccccccccccccccccc","state":"completed","receipt_id":"rcpt-cccccccccccccccccccccccccccccccc"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/receipts/rcpt-cccccccccccccccccccccccccccccccc"):
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer serverFetchErr.Close()

	aFetchErr := &Adapter{client: client.New(serverFetchErr.URL, "token")}
	resFetchErr, _, _ := aFetchErr.MachineStart(ctx, nil, MachineStartInput{
		ID:             "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test",
		IdempotencyKey: "key-fetcherr",
		Timeout:        "30s",
	})
	if resFetchErr == nil || !resFetchErr.IsError {
		t.Error("expected tool error for receipt fetch failure")
	} else {
		msg := resFetchErr.Content[0].(*mcp.TextContent).Text
		if msg != "an internal daemon error occurred" {
			t.Errorf("unexpected fetch error message: %q", msg)
		}
	}
}
