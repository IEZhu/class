package livekitapi

import "testing"

// Серверный API LiveKit живёт по тому же адресу, что и комната, но по
// http(s): схема меняется только в начале URL.
func TestNewBaseURL(t *testing.T) {
	cases := []struct{ url, want string }{
		{"wss://class.livekit.cloud", "https://class.livekit.cloud"},
		{"wss://class.livekit.cloud/", "https://class.livekit.cloud"},
		{"ws://localhost:7880", "http://localhost:7880"},
		{"https://class.livekit.cloud", "https://class.livekit.cloud"},
		// «ws://» не в начале — не схема, его не трогаем
		{"https://proxy.example/ws://x", "https://proxy.example/ws://x"},
	}
	for _, c := range cases {
		if got := New(c.url, "k", "s").baseURL; got != c.want {
			t.Errorf("New(%q).baseURL = %q, хотели %q", c.url, got, c.want)
		}
	}
}
