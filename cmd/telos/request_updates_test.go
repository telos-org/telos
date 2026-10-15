package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/telos-org/telos/internal/cloud"
)

func TestSavedFilesPinUpdateAndPreparedPlan(t *testing.T) {
	valid, invalid := true, false
	request := testDeploymentPlan("saved", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID = 1, "cp_first"
	request.LegacyConfirmationValid = &valid
	client := cloud.NewClient("https://api.example.com", "token")
	bookmark := newSavedDeploymentPlan(client, &request, "org_1")
	if bookmark.Version != 2 || bookmark.PreparedPlanID != "cp_first" || bookmark.UpdateNumber != 1 {
		t.Fatalf("bookmark=%+v", bookmark)
	}
	if err := validateSavedRequest(&bookmark, &request); err != nil {
		t.Fatal(err)
	}
	request.PreparedPlanID = "cp_replanned"
	if err := validateSavedRequest(&bookmark, &request); err == nil {
		t.Fatal("replanned contents accepted")
	}
	legacy := bookmark
	legacy.Version, legacy.PreparedPlanID, legacy.UpdateNumber = 1, "", 0
	request.LegacyConfirmationValid = &invalid
	if err := validateSavedRequest(&legacy, &request); err == nil {
		t.Fatal("legacy file silently upgraded")
	}
	request.PreparedPlanID, request.LegacyConfirmationValid, request.PlanStale = "cp_first", &valid, true
	if err := validateSavedRequest(&bookmark, &request); err == nil || !strings.Contains(err.Error(), "deployment changed") {
		t.Fatalf("stale error=%v", err)
	}
}

func TestConfirmationCannotApproveOrReportAReplacementAsSuccess(t *testing.T) {
	for _, status := range []string{"awaiting_confirmation", "applied"} {
		t.Run(status, func(t *testing.T) {
			first := testDeploymentPlan("apply", "awaiting_confirmation")
			first.UpdateNumber, first.PreparedPlanID = 1, "cp_first"
			confirms := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					confirms++
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["expected_plan_id"] != "cp_first" {
						t.Errorf("body=%v", body)
					}
					http.Error(w, "replaced", http.StatusConflict)
					return
				}
				second := first
				second.UpdateNumber, second.PreparedPlanID, second.Status = 2, "cp_second", status
				_ = json.NewEncoder(w).Encode(second)
			}))
			defer server.Close()
			_, err := awaitCloudApply(context.Background(), cloud.NewClient(server.URL, "token"), &first, true,
				strings.NewReader(""), io.Discard, time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), "changed") || confirms != 1 {
				t.Fatalf("replacement approved or reported as success: err=%v confirmations=%d", err, confirms)
			}
		})
	}
}

func TestInteractiveApplyStopsWhenPlanChangesWhilePromptIsOpen(t *testing.T) {
	first := testDeploymentPlan("apply", "awaiting_confirmation")
	first.UpdateNumber, first.PreparedPlanID = 1, "cp_first"
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations++
			http.Error(w, "unexpected", 500)
			return
		}
		second := first
		second.UpdateNumber, second.PreparedPlanID = 2, "cp_second"
		_ = json.NewEncoder(w).Encode(second)
	}))
	defer server.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	_, err := awaitCloudApply(context.Background(), cloud.NewClient(server.URL, "token"), &first, false, reader, io.Discard, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "changed") || mutations != 0 {
		t.Fatalf("err=%v mutations=%d", err, mutations)
	}
}

func TestExactSavedReferenceCanApplyFormerRegularRequest(t *testing.T) {
	request := testDeploymentPlan("apply", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID = 2, "cp_second"
	confirms := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDeploymentPlanPrerequisites(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			confirms++
			request.Status = "applied"
		}
		_ = json.NewEncoder(w).Encode(request)
	}))
	defer server.Close()
	configureCloudTest(t, server.URL)
	bookmark := newSavedDeploymentPlan(cloud.NewClient(server.URL, "token"), &request, "org_personal")
	captureStdout(t, func() {
		if err := runSavedCloudApply(&bookmark, "", true); err != nil {
			t.Fatal(err)
		}
	})
	if confirms != 1 {
		t.Fatalf("confirmations=%d", confirms)
	}
}
