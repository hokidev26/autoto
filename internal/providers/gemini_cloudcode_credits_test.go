package providers

import (
	"net/http"
	"testing"
)

func TestGeminiCloudCodeShouldUseGoogleOneCredits(t *testing.T) {
	quota := []byte(`{"error":{"code":429,"message":"You have exhausted your capacity on this model.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`)
	rateLimit := []byte(`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 3s.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3.9s"}]}}`)
	grpcQuota := []byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
	streamQuota := []byte(`{"response":{"error":{"code":8,"message":"quota exhausted","status":"RESOURCE_EXHAUSTED"}}}`)
	keyword := []byte(`{"error":{"message":"quota_exhausted on this model","status":"RESOURCE_EXHAUSTED"}}`)

	cases := []struct {
		name   string
		status int
		body   []byte
		want   bool
	}{
		{name: "empty 429", status: http.StatusTooManyRequests, body: nil, want: false},
		{name: "400", status: http.StatusBadRequest, body: quota, want: false},
		{name: "structured quota", status: http.StatusTooManyRequests, body: quota, want: true},
		{name: "short rate limit", status: http.StatusTooManyRequests, body: rateLimit, want: false},
		{name: "grpc quota text", status: http.StatusTooManyRequests, body: grpcQuota, want: true},
		{name: "stream envelope", status: http.StatusTooManyRequests, body: streamQuota, want: true},
		{name: "keyword", status: http.StatusTooManyRequests, body: keyword, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := geminiCloudCodeShouldUseGoogleOneCredits(tc.status, tc.body); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
