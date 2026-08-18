package llm

import "testing"

// TestVertexCredentialType guards the credential-validation hardening behind
// NewVertexProviderFromJSON: only service_account and authorized_user documents
// are accepted. Federated types (external_account, impersonated_service_account)
// embed operator-controllable endpoint URLs and must be rejected rather than
// silently trusted for authentication.
func TestVertexCredentialType(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"service account accepted", `{"type":"service_account","project_id":"p"}`, false},
		{"authorized user accepted", `{"type":"authorized_user","client_id":"c"}`, false},
		{"external account rejected", `{"type":"external_account","audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/x/providers/y"}`, true},
		{"impersonated service account rejected", `{"type":"impersonated_service_account"}`, true},
		{"malformed json rejected", `{not json`, true},
		{"empty type rejected", `{"type":""}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := vertexCredentialType([]byte(tt.payload))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got credential type %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("vertexCredentialType: %v", err)
			}
			if got != "service_account" && got != "authorized_user" {
				t.Fatalf("unexpected credential type %q", got)
			}
		})
	}
}
