package channels

import "testing"

// An external channel without allow_from is open to everyone and says so; a
// channel only authenticated users reach is not.
func TestBaseChannelOpenToEveryone(t *testing.T) {
	for name, tc := range map[string]struct {
		allowList []string
		opts      []BaseChannelOption
		want      bool
	}{
		"empty allow_from":                    {nil, nil, true},
		"blank allow_from":                    {[]string{""}, nil, true},
		"explicit everyone":                   {[]string{"*"}, nil, false},
		"allow-listed sender":                 {[]string{"123"}, nil, false},
		"authenticated access":                {nil, []BaseChannelOption{WithAuthenticatedAccess()}, false},
		"authenticated access and allow_from": {[]string{"123"}, []BaseChannelOption{WithAuthenticatedAccess()}, false},
	} {
		t.Run(name, func(t *testing.T) {
			ch := NewBaseChannel("test", nil, nil, tc.allowList, tc.opts...)
			if got := ch.OpenToEveryone(); got != tc.want {
				t.Fatalf("OpenToEveryone() = %v, want %v", got, tc.want)
			}
		})
	}
	// Authenticated access changes only the audit, not who is allowed.
	if ch := NewBaseChannel("test", nil, nil, nil, WithAuthenticatedAccess()); !ch.IsAllowed("anyone") {
		t.Fatal("an authenticated channel without allow_from refuses its senders")
	}
}
