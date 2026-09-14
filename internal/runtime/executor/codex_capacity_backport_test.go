package executor

import "testing"

// Regression cases from upstream 3ae9093d (MIT).
func TestCodexCapacityBootstrapBackport(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"model_at_capacity"}}`,
		`{"error":{"code":"model_is_at_capacity"}}`,
		`{"error":{"message":"This model is at capacity"}}`,
	} {
		if !isCodexModelCapacityError([]byte(body)) || !isCodexOverloadBootstrapFailure([]byte(body)) {
			t.Fatalf("capacity not classified: %s", body)
		}
	}
	if isCodexOverloadBootstrapFailure([]byte(`{"error":{"code":"invalid_api_key"}}`)) {
		t.Fatal("authentication treated as overload")
	}
}
