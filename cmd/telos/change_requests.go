package main

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

func cloudChangeRequestURL(control *cloud.Client, sessionID, requestID string) string {
	base, err := url.Parse(control.Endpoint)
	if err != nil {
		return ""
	}
	base.Host = strings.TrimPrefix(base.Host, "api.")
	base.Path = "/deployments/" + url.PathEscape(sessionID)
	query := url.Values{"tab": {"change-requests"}}
	if requestID != "" {
		query.Set("request", requestID)
	}
	if control.OrgID != "" {
		query.Set("org", strings.TrimSpace(control.OrgID))
	}
	base.RawQuery = query.Encode()
	return base.String()
}

func cloudRequestReviewURL(control *cloud.Client, request cloud.ChangeRequestRecord) string {
	if request.ReviewURL != "" {
		return request.ReviewURL
	}
	return cloudChangeRequestURL(control, request.DeploymentID, request.ID)
}

func printCloudChangeRequestReceipt(out io.Writer, result *cloud.SessionMutationResult, contextName, reviewURL string) {
	request := result.ChangeRequest
	deployment := result.Deployment
	fmt.Fprintf(out, "requested %s\n\n", deployment.Name)
	printSummaryField(out, "Request", request.ID)
	printSummaryField(out, "Status", changeRequestStatus(request.Status))
	printSummaryField(out, "Action", request.Action)
	printSummaryField(out, "Session", deployment.ID)
	printSummaryField(out, "Proposed", request.PackageDigest)
	if deployment.CurrentRevisionID != "" {
		printSummaryField(out, "Current", deployment.CurrentRevisionID)
	} else if request.Action == "create" {
		printSummaryField(out, "Current", "not deployed yet")
	}
	if request.QueuePosition != nil {
		printSummaryField(out, "Queue", fmt.Sprint(*request.QueuePosition))
	}
	if contextName != "" {
		printSummaryField(out, "Context", contextName)
	}
	printSummaryField(out, "Review", reviewURL)
}

func changeRequestStatus(status string) string {
	switch status {
	case "awaiting_confirmation":
		return "awaiting confirmation"
	default:
		return strings.ReplaceAll(status, "_", " ")
	}
}

func pendingCloudChangeRequests(control *cloud.Client, sessionID string) ([]cloud.ChangeRequestRecord, error) {
	capabilities, err := control.DeploymentCapabilities()
	if err != nil || !capabilities.DeploymentChangeRequests {
		return nil, err
	}
	requests, err := control.ListChangeRequests(sessionID)
	if err != nil {
		return nil, err
	}
	pending := make([]cloud.ChangeRequestRecord, 0, len(requests))
	for _, request := range requests {
		if request.Pending() {
			pending = append(pending, request)
		}
	}
	return pending, nil
}

func printPendingCloudChangeRequests(out io.Writer, control *cloud.Client, requests []cloud.ChangeRequestRecord) {
	if len(requests) == 0 {
		return
	}
	fmt.Fprintln(out, "\nChange requests")
	for _, request := range requests {
		printDetailField(out, request.ID, request.Action+" — "+changeRequestStatus(request.Status))
		printDetailField(out, "Review", cloudRequestReviewURL(control, request))
	}
}
