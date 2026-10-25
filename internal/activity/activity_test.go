package activity

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// This complete wire contract is mirrored by cloud's retained-runtime adapter.
// cloud/scripts/check_activity_contract.py verifies both copies and projections.
func TestActivityContract(t *testing.T) {
	data, err := os.ReadFile("testdata/activity.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string
		Event    map[string]any
		Costs    *bool
		Expected map[string]any
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := Project(tc.Event, "", tc.Costs == nil || *tc.Costs)
			if tc.Expected == nil {
				if got != nil {
					t.Fatalf("hidden event displayed: %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("visible event hidden")
			}
			encoded, _ := json.Marshal(got)
			var fields map[string]any
			_ = json.Unmarshal(encoded, &fields)
			if !reflect.DeepEqual(fields, tc.Expected) {
				t.Errorf("entry = %#v, want %#v", fields, tc.Expected)
			}
		})
	}
}
