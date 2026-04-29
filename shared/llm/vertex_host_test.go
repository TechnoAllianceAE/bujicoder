package llm

import "testing"

func TestVertexHostForRegion(t *testing.T) {
	cases := []struct {
		region string
		want   string
	}{
		{"us-central1", "us-central1-aiplatform.googleapis.com"},
		{"us-east5", "us-east5-aiplatform.googleapis.com"},
		{"global", "aiplatform.googleapis.com"},
		{"", "aiplatform.googleapis.com"},
	}
	for _, tc := range cases {
		if got := vertexHostForRegion(tc.region); got != tc.want {
			t.Errorf("vertexHostForRegion(%q) = %q, want %q", tc.region, got, tc.want)
		}
	}
}
