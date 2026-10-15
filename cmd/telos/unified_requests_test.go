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

func TestYesDoesNotApproveAReplacementAfterAConfirmationRace(t *testing.T) {
	request := testDeploymentPlan("apply", "awaiting_confirmation")
	request.UpdateNumber, request.PreparedPlanID = 1, "cp_reviewed"
	confirms := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			confirms++
			http.Error(w, "plan replaced", http.StatusConflict)
			return
		}
		current := request
		current.UpdateNumber, current.PreparedPlanID = 2, "cp_unreviewed"
		_ = json.NewEncoder(w).Encode(current)
	}))
	defer server.Close()
	_, err := awaitCloudApply(context.Background(), cloud.NewClient(server.URL, "token"), &request, true,
		strings.NewReader(""), io.Discard, io.Discard, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "changed") || confirms != 1 {
		t.Fatalf("replacement approved: err=%v confirmations=%d", err, confirms)
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
