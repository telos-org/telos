package cloud

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountBootstrap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/account/bootstrap" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"personal_org_id": "org_personal",
			"organizations": [
				{"id":"org_personal","handle":"grohan","display_name":"Rohan","kind":"personal","role":"owner","default_publish_scope":"grohan"},
				{"id":"org_telos","handle":"telos","display_name":"Telos","kind":"platform","role":"owner","default_publish_scope":"telos"}
			]
		}`))
	}))
	defer server.Close()

	account, err := NewClient(server.URL, "test-token").AccountBootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if account.PersonalOrgID != "org_personal" || len(account.Organizations) != 2 {
		t.Fatalf("account = %#v", account)
	}
}

func TestResolveContext(t *testing.T) {
	personalHandle := "grohan"
	platformHandle := "telos"
	account := AccountBootstrapRecord{
		PersonalOrgID: "org_personal",
		Organizations: []OrganizationRecord{
			{ID: "org_personal", Handle: &personalHandle},
			{ID: "org_telos", Handle: &platformHandle},
		},
	}

	tests := []struct {
		name    string
		context string
		want    string
		wantErr bool
	}{
		{name: "implicit personal", want: "org_personal"},
		{name: "explicit personal", context: "personal", want: "org_personal"},
		{name: "handle", context: "@telos", want: "org_telos"},
		{name: "stable id", context: "org_telos", want: "org_telos"},
		{name: "unknown handle", context: "@missing", wantErr: true},
		{name: "unrecognized value", context: "telos", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			organization, err := account.ResolveContext(tt.context)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if organization.ID != tt.want {
				t.Fatalf("organization = %q, want %q", organization.ID, tt.want)
			}
		})
	}
}
