package provider

import (
	"testing"
	"time"
)

func TestTokenExpiryPrecedenceAndFirstValidField(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	for _, test := range []struct {
		name  string
		token copilotTokenResponse
		want  time.Time
	}{
		{name: "explicit wins", token: copilotTokenResponse{ExpiresAt: 200, Token: "exp=300", RefreshIn: 5}, want: time.Unix(200, 0)},
		{name: "first valid field wins", token: copilotTokenResponse{Token: "kind=x; exp=300; exp=400", RefreshIn: 5}, want: time.Unix(300, 0)},
		{name: "invalid fields skipped", token: copilotTokenResponse{ExpiresAt: -1, Token: "exp=no;exp=0;exp=-5;exp=400"}, want: time.Unix(400, 0)},
		{name: "first expired timestamp retained", token: copilotTokenResponse{Token: "exp=50;exp=400"}, want: time.Unix(50, 0)},
		{name: "case sensitive field", token: copilotTokenResponse{Token: "EXP=300;exp=400"}, want: time.Unix(400, 0)},
		{name: "refresh fallback", token: copilotTokenResponse{Token: "exp=no;missing", RefreshIn: 5}, want: now.Add(5 * time.Second)},
		{name: "default fallback", token: copilotTokenResponse{RefreshIn: -1}, want: now.Add(20 * time.Minute)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := tokenExpiry(test.token, now); !got.Equal(test.want) {
				t.Errorf("expiry = %v, want %v", got, test.want)
			}
		})
	}
}
